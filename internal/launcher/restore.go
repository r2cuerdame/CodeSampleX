package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// VerifyPayload reports whether the payload a descriptor names is present on
// disk and hashes to what the descriptor recorded. It is the same check the
// launcher makes before executing anything, exported so a repair can ask "is
// this one already fine?" without duplicating the rule.
func VerifyPayload(root string, d Descriptor) error { return validateDescriptor(root, d) }

// RestorePayload puts bytes back at a descriptor's immutable payload path.
//
// This exists for one situation: the payload file a still-correct pointer
// names is gone, and so is every fallback the pointer records, so nothing can
// run and nothing on this machine can repair it. The bytes then have to come
// from outside — see internal/update.RehydrateInstall, which re-fetches them
// from the official release path — and this is where they re-enter the install.
//
// The descriptor's own SHA-256 is the whole trust boundary. It was recorded by
// this install from a signed manifest at commit time, and staged bytes that do
// not hash to it never reach the payload path. A caller cannot use this to
// adopt a file it merely found: it must already know the exact digest, and
// this refuses everything else. After the move the payload is verified again
// from disk, so a file that is quarantined or truncated between the hash check
// and the rename is reported as a failed restore rather than a repair.
func RestorePayload(root string, d Descriptor, staged string) error {
	if err := validateDescriptorShape(d); err != nil {
		return err
	}
	final, err := PayloadPath(root, d.Version)
	if err != nil {
		return err
	}
	sum, err := fileHash(staged)
	if err != nil {
		return err
	}
	if sum != d.SHA256 {
		return fmt.Errorf("launcher: restore payload %s: %w", d.Version, errPayloadCorrupt)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	// Prove the directory chain belongs to this install before writing into it.
	// MkdirAll happily follows a reparse point someone else planted, and the
	// post-write verification would then be verifying a file outside the root.
	if err := validateContainedChain(root, filepath.Dir(final)); err != nil {
		return err
	}
	// Replace rather than refuse an existing file. The shape this repairs is a
	// payloads/<version>/ directory holding either nothing or a corrupt
	// remnant; the version path is immutable in the sense that one version has
	// one digest, and that digest was checked above.
	if err := replacePointer(staged, final); err != nil {
		return err
	}
	if err := validateDescriptor(root, d); err != nil {
		return fmt.Errorf("launcher: restored payload %s did not survive verification: %w", d.Version, err)
	}
	return nil
}

// RehydrateRecordName is the durable evidence that this install had to refetch
// a payload from the release it was originally installed from.
//
// It is a separate fact from RecoveryRecordName and deserves its own file. A
// recovery record says the launcher fell back to a payload that was still on
// this machine; this one says there was nothing left to fall back to. The
// second is a far stronger signal about a release — the local recovery set was
// exhausted — and it must not be overwritten by the ordinary recoveries that
// follow a successful repair.
const RehydrateRecordName = "launcher-rehydrate.json"

// RehydrateRecordSchema is the on-disk version of the record below.
const RehydrateRecordSchema = 1

// RehydrateRecord is the most recent repair attempt in one install.
type RehydrateRecord struct {
	Schema int `json:"schema"`

	// AttemptedAt is when the attempt finished. Outcome is "restored" or
	// "failed"; Error carries why a failed one failed.
	AttemptedAt time.Time `json:"attemptedAt"`
	Outcome     string    `json:"outcome"`
	Error       string    `json:"error,omitempty"`

	// ExhaustedVersion is the current payload that had no fallback left, and
	// RestoredVersions are the versions whose bytes came back.
	ExhaustedVersion string   `json:"exhaustedVersion,omitempty"`
	RestoredVersions []string `json:"restoredVersions,omitempty"`

	// Attempts counts consecutive attempts since the last successful one.
	Attempts int `json:"attempts"`
}

// RehydrateOutcomeRestored and RehydrateOutcomeFailed are the only two values
// Outcome takes.
const (
	RehydrateOutcomeRestored = "restored"
	RehydrateOutcomeFailed   = "failed"
)

// Summary renders the record as one operator-facing line.
func (r RehydrateRecord) Summary() string {
	if r.Outcome == RehydrateOutcomeRestored {
		line := "refetched the payload from the official release"
		if len(r.RestoredVersions) > 0 {
			line += " (" + strings.Join(r.RestoredVersions, ", ") + ")"
		}
		if r.ExhaustedVersion != "" {
			line += fmt.Sprintf("; %s had no verified fallback left on this machine", r.ExhaustedVersion)
		}
		return line
	}
	line := "payload refetch FAILED"
	if r.ExhaustedVersion != "" {
		line += fmt.Sprintf(" for %s", r.ExhaustedVersion)
	}
	if r.Error != "" {
		line += ": " + r.Error
	}
	if r.Attempts > 1 {
		line += fmt.Sprintf("; %d consecutive attempts", r.Attempts)
	}
	return line
}

// RehydrateRecordPath is where the record lives for a given install root.
func RehydrateRecordPath(root string) string { return filepath.Join(root, RehydrateRecordName) }

// ReadRehydrateRecord returns the install's repair record. The second result is
// false when no repair has ever been attempted, which is a different fact from
// a record that could not be read.
func ReadRehydrateRecord(root string) (RehydrateRecord, bool, error) {
	raw, err := os.ReadFile(RehydrateRecordPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return RehydrateRecord{}, false, nil
	}
	if err != nil {
		return RehydrateRecord{}, false, err
	}
	var r RehydrateRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return RehydrateRecord{}, false, fmt.Errorf("launcher: parse %s: %w", RehydrateRecordName, err)
	}
	if r.Schema != RehydrateRecordSchema {
		return RehydrateRecord{}, false, fmt.Errorf("launcher: unsupported %s schema %d", RehydrateRecordName, r.Schema)
	}
	if r.Outcome != RehydrateOutcomeRestored && r.Outcome != RehydrateOutcomeFailed {
		return RehydrateRecord{}, false, fmt.Errorf("launcher: %s has no outcome", RehydrateRecordName)
	}
	return r, true, nil
}

// RecordRehydrate folds one repair attempt into the install's record. Like
// RecordRecovery it is total: the caller is on the damaged path already and
// must not be stopped by an evidence file it could not write.
func RecordRehydrate(root string, r RehydrateRecord, now time.Time) error {
	r.Schema = RehydrateRecordSchema
	r.AttemptedAt = now.UTC()
	r.Attempts = 1
	if r.Outcome == RehydrateOutcomeFailed {
		if prev, ok, err := ReadRehydrateRecord(root); err == nil && ok && prev.Outcome == RehydrateOutcomeFailed {
			r.Attempts = prev.Attempts + 1
		}
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".launcher-rehydrate-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
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
	if err := replacePointer(tmp, RehydrateRecordPath(root)); err != nil {
		return err
	}
	committed = true
	return nil
}
