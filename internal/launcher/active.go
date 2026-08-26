package launcher

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	Schema          = 1
	ProtocolVersion = "v1.0.0"
	maxPayloadBytes = 256 << 20
)

var canonicalVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Reason codes name why an install could not be used, in words that stay the
// same across operating systems and filesystems. The Go error text behind one
// does not: "GetFileAttributesEx ...: The system cannot find the file
// specified" and "no such file or directory" are the same fact. A caller that
// has to decide "is this install broken, or did my command fail?" -- an MCP
// host reading stderr, an operator reading a recovery diagnostic -- matches on
// these.
const (
	ReasonPointerUnreadable  = "pointer-unreadable"
	ReasonDescriptorInvalid  = "descriptor-invalid"
	ReasonPayloadMissing     = "payload-missing"
	ReasonPayloadNotRegular  = "payload-not-regular"
	ReasonPayloadUnreadable  = "payload-unreadable"
	ReasonPayloadCorrupt     = "payload-corrupt"
	ReasonPayloadStartFailed = "payload-start-failed"
)

var (
	errDescriptorInvalid  = errors.New("invalid descriptor")
	errPayloadMissing     = errors.New("payload file is missing")
	errPayloadNotRegular  = errors.New("payload is not a regular file")
	errPayloadUnreadable  = errors.New("payload could not be read")
	errPayloadCorrupt     = errors.New("payload SHA-256 mismatch")
	errPayloadStartFailed = errors.New("payload process could not start")
)

// Reason classifies an error from this package into a stable reason code.
// Anything it does not recognize is reported as an unusable pointer, which is
// the truthful default: the launcher could not establish what to run.
func Reason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errPayloadMissing):
		return ReasonPayloadMissing
	case errors.Is(err, errPayloadNotRegular):
		return ReasonPayloadNotRegular
	case errors.Is(err, errPayloadCorrupt):
		return ReasonPayloadCorrupt
	case errors.Is(err, errPayloadUnreadable):
		return ReasonPayloadUnreadable
	case errors.Is(err, errDescriptorInvalid):
		return ReasonDescriptorInvalid
	case errors.Is(err, errPayloadStartFailed):
		return ReasonPayloadStartFailed
	default:
		return ReasonPointerUnreadable
	}
}

type Descriptor struct {
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Sequence uint64 `json:"sequence"`
}

type Active struct {
	Schema       int         `json:"schema"`
	Current      Descriptor  `json:"current"`
	Previous     *Descriptor `json:"previous,omitempty"`
	RollbackHold *Descriptor `json:"rollbackHold,omitempty"`
}

func Path(root string) string { return filepath.Join(root, "active.json") }

func PayloadPath(root, version string) (string, error) {
	if !canonicalVersion.MatchString(version) {
		return "", fmt.Errorf("launcher: %w: noncanonical payload version %q", errDescriptorInvalid, version)
	}
	return filepath.Join(root, "payloads", version, "csx-payload.exe"), nil
}

func Load(root string) (Active, error) {
	a, err := Read(root)
	if err != nil {
		return Active{}, err
	}
	if err := validateCurrent(root, a); err != nil {
		return Active{}, err
	}
	return a, nil
}

// Resolution is the payload the launcher will execute and, when the recorded
// current turned out to be unusable, how it got back to a verified one.
type Resolution struct {
	Descriptor  Descriptor
	PayloadPath string

	// Recovered is set when current failed verification and a descriptor this
	// same pointer already recorded as previous passed it instead.
	// RollbackHold is rejection metadata, never an execution candidate.
	// FailedVersion and FailedReason describe the rejected current.
	Recovered     bool
	FailedVersion string
	FailedReason  string

	// Healed reports whether the recovered pointer was written back to disk. A
	// read-only or contended install still runs the fallback payload for this
	// invocation; HealError then says why the pointer itself stayed broken.
	Healed    bool
	HealError error
}

// Resolve picks the payload to execute, falling back to the last known good one
// when current cannot be verified.
//
// A verified payload does not stay verified. On Windows the install lost its
// current payload twice to Defender quarantining the executable minutes after a
// correctly staged, hashed and self-tested update had committed it -- the
// pointer was right, the file was simply gone. Failing hard there takes csx and
// its MCP server down over a payload the same pointer still records a working
// alternative for, and leaves no way back: Rollback and every ownership check
// in internal/update load the pointer, which verifies current first.
//
// So an unusable current is recoverable, not fatal. Only descriptors this
// pointer recorded with their own SHA-256 are candidates: a payload directory
// left on disk by an older release was never verified by this install and is
// not adopted. Nothing is ever executed unverified, and when no candidate
// verifies, Resolve fails with a reason instead of returning something to run.
func Resolve(root string) (Resolution, error) {
	a, err := Read(root)
	if err != nil {
		return Resolution{}, err
	}
	currentErr := validateDescriptor(root, a.Current)
	if currentErr == nil {
		path, err := PayloadPath(root, a.Current.Version)
		if err != nil {
			return Resolution{}, err
		}
		return Resolution{Descriptor: a.Current, PayloadPath: path}, nil
	}
	return resolveFallback(root, a, currentErr)
}

// RecoverAfterStartFailure closes the verification-to-CreateProcess window.
// A payload can hash correctly and then be quarantined, have its permission
// changed, or otherwise become unstartable before the operating system opens
// it. That is the same unusable-current condition as a failed hash check, so a
// descriptor this pointer already recorded as last-known-good gets one bounded
// retry. The launcher never retries an unrecorded directory or loops twice.
func RecoverAfterStartFailure(root string, failed Descriptor, startErr error) (Resolution, error) {
	a, err := Read(root)
	if err != nil {
		return Resolution{}, fmt.Errorf("launcher: recover after payload start failure: %w: %v", errPayloadStartFailed, err)
	}
	if !samePayload(a.Current, failed) {
		// An updater won the race after the failed process start. Resolve its
		// freshly published pointer rather than healing from stale bytes.
		res, err := Resolve(root)
		if err != nil {
			return Resolution{}, fmt.Errorf("launcher: recover after payload start failure: %w: %v", errPayloadStartFailed, err)
		}
		return res, nil
	}
	return resolveFallback(root, a, fmt.Errorf("%w: %v", errPayloadStartFailed, startErr))
}

func resolveFallback(root string, a Active, currentErr error) (Resolution, error) {
	failed := fmt.Errorf("launcher: current payload %s: %w", a.Current.Version, currentErr)
	candidate := a.Previous
	// RollbackHold records an explicitly rejected release. Rollback currently
	// keeps that descriptor in Previous as ownership/history metadata too, so
	// execution eligibility must be stricter than mere pointer membership.
	// Never automatically reactivate the rejected artifact, even if only its
	// sequence was promoted later. Hold remains useful to the updater as a
	// sequence floor and automatic-reinstall suppression marker.
	if candidate == nil || candidate.Version == a.Current.Version ||
		(a.RollbackHold != nil && samePayload(*candidate, *a.RollbackHold)) ||
		validateDescriptor(root, *candidate) != nil {
		return Resolution{}, fmt.Errorf("%w; no verified fallback payload remains", failed)
	}
	path, err := PayloadPath(root, candidate.Version)
	if err != nil {
		return Resolution{}, fmt.Errorf("%w; no verified fallback payload remains", failed)
	}
	res := Resolution{
		Descriptor:    *candidate,
		PayloadPath:   path,
		Recovered:     true,
		FailedVersion: a.Current.Version,
		FailedReason:  Reason(currentErr),
	}
	res.Healed, res.HealError = heal(root, a, *candidate)
	return res, nil
}

// heal writes the recovery back so the rest of csx sees a consistent install:
// internal/update loads this pointer for every ownership and update decision,
// so an unhealed pointer keeps the machine from ever fetching a working payload
// again. Failure to write is reported, not returned as an error -- this
// invocation can still run the verified fallback.
//
// The rejected version is kept as rollbackHold rather than dropped. That holds
// the automatic updater back from reinstalling the exact payload that just
// failed to run, while still letting a genuinely newer release through, and it
// preserves the sequence floor mergeLauncherFloor reads off this pointer.
func heal(root string, seen Active, candidate Descriptor) (bool, error) {
	// CommitPayload and rollback already hold this install-scoped lock while
	// changing active.json. Join that same protocol before the compare/write:
	// comparing first and locking later would still let an updater commit a new
	// current in the gap and then have this stale recovery overwrite it.
	unlock, err := acquireRecoveryInstallLock(root, 5*time.Second)
	if err != nil {
		return false, err
	}
	defer unlock()

	// An updater committing a good payload between the Read above and this
	// lock acquisition is now complete. Re-read inside the critical section and
	// stand down if anything moved; the fallback we return is verified either
	// way.
	if fresh, err := Read(root); err != nil {
		return false, err
	} else if fresh.Current != seen.Current {
		return false, errors.New("launcher: active pointer changed during recovery")
	}
	hold := seen.Current
	next := Active{Schema: Schema, Current: candidate, RollbackHold: &hold}
	if err := Write(root, next); err != nil {
		return false, err
	}
	return true, nil
}

// acquireRecoveryInstallLock speaks the updater's existing .update.lock
// protocol: exclusive creation plus a random owner token and pid. Recovery
// uses the updater's same conservative stale-owner rule: it only reclaims a
// lock whose named process is proven dead, or malformed content older than a
// full day. This matters because an invalid current plus a lock left by a
// crashed updater otherwise prevents both pointer healing and the fallback
// payload's own update command from ever reaching updater-side cleanup.
var acquireRecoveryInstallLock = func(root string, wait time.Duration) (func(), error) {
	return AcquireUpdateLock(filepath.Join(root, ".update.lock"), wait)
}

// AcquireUpdateLock acquires the token/pid lock shared by launcher recovery
// and updater writes. It is exported only within this repository's internal
// packages so every Windows participant uses the same identity-safe stale-file
// takeover instead of reintroducing a read-path/remove-path race.
func AcquireUpdateLock(path string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tokenRaw := make([]byte, 16)
	if _, err := rand.Read(tokenRaw); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenRaw)
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err := fmt.Fprintf(f, "%s %d\n", token, os.Getpid()); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return func() { releaseRecoveryInstallLock(path, token) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			if recoveryLockCreateErrorIsTransient(err) {
				if !time.Now().Before(deadline) {
					return nil, errors.New("launcher: install update lock is busy")
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("launcher: acquire install update lock: %w", err)
		}
		unlock, acquired, retry, takeoverErr := tryTakeOverRecoveryInstallLock(path, token)
		if takeoverErr != nil {
			return nil, takeoverErr
		}
		if acquired {
			return unlock, nil
		}
		if retry {
			continue
		}
		if !time.Now().Before(deadline) {
			return nil, errors.New("launcher: install update lock is busy")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A malformed lock may be observed between O_EXCL creation and the owner's
// first write, so age alone may reclaim it only after far longer than any real
// update. A parsed live owner is never overruled, however old the file is.
const namelessRecoveryLockAbandonedAfter = 24 * time.Hour

// recoveryLockBeforeDisposition is a test seam for the exact Windows window
// between inspecting a pinned lock handle and marking that same file deleted.
var recoveryLockBeforeDisposition = func() {}

func recoveryInstallLockRecordIsStale(raw []byte, modTime time.Time) bool {
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return time.Since(modTime) > namelessRecoveryLockAbandonedAfter
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		return time.Since(modTime) > namelessRecoveryLockAbandonedAfter
	}
	return !recoveryLockPidAlive(pid)
}

func releaseRecoveryInstallLock(path, token string) {
	deadline := time.Now().Add(time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err != nil || !strings.HasPrefix(string(raw), token+" ") {
			return
		}
		if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Read parses and structurally validates the pointer without hashing payloads.
// The launcher itself uses Load before execution; stale-process polling uses
// Read so every MCP tool call does not reread the complete executable.
func Read(root string) (Active, error) {
	path := Path(root)
	fi, err := os.Stat(path)
	if err != nil {
		return Active{}, err
	}
	if fi.Size() > 64<<10 {
		return Active{}, errors.New("launcher: active.json exceeds size limit")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Active{}, err
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return Active{}, err
	}
	var a Active
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return Active{}, fmt.Errorf("launcher: parse active.json: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Active{}, errors.New("launcher: active.json has trailing data")
	}
	if a.Schema != Schema {
		return Active{}, fmt.Errorf("launcher: unsupported active schema %d", a.Schema)
	}
	if err := validateDescriptorShape(a.Current); err != nil {
		return Active{}, err
	}
	if a.Previous != nil {
		if err := validateDescriptorShape(*a.Previous); err != nil {
			return Active{}, err
		}
	}
	if a.RollbackHold != nil {
		if err := validateDescriptorShape(*a.RollbackHold); err != nil {
			return Active{}, err
		}
	}
	return a, nil
}

func rejectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyTok.(string)
				if !ok {
					return errors.New("launcher: invalid JSON object key")
				}
				if seen[key] {
					return fmt.Errorf("launcher: duplicate JSON key %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		default:
			return errors.New("launcher: invalid JSON delimiter")
		}
	}
	return walk()
}

func Validate(root string, a Active) error {
	if err := validateCurrent(root, a); err != nil {
		return err
	}
	// Rollback keeps the explicitly rejected artifact in both Previous and
	// RollbackHold as ownership/floor metadata. It is not an executable LKG;
	// requiring it to remain on disk would let Defender quarantine of a version
	// the operator already rejected block every future verified update.
	if a.Previous != nil && (a.RollbackHold == nil || !samePayload(*a.Previous, *a.RollbackHold)) {
		if err := validateDescriptor(root, *a.Previous); err != nil {
			return fmt.Errorf("launcher: previous payload: %w", err)
		}
	}
	return nil
}

func validateCurrent(root string, a Active) error {
	if a.Schema != Schema {
		return fmt.Errorf("launcher: unsupported active schema %d", a.Schema)
	}
	if err := validateDescriptor(root, a.Current); err != nil {
		return fmt.Errorf("launcher: current payload: %w", err)
	}
	return nil
}

func validateDescriptor(root string, d Descriptor) error {
	if err := validateDescriptorShape(d); err != nil {
		return err
	}
	path, _ := PayloadPath(root, d.Version)
	if err := validateContainedRegular(root, path); err != nil {
		return err
	}
	sum, err := fileHash(path)
	if err != nil {
		return err
	}
	if sum != d.SHA256 {
		return errPayloadCorrupt
	}
	return nil
}

func validateDescriptorShape(d Descriptor) error {
	if !canonicalVersion.MatchString(d.Version) || d.Sequence == 0 || len(d.SHA256) != 64 || strings.ToLower(d.SHA256) != d.SHA256 {
		return errDescriptorInvalid
	}
	if _, err := hex.DecodeString(d.SHA256); err != nil {
		return fmt.Errorf("%w: invalid SHA-256", errDescriptorInvalid)
	}
	return nil
}

func validateContainedRegular(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: payload escapes install root", errPayloadNotRegular)
	}
	for current := pathAbs; ; current = filepath.Dir(current) {
		fi, err := os.Lstat(current)
		if err != nil {
			return classifyStat(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 || hasReparsePoint(current) {
			return fmt.Errorf("%w: reparse/symlink path refused", errPayloadNotRegular)
		}
		if strings.EqualFold(filepath.Clean(current), filepath.Clean(rootAbs)) {
			break
		}
		if parent := filepath.Dir(current); parent == current {
			return fmt.Errorf("%w: install root was not reached", errPayloadNotRegular)
		}
	}
	fi, err := os.Lstat(pathAbs)
	if err != nil {
		return classifyStat(err)
	}
	if !fi.Mode().IsRegular() {
		return errPayloadNotRegular
	}
	return nil
}

// classifyStat separates "the payload is gone" -- the shape a quarantined or
// half-finished update leaves behind, and the one worth recovering from -- from
// a filesystem that refused to answer, which recovery cannot assume anything
// about.
func classifyStat(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %v", errPayloadMissing, err)
	}
	return fmt.Errorf("%w: %v", errPayloadUnreadable, err)
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", classifyStat(err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxPayloadBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: %v", errPayloadUnreadable, err)
	}
	if n > maxPayloadBytes {
		return "", fmt.Errorf("%w: payload exceeds size limit", errPayloadCorrupt)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Write(root string, a Active) error {
	if err := Validate(root, a); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".active-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := replacePointer(tmp, Path(root)); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func CommitPayload(root, staged string, d Descriptor) (Active, error) {
	// Keep the structurally valid pointer even if Defender removed current
	// after the updater's preflight. Load would discard Previous together with
	// the now-invalid Current, leaving the newly committed payload with no LKG.
	old, readErr := Read(root)
	loadErr := readErr
	if readErr == nil {
		loadErr = validateCurrent(root, old)
	}
	if loadErr == nil && old.Current.Version == d.Version && old.Current.SHA256 == d.SHA256 {
		if d.Sequence < old.Current.Sequence {
			return Active{}, errors.New("launcher: refused descriptor sequence rollback")
		}
		if d.Sequence == old.Current.Sequence {
			return old, nil
		}
		old.Current.Sequence = d.Sequence
		if err := Write(root, old); err != nil {
			return Active{}, err
		}
		return old, nil
	}
	final, err := PayloadPath(root, d.Version)
	if err != nil {
		return Active{}, err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(final)), 0o700); err != nil {
		return Active{}, err
	}
	created := true
	if err := os.Mkdir(filepath.Dir(final), 0o700); err != nil {
		if !os.IsExist(err) {
			return Active{}, err
		}
		created = false
	}
	// A commit that fails partway must leave nothing addressable behind: an
	// empty payloads/<version>/ is the exact shape an invalid current takes on
	// disk, and a promoted file that then failed verification would squat on an
	// immutable version path the real payload can never reclaim. Only what this
	// call created is ever undone -- a payload already at that path is left
	// alone, and os.Remove refuses a non-empty directory -- so the current and
	// last-known-good payloads cannot be reached from here.
	committed, promoted := false, false
	defer func() {
		if committed {
			return
		}
		if promoted {
			_ = os.Remove(final)
		}
		if created {
			_ = os.Remove(filepath.Dir(final))
		}
	}()
	if _, err := os.Stat(final); err == nil {
		if err := validateDescriptor(root, d); err != nil {
			return Active{}, errors.New("launcher: immutable version path contains different payload")
		}
	} else if !os.IsNotExist(err) {
		return Active{}, err
	} else if err := promotePayload(staged, final); err != nil {
		return Active{}, err
	} else {
		promoted = true
	}
	if err := validateDescriptor(root, d); err != nil {
		return Active{}, err
	}
	committed = true
	next := Active{Schema: Schema, Current: d}
	if readErr == nil {
		next.Previous = verifiedPreviousForCommit(root, old, d)
	}
	if err := Write(root, next); err != nil {
		return Active{}, err
	}
	return next, nil
}

func verifiedPreviousForCommit(root string, old Active, next Descriptor) *Descriptor {
	for _, candidate := range []*Descriptor{&old.Current, old.Previous} {
		if candidate == nil || (candidate.Version == next.Version && candidate.SHA256 == next.SHA256) {
			continue
		}
		if old.RollbackHold != nil && samePayload(*candidate, *old.RollbackHold) {
			continue
		}
		if validateDescriptor(root, *candidate) != nil {
			continue
		}
		verified := *candidate
		return &verified
	}
	return nil
}

func samePayload(a, b Descriptor) bool {
	return a.Version == b.Version && a.SHA256 == b.SHA256
}

func ImportPrevious(root, source string, d Descriptor) (Active, error) {
	if err := validateDescriptorShape(d); err != nil {
		return Active{}, err
	}
	f, err := os.Open(source)
	if err != nil {
		return Active{}, err
	}
	defer f.Close()
	tmp, err := os.CreateTemp(root, ".legacy-payload-*.exe")
	if err != nil {
		return Active{}, err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return Active{}, err
	}
	n, err := io.Copy(tmp, io.LimitReader(f, maxPayloadBytes+1))
	if err != nil {
		_ = tmp.Close()
		return Active{}, err
	}
	if n > maxPayloadBytes {
		_ = tmp.Close()
		return Active{}, errors.New("launcher: legacy payload exceeds size limit")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Active{}, err
	}
	if err := tmp.Close(); err != nil {
		return Active{}, err
	}
	final, err := PayloadPath(root, d.Version)
	if err != nil {
		return Active{}, err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(final)), 0o700); err != nil {
		return Active{}, err
	}
	if err := os.Mkdir(filepath.Dir(final), 0o700); err != nil && !os.IsExist(err) {
		return Active{}, err
	}
	if _, err := os.Stat(final); os.IsNotExist(err) {
		if err := promotePayload(tmpName, final); err != nil {
			return Active{}, err
		}
		cleanup = false
	}
	if err := validateDescriptor(root, d); err != nil {
		return Active{}, err
	}
	a, err := Load(root)
	if err != nil {
		return Active{}, err
	}
	if a.Previous == nil {
		a.Previous = &d
		if err := Write(root, a); err != nil {
			return Active{}, err
		}
	}
	return a, nil
}

// Rollback returns the install to its previous payload.
//
// It reads the pointer instead of loading it: a current that will not verify is
// the state people reach for rollback in, and verifying it first made the
// documented recovery unusable in exactly that case.
func Rollback(root string) (Active, error) {
	a, err := Read(root)
	if err != nil {
		return Active{}, err
	}
	if a.Previous == nil {
		return Active{}, errors.New("launcher: no previous payload")
	}
	if a.RollbackHold != nil && samePayload(*a.Previous, *a.RollbackHold) {
		return Active{}, errors.New("launcher: previous payload is explicitly rollback-held")
	}
	if err := validateDescriptor(root, *a.Previous); err != nil {
		return Active{}, fmt.Errorf("launcher: previous payload: %w", err)
	}
	if a.Previous.Sequence >= a.Current.Sequence {
		return Active{}, errors.New("launcher: rollback was already applied or previous payload is not older")
	}
	hold := a.Current
	next := Active{Schema: Schema, Current: *a.Previous, RollbackHold: &hold}
	// The rejected version stays recorded as previous only while its payload is
	// still runnable. Keeping an unrunnable one there would fail Write's
	// validation and take the whole rollback down with it -- and it could never
	// serve as the fallback previous is there to be.
	if validateDescriptor(root, a.Current) == nil {
		current := a.Current
		next.Previous = &current
	}
	if err := Write(root, next); err != nil {
		return Active{}, err
	}
	return next, nil
}
