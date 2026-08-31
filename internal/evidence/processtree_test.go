package evidence

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A command that times out must take its whole tree with it.
//
// exec.CommandContext kills the process it started and nothing below it. Every
// command this network observes is a shell — `npm test`, `go test`,
// `powershell -File` — so the thing doing the work is a grandchild, and on a
// timeout it survived. Reported through report_csx_issue (9, 10): a timed-out
// PowerShell test child kept running after the tool returned, and the caller
// was left with no handle to its eventual exit code.
//
// On a farm slot that means a run which already gave up keeps burning the CPU
// the next assignment needs.
//
// The fixture writes a marker from a grandchild after a delay longer than the
// deadline. Killed tree: the marker never appears. Direct child only: it does.
func TestATimedOutCommandLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-survived")

	argv := grandchildArgv(t, marker)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, _, err := Run(ctx, argv, dir); err != nil && !strings.Contains(err.Error(), "context") {
		// A killed command is not an error this test cares about; a hang is.
		t.Logf("Run returned: %v", err)
	}
	// Run must return near the deadline, not after the grandchild finishes:
	// a surviving grandchild holds the pipes Wait is waiting to see closed.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run blocked for %v past the deadline", elapsed)
	}

	// Long enough that the grandchild would have written, had it survived.
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a grandchild outlived the command that timed out")
	}
}

// grandchildArgv builds a script that spawns a detached second process and
// exits, so only a tree-wide kill can stop the writer.
//
// Scripts on disk rather than inline shell: quoting a nested `start /b cmd /c
// "..."` through Go's argv and cmd.exe's own parser produced something cmd
// could not run at all, so no grandchild was ever spawned and the test passed
// against the bug it exists to catch.
func grandchildArgv(t *testing.T, marker string) []string {
	t.Helper()
	dir := filepath.Dir(marker)
	if runtime.GOOS == "windows" {
		inner := filepath.Join(dir, "inner.cmd")
		outer := filepath.Join(dir, "outer.cmd")
		writeScript(t, inner, "@echo off\r\nping -n 3 127.0.0.1 >nul\r\necho survived > \""+marker+"\"\r\n")
		// The outer script must still be running when the deadline fires —
		// that is the reported situation, a test runner killed mid-run. An
		// earlier version let it exit right after spawning, so the command
		// SUCCEEDED before the deadline and no timeout was exercised at all:
		// Run came back exit 0 at 2s with the marker written, and the test was
		// measuring nothing.
		writeScript(t, outer, "@echo off\r\nstart \"\" /b \""+inner+"\"\r\necho started\r\nping -n 20 127.0.0.1 >nul\r\n")
		return []string{"cmd", "/c", outer}
	}
	script := filepath.Join(dir, "outer.sh")
	writeScript(t, script, "#!/bin/sh\n(sleep 2; echo survived > '"+marker+"') &\necho started\nsleep 20\n")
	return []string{"sh", script}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
