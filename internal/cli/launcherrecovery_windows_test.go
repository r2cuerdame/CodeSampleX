//go:build windows

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

// launcherInstallFixture builds the shape resolveLauncherEnvironment insists
// on: the first-party install root under LOCALAPPDATA, a stable csx.exe beside
// it, and an active pointer whose current descriptor is the payload said to be
// running.
func launcherInstallFixture(t *testing.T) (root, payload string) {
	t.Helper()
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	root = filepath.Join(local, "csx")
	payload, err := launcher.PayloadPath(root, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("csx test fixture payload: installed version")
	if err := os.WriteFile(payload, body, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	d := launcher.Descriptor{Version: "v1.0.0", SHA256: hex.EncodeToString(sum[:]), Sequence: 6}
	if err := launcher.Write(root, launcher.Active{Schema: launcher.Schema, Current: d}); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(stable, []byte("csx test fixture launcher"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CSX_LAUNCHER_ROOT", root)
	t.Setenv("CSX_LAUNCHER_PATH", stable)
	t.Setenv("CSX_LAUNCHER_VERSION", launcher.ProtocolVersion)
	t.Setenv("CSX_PAYLOAD_VERSION", d.Version)
	t.Setenv("CSX_ACTIVE_SEQUENCE", "6")
	t.Setenv("CSX_ACTIVE_SHA256", d.SHA256)
	return root, payload
}

// The launcher's recovery succeeds by design: the command exits 0, the pointer
// is repaired, and from the next run on nothing anywhere mentions that a
// released payload was destroyed on this machine. `csx update status` is where
// an operator goes to find out, so it has to say so.
func TestUpdateStatusSurfacesAPayloadRecovery(t *testing.T) {
	root, payload := launcherInstallFixture(t)
	res := launcher.Resolution{
		Descriptor:    launcher.Descriptor{Version: "v0.1.22", SHA256: strings.Repeat("b", 64), Sequence: 4},
		Recovered:     true,
		FailedVersion: "v0.1.44",
		FailedReason:  launcher.ReasonPayloadMissing,
		Healed:        true,
	}
	at := time.Date(2026, 8, 24, 8, 7, 14, 0, time.UTC)
	if err := launcher.RecordRecovery(root, res, at); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	out, _ := captureStdout(t, func() int { reportLauncherRecovery(home, payload); return 0 })
	for _, want := range []string{"payload recovery:", "v0.1.44", launcher.ReasonPayloadMissing, "v0.1.22", "payload recovery last seen:"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output %q is missing %q", out, want)
		}
	}
}

// An install that never lost a payload must say nothing at all. A status
// command that always mentions recovery teaches operators to ignore the line
// that matters.
func TestUpdateStatusSaysNothingWithoutARecovery(t *testing.T) {
	_, payload := launcherInstallFixture(t)
	home := t.TempDir()
	out, _ := captureStdout(t, func() int { reportLauncherRecovery(home, payload); return 0 })
	if strings.TrimSpace(out) != "" {
		t.Fatalf("a healthy install reported %q", out)
	}
}

// A record that cannot be parsed still proves a recovery happened. Staying
// silent would delete the fact along with the detail, so the reason goes to
// stderr instead.
func TestUpdateStatusReportsAnUnreadableRecoveryRecord(t *testing.T) {
	root, payload := launcherInstallFixture(t)
	if err := os.WriteFile(launcher.RecoveryRecordPath(root), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	out, _ := captureStdout(t, func() int { reportLauncherRecovery(home, payload); return 0 })
	if !strings.Contains(out, "payload recovery record is unreadable") {
		t.Fatalf("status output %q hid an unreadable record", out)
	}
}

// A repair leaves an install that looks entirely healthy: the pointer never
// changed, the payload is back, and every later run is ordinary. The one fact
// that matters to a release decision -- that this machine's whole local
// recovery set was destroyed and the payload had to come back over the network
// -- survives only in the record, so status has to read it back.
func TestUpdateStatusSurfacesAPayloadRepair(t *testing.T) {
	root, payload := launcherInstallFixture(t)
	at := time.Date(2026, 8, 29, 15, 43, 40, 0, time.UTC)
	rec := launcher.RehydrateRecord{
		Outcome:          launcher.RehydrateOutcomeRestored,
		ExhaustedVersion: "v0.1.47",
		RestoredVersions: []string{"v0.1.47"},
	}
	if err := launcher.RecordRehydrate(root, rec, at); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	out, _ := captureStdout(t, func() int { reportLauncherRehydrate(home, payload); return 0 })
	for _, want := range []string{"payload repair:", "v0.1.47", "no verified fallback left", "payload repair last attempted:"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output %q is missing %q", out, want)
		}
	}
}

// A failed repair is the more urgent report of the two: the install is still
// down. It must not be reported as anything else.
func TestUpdateStatusSurfacesAFailedPayloadRepair(t *testing.T) {
	root, payload := launcherInstallFixture(t)
	rec := launcher.RehydrateRecord{
		Outcome:          launcher.RehydrateOutcomeFailed,
		ExhaustedVersion: "v0.1.47",
		Error:            "download refused",
	}
	if err := launcher.RecordRehydrate(root, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A second consecutive failure is a different operational state from one.
	if err := launcher.RecordRehydrate(root, rec, time.Now()); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	out, _ := captureStdout(t, func() int { reportLauncherRehydrate(home, payload); return 0 })
	for _, want := range []string{"refetch FAILED", "v0.1.47", "download refused", "2 consecutive attempts"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output %q is missing %q", out, want)
		}
	}
}

// An install that never ran out of payloads says nothing.
func TestUpdateStatusSaysNothingWithoutARepair(t *testing.T) {
	_, payload := launcherInstallFixture(t)
	home := t.TempDir()
	out, _ := captureStdout(t, func() int { reportLauncherRehydrate(home, payload); return 0 })
	if strings.TrimSpace(out) != "" {
		t.Fatalf("an install that never needed a repair reported %q", out)
	}
}
