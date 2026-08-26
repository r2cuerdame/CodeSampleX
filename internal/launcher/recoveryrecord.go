package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RecoveryRecordName is the file inside the install root that keeps what the
// launcher's stderr line cannot: that a recovery happened at all.
//
// Recovery is designed to be survivable, and that is exactly the problem this
// file exists for. The launcher notices the current payload is unusable, runs
// the last-known-good one, repairs the pointer, and the command the user asked
// for then succeeds and exits 0 — correctly, because it did run. The single
// stderr line announcing it goes to an MCP host's log that nobody reads, and
// once the pointer is repaired no later run says anything at all. From then on
// the install looks healthy and the operator has no way to find out that a
// released payload was destroyed on this machine.
//
// On Windows the cause so far has always been Microsoft Defender quarantining
// `csx-payload.exe` as a false positive minutes to hours after a verified
// update committed it. Whether the launcher survived it is a different
// question from whether it happened, and only the second one belongs in a
// release decision.
const RecoveryRecordName = "launcher-recovery.json"

// RecoveryRecordSchema is the on-disk version of the record below.
const RecoveryRecordSchema = 1

// RecoveryRecord is the durable evidence of payload recoveries in one install.
//
// It holds the most recent incident rather than a log. A launcher that fails
// to start its payload can be invoked hundreds of times a day by an editor, and
// an unbounded file in the install root would be its own operational problem.
type RecoveryRecord struct {
	Schema int `json:"schema"`

	// FailedVersion and FailedReason describe the payload that could not be
	// used. FailedReason is one of this package's stable reason codes.
	FailedVersion string `json:"failedVersion"`
	FailedReason  string `json:"failedReason"`

	// RanVersion is the last-known-good payload that ran instead.
	RanVersion string `json:"ranVersion"`

	// PointerRepaired reports whether active.json was healed. PointerError
	// says why not when it was not; an install that keeps recovering without
	// ever repairing the pointer is a different fault from one that recovered
	// once and moved on.
	PointerRepaired bool   `json:"pointerRepaired"`
	PointerError    string `json:"pointerError,omitempty"`

	// FirstObservedAt and Observations cover the run of consecutive
	// recoveries from this same failed version and reason; a different
	// failure starts a new incident. Observations is a floor, not a count:
	// several csx processes can start at once and the last write wins.
	FirstObservedAt time.Time `json:"firstObservedAt"`
	LastObservedAt  time.Time `json:"lastObservedAt"`
	Observations    int       `json:"observations"`
}

// Summary renders the record as one operator-facing line.
func (r RecoveryRecord) Summary() string {
	line := fmt.Sprintf("payload %s was unusable (%s); ran last-known-good %s instead",
		r.FailedVersion, r.FailedReason, r.RanVersion)
	if r.Observations > 1 {
		line += fmt.Sprintf("; seen %d times since %s", r.Observations, r.FirstObservedAt.Local().Format("2006-01-02 15:04:05"))
	}
	if !r.PointerRepaired {
		line += "; the active pointer was NOT repaired"
		if r.PointerError != "" {
			line += " (" + r.PointerError + ")"
		}
	}
	return line
}

// RecoveryRecordPath is where the record lives for a given install root.
func RecoveryRecordPath(root string) string { return filepath.Join(root, RecoveryRecordName) }

// RecordRecovery folds one recovery into the install's record.
//
// It is deliberately total: every failure mode returns an error the caller is
// expected to ignore. Losing the evidence is bad; refusing to run csx because
// the evidence could not be written would be worse, and this is called on the
// path where the install is already damaged.
func RecordRecovery(root string, res Resolution, now time.Time) error {
	if !res.Recovered {
		return nil
	}
	next := RecoveryRecord{
		Schema:          RecoveryRecordSchema,
		FailedVersion:   res.FailedVersion,
		FailedReason:    res.FailedReason,
		RanVersion:      res.Descriptor.Version,
		PointerRepaired: res.Healed,
		FirstObservedAt: now.UTC(),
		LastObservedAt:  now.UTC(),
		Observations:    1,
	}
	if res.HealError != nil {
		next.PointerError = res.HealError.Error()
	}
	if prev, ok, err := ReadRecoveryRecord(root); err == nil && ok &&
		prev.FailedVersion == next.FailedVersion && prev.FailedReason == next.FailedReason {
		next.FirstObservedAt = prev.FirstObservedAt
		next.Observations = prev.Observations + 1
	}
	return writeRecoveryRecord(root, next)
}

// ReadRecoveryRecord returns the install's record. The second result is false
// when no recovery has ever been recorded, which is a different fact from a
// record that could not be read.
func ReadRecoveryRecord(root string) (RecoveryRecord, bool, error) {
	raw, err := os.ReadFile(RecoveryRecordPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return RecoveryRecord{}, false, nil
	}
	if err != nil {
		return RecoveryRecord{}, false, err
	}
	var r RecoveryRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return RecoveryRecord{}, false, fmt.Errorf("launcher: parse %s: %w", RecoveryRecordName, err)
	}
	if r.Schema != RecoveryRecordSchema {
		return RecoveryRecord{}, false, fmt.Errorf("launcher: unsupported %s schema %d", RecoveryRecordName, r.Schema)
	}
	if r.FailedVersion == "" || r.FailedReason == "" || r.RanVersion == "" {
		return RecoveryRecord{}, false, fmt.Errorf("launcher: %s is missing the versions it is evidence about", RecoveryRecordName)
	}
	return r, true, nil
}

func writeRecoveryRecord(root string, r RecoveryRecord) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".launcher-recovery-*.json")
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
	// The same replace the active pointer uses, so a recovery record cannot be
	// observed half-written by the csx process that starts a millisecond later.
	if err := replacePointer(tmp, RecoveryRecordPath(root)); err != nil {
		return err
	}
	committed = true
	return nil
}
