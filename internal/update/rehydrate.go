package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

// DefaultReleaseDownloadBase is the official release download root. Every
// rehydrated payload comes from underneath it, and the exact path is rebuilt
// from the version and target rather than taken from anywhere on this machine.
const DefaultReleaseDownloadBase = "https://github.com/r2cuerdame/CodeSampleX/releases/download"

// RehydrateCooldown bounds how often a failing install repeats a repair it
// cannot complete. An editor or MCP host restarts a broken csx dozens of times
// a day; without this, every one of those would pay a full network timeout on
// a machine that is simply offline. An explicit operator repair ignores it.
const RehydrateCooldown = 5 * time.Minute

// ErrRehydrateCooldown reports that a failed repair was attempted too recently.
var ErrRehydrateCooldown = errors.New("update: payload repair was attempted too recently")

// ErrRehydrateNotNeeded reports that the pointer's current payload already
// verifies, so there was nothing to repair.
var ErrRehydrateNotNeeded = errors.New("update: the current payload is already verified")

// RehydrateOptions configures one repair.
type RehydrateOptions struct {
	// HTTP is a test seam. Production leaves it nil, which is also what makes
	// the official-release URL check mandatory: httptest addresses are
	// deliberately not trusted download hosts.
	HTTP *http.Client
	// BaseURL overrides DefaultReleaseDownloadBase and is honored only
	// alongside an injected HTTP client.
	BaseURL string
	// SelfTest defaults to running `<staged> version`, which is how a payload
	// that downloads and hashes correctly but cannot execute is caught before
	// it is promoted.
	SelfTest func(ctx context.Context, path, version string) error
	// Arch defaults to runtime.GOARCH; OS is always the running one, because a
	// payload for another target could never have produced this pointer.
	Arch string
	// Force skips the cooldown. Explicit operator repair sets it.
	Force bool
	Now   func() time.Time
}

// RehydrateReport says what one repair did. It is returned on success and on
// failure, because "current came back but the fallback did not" is a different
// operational state from "nothing came back".
type RehydrateReport struct {
	// ExhaustedVersion is the current payload the install could not run.
	ExhaustedVersion string
	// Restored names the versions whose bytes were refetched and verified.
	Restored []string
	// AlreadyVerified names candidates that turned out to be fine on disk.
	AlreadyVerified []string
	// FallbackVersion is the verified previous payload the install has after
	// the repair, empty when the pointer records none.
	FallbackVersion string
}

// RehydrateInstall refetches the payloads this install's pointer already
// records, from the official release path they were installed from.
//
// It is the answer to the one failure the launcher's last-known-good recovery
// cannot handle. That recovery works by running a payload the pointer recorded
// earlier and that is still on disk; Defender quarantining `csx-payload.exe`
// as a false positive takes files, not pointers, and on 2026-08-29 it took the
// current payload and the recorded fallback on the same machine. The pointer
// was still exactly right. Every byte it named was gone, so there was nothing
// left on the machine to run — including the csx that owns `csx update`.
//
// What makes this safe is that it adopts nothing. The descriptor's SHA-256 was
// recorded by this install from a signed manifest when the payload was
// committed, and it is the only thing the refetched bytes are accepted
// against: a hash mismatch, a truncated transfer, a substituted file and a
// redirect off the release path all end the same way, with the payload path
// untouched. A payload directory that merely happens to sit on this disk is
// still never adopted, exactly as launcher.Resolve refuses to. And the active
// pointer is not written here at all — this restores bytes the pointer already
// chose, and leaves every pointer change to the verified-only paths that own
// it.
//
// It restores the recorded fallback as well as current, so a repaired install
// gets its minimum recovery set back rather than the single payload it needs
// to boot: the next quarantine is then handled locally, without the network.
func RehydrateInstall(ctx context.Context, root string, opts RehydrateOptions) (RehydrateReport, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	report, err := rehydrate(ctx, root, opts, now)
	if errors.Is(err, ErrRehydrateCooldown) || errors.Is(err, ErrRehydrateNotNeeded) {
		return report, err
	}
	if err == nil && report.ExhaustedVersion == "" && len(report.Restored) == 0 {
		// An explicit repair on a healthy install did nothing. Recording that
		// would put a repair line into `csx update status` for an install that
		// never lost a payload, which is exactly the noise that teaches an
		// operator to ignore the line when it matters.
		return report, nil
	}
	rec := launcher.RehydrateRecord{
		Outcome:          launcher.RehydrateOutcomeRestored,
		ExhaustedVersion: report.ExhaustedVersion,
		RestoredVersions: report.Restored,
	}
	if err != nil {
		rec.Outcome = launcher.RehydrateOutcomeFailed
		rec.Error = err.Error()
	}
	// Evidence must never be the reason a repair is reported as failed: this
	// runs on an install that is already broken.
	_ = launcher.RecordRehydrate(root, rec, now())
	return report, err
}

func rehydrate(ctx context.Context, root string, opts RehydrateOptions, now func() time.Time) (RehydrateReport, error) {
	var report RehydrateReport
	// The pointer is the only trust anchor a repair has. If it cannot even be
	// parsed there is no recorded digest to hold refetched bytes to, and
	// guessing one would be the adoption this whole path refuses.
	if _, err := launcher.Read(root); err != nil {
		return report, fmt.Errorf("update: payload repair needs a readable active pointer: %w", err)
	}
	if !opts.Force {
		if prev, ok, err := launcher.ReadRehydrateRecord(root); err == nil && ok &&
			prev.Outcome == launcher.RehydrateOutcomeFailed &&
			now().UTC().Sub(prev.AttemptedAt) < RehydrateCooldown {
			return report, fmt.Errorf("%w (last attempt %s: %s)", ErrRehydrateCooldown,
				prev.AttemptedAt.Format(time.RFC3339), prev.Error)
		}
	}
	// Join the same install lock CommitPayload, rollback and launcher recovery
	// hold. A repair that raced a committing updater could otherwise replace a
	// version path underneath it.
	unlock, err := launcher.AcquireUpdateLock(filepath.Join(root, ".update.lock"), 5*time.Second)
	if err != nil {
		return report, err
	}
	defer unlock()

	// Re-read under the lock: an updater may have published a working payload
	// while this process was deciding to repair.
	a, err := launcher.Read(root)
	if err != nil {
		return report, fmt.Errorf("update: payload repair needs a readable active pointer: %w", err)
	}
	if launcher.VerifyPayload(root, a.Current) == nil {
		if !opts.Force {
			return report, ErrRehydrateNotNeeded
		}
	} else {
		// Only a current that does not verify is an exhaustion. An explicit
		// repair run against a healthy install still walks the candidates, to
		// restore a missing fallback, but it must not claim an outage.
		report.ExhaustedVersion = a.Current.Version
	}

	arch := opts.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	// current first: it is what the install runs. previous second: restoring it
	// is what leaves a verified fallback behind, so the next lost payload is
	// recovered locally instead of needing the network again. rollbackHold is
	// never a candidate — launcher.Resolve refuses to execute it, so refetching
	// it would put an explicitly rejected artifact back on disk for nothing.
	candidates := []launcher.Descriptor{a.Current}
	if a.Previous != nil && a.Previous.Version != a.Current.Version &&
		(a.RollbackHold == nil || *a.Previous != *a.RollbackHold) {
		candidates = append(candidates, *a.Previous)
	}

	var currentErr error
	for i, d := range candidates {
		if launcher.VerifyPayload(root, d) == nil {
			report.AlreadyVerified = append(report.AlreadyVerified, d.Version)
			if i > 0 {
				report.FallbackVersion = d.Version
			}
			continue
		}
		if err := refetchPayload(ctx, root, d, arch, opts); err != nil {
			if i == 0 {
				currentErr = err
				break
			}
			// A fallback that cannot be refetched does not undo a repaired
			// current. The install runs; it simply has no local spare, which
			// the report and the record both say.
			continue
		}
		report.Restored = append(report.Restored, d.Version)
		if i > 0 {
			report.FallbackVersion = d.Version
		}
	}
	if currentErr != nil {
		return report, fmt.Errorf("update: repair %s from the official release: %w", a.Current.Version, currentErr)
	}
	// Prove the install can actually resolve a payload now. Defender has
	// quarantined a csx payload within seconds of it being written, and a
	// repair that reports success over bytes that are already gone again is the
	// exact silent failure this whole path exists to avoid.
	if err := launcher.VerifyPayload(root, a.Current); err != nil {
		return report, fmt.Errorf("update: repaired payload %s did not stay on disk: %w", a.Current.Version, err)
	}
	// The same question about the fallback, which is the whole reason it was
	// refetched. A fallback that is already gone again is not one, and claiming
	// it would tell the next quarantine to expect a local recovery that cannot
	// happen.
	if report.FallbackVersion != "" && len(candidates) > 1 &&
		launcher.VerifyPayload(root, candidates[1]) != nil {
		report.FallbackVersion = ""
	}
	return report, nil
}

// releaseURLFor resolves where one payload is refetched from. A BaseURL only
// means anything next to an injected HTTP client: production always rebuilds
// the official path and then checks it against the same allowlist the signed
// updater uses, so no local setting can move where a repair downloads from.
func releaseURLFor(opts RehydrateOptions, version, osName, arch string) (string, error) {
	base := opts.BaseURL
	if opts.HTTP == nil {
		base = ""
	}
	raw, err := ReleaseAssetURL(base, version, osName, arch)
	if err != nil {
		return "", err
	}
	if opts.HTTP == nil {
		if err := validateSignedAssetURL(raw, version, osName, arch); err != nil {
			return "", err
		}
	}
	return raw, nil
}

// afterRestoreForTests is the seam for the window this repair cannot close by
// waiting: Defender has taken a csx payload seconds after it was written, and a
// repair must report that as a failure rather than as a success over bytes that
// are already gone.
var afterRestoreForTests = func(root string, d launcher.Descriptor) {}

func refetchPayload(ctx context.Context, root string, d launcher.Descriptor, arch string, opts RehydrateOptions) error {
	raw, err := releaseURLFor(opts, d.Version, runtime.GOOS, arch)
	if err != nil {
		return err
	}
	staged, err := stageRehydratedPayload(ctx, root, raw, d, opts)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	selfTest := opts.SelfTest
	if selfTest == nil {
		selfTest = selfTestBinary
	}
	// The bytes hash correctly; this asks whether they run. A payload the
	// operating system refuses to start is not a repair, and promoting it would
	// replace one unusable payload path with another.
	if err := selfTest(ctx, staged, d.Version); err != nil {
		return err
	}
	if err := launcher.RestorePayload(root, d, staged); err != nil {
		return err
	}
	afterRestoreForTests(root, d)
	return nil
}

func stageRehydratedPayload(ctx context.Context, root, rawURL string, d launcher.Descriptor, opts RehydrateOptions) (string, error) {
	client := opts.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return validateDownloadURL(req.URL)
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", rawURL, resp.StatusCode)
	}
	if resp.ContentLength > maxBinaryBytes {
		return "", errors.New("refetched payload exceeds size limit")
	}
	pattern := ".csx-rehydrate-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	f, err := os.CreateTemp(root, pattern)
	if err != nil {
		return "", fmt.Errorf("stage refetched payload: %w", err)
	}
	name := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	if err := f.Chmod(0o700); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("stage refetched payload: %w", err)
	}
	h := sha256.New()
	// An interrupted transfer ends here as a short read, and a short read can
	// only ever produce a different digest than the one the pointer recorded.
	// There is no separate "was it complete" question to get wrong.
	_, writeErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxBinaryBytes+1))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return "", fmt.Errorf("stage refetched payload: %w", writeErr)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != d.SHA256 {
		return "", fmt.Errorf("refetched %s has SHA-256 %s, want the %s this install recorded", d.Version, got, d.SHA256)
	}
	cleanup = false
	return name, nil
}

// ReleaseAssetURL rebuilds the official download URL for one release target.
// The version and target decide the whole path; nothing on the local machine
// contributes to it beyond the version string the pointer already recorded.
func ReleaseAssetURL(base, version, osName, arch string) (string, error) {
	if !IsCanonicalReleaseVersion(version) {
		return "", fmt.Errorf("update: release version %q is not canonical vMAJOR.MINOR.PATCH", version)
	}
	if osName == "" || arch == "" || strings.ContainsAny(osName+arch, "/\\?#") {
		return "", fmt.Errorf("update: invalid release target %s/%s", osName, arch)
	}
	if base == "" {
		base = DefaultReleaseDownloadBase
	}
	name := "csx-" + osName + "-" + arch
	if osName == "windows" {
		name += ".exe"
	}
	joined := strings.TrimSuffix(base, "/") + "/" + version + "/" + name
	if _, err := url.Parse(joined); err != nil {
		return "", fmt.Errorf("update: invalid release asset URL: %w", err)
	}
	return joined, nil
}
