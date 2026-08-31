package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// os.Executable() inside `go test` is the TEST binary, so spawning it as
// `daemon run` re-runs the whole package — and if anything in that run reaches
// the spawn again, so does the child. One `go test ./internal/cli/` produced
// 348 processes this way, kept respawning after its parent was killed, and had
// to be cleared by repeated taskkill passes on the workstation.
//
// The trigger was a one-line change in csx stats from NewClient to
// EnsureRunning. Any future line that reaches EnsureRunning from a test does
// the same, so the refusal lives at the spawn rather than at that call site.
func TestATestBinaryIsNeverSpawnedAsADaemon(t *testing.T) {
	for _, path := range []string{
		`C:\Users\x\AppData\Local\Temp\go-build3415670920\b343\cli.test.exe`,
		"/tmp/go-build123/b001/daemon.test",
		"cli.test.exe",
		"daemon.test",
	} {
		if !isTestBinary(path) {
			t.Errorf("%q would be spawned as a daemon; that is the fork bomb", path)
		}
	}
	for _, path := range []string{
		`C:\Users\x\AppData\Local\csx\csx.exe`,
		"/usr/local/bin/csx",
		`C:\Program Files\contest\csx.exe`,
	} {
		if isTestBinary(path) {
			t.Errorf("%q is a real binary and must still be spawnable", path)
		}
	}
	_ = filepath.Base
}

// And the refusal has to be the spawn's own answer, not a caller's courtesy.
func TestSpawnDetachedRefusesTheTestBinaryItIsRunningIn(t *testing.T) {
	err := spawnDetached(t.TempDir())
	if err == nil {
		t.Fatal("spawnDetached started something from inside a test binary")
	}
	if !strings.Contains(err.Error(), "refusing to spawn a test binary") {
		t.Errorf("error = %v, want the refusal", err)
	}
}
