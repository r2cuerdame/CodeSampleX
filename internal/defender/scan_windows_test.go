//go:build windows

package defender

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The whole package is worth nothing if asking the question destroys the
// answer: a fixture guard that quarantines the fixture it measures turns one
// clear failure into a different, confusing one on the next run.
func TestScanDoesNotRemediateWhatItMeasures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.bin")
	body := []byte("csx defender scan fixture, deliberately unremarkable")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	verdicts, err := Scan(context.Background(), path)
	if errors.Is(err, ErrUnavailable) {
		t.Skip("Microsoft Defender command-line scanner is not installed on this machine")
	}
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(verdicts) != 1 {
		t.Fatalf("Scan returned %d verdicts, want 1", len(verdicts))
	}
	if verdicts[0].Flagged {
		t.Fatalf("Defender flagged an inert fixture: %s", verdicts[0])
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the scan removed its own subject: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("the scan rewrote its own subject: %q", got)
	}
}

// MpCmdRun answers a missing file with the same exit code it uses for "threats
// found". Reading that as a detection would report every typo as malware, so
// the target is checked before the scanner is asked.
func TestScanRefusesAMissingTargetInsteadOfCallingItAThreat(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-written.bin")
	verdicts, err := Scan(context.Background(), missing)
	if errors.Is(err, ErrUnavailable) {
		t.Skip("Microsoft Defender command-line scanner is not installed on this machine")
	}
	if err == nil {
		t.Fatalf("a missing target produced verdicts %v instead of an error", verdicts)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want it to name the missing file", err)
	}
}

// An operator who pins the scanner to something that is not there must be told
// so. Silently falling back would report a verdict from a build they did not
// choose.
func TestPinnedScannerThatIsNotThereIsUnavailableNotIgnored(t *testing.T) {
	t.Setenv("CSX_DEFENDER_MPCMDRUN", filepath.Join(t.TempDir(), "no-such-MpCmdRun.exe"))
	if _, err := Scan(context.Background(), filepath.Join(t.TempDir(), "anything")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
