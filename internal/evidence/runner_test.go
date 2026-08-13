package evidence

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"

	nodeadapter "github.com/r2cuerdame/codesamplex/adapters/node"
)

func hasNode() bool {
	_, err := exec.LookPath("node")
	return err == nil
}

// passCmd returns a command that exits 0: node when available, otherwise
// a shell builtin.
func passCmd() []string {
	if hasNode() {
		return []string{"node", "--version"}
	}
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "exit", "0"}
	}
	return []string{"sh", "-c", "exit 0"}
}

func TestRunPassthroughAndLocalLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	argv := passCmd()
	code, _, err := Run(context.Background(), argv, t.TempDir())
	if err != nil {
		t.Fatalf("Run(%v): %v", argv, err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(home, "logs", "last-run.log")); err != nil {
		t.Fatalf("last-run.log not written: %v", err)
	}

	// The fallback command is one no adapter classifies (contract C14:
	// unknown commands record USED only).
	res := &scanner.ScanResult{Adapters: []scanner.Adapter{nodeadapter.Adapter{}}}
	if p := res.Classify([]string{"cmd", "/c", "exit", "0"}); p.Known {
		t.Fatalf("cmd /c exit 0 classified as %v, want unknown", p)
	}
	if hasNode() {
		if p := res.Classify(argv); !p.Known {
			t.Fatalf("node command not classified: %v", p)
		}
	}
}

func TestRunExitCodePassthrough(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	var argv []string
	switch {
	case hasNode():
		argv = []string{"node", "-e", "process.exit(3)"}
	case runtime.GOOS == "windows":
		argv = []string{"cmd", "/c", "exit", "3"}
	default:
		argv = []string{"sh", "-c", "exit 3"}
	}
	code, _, err := Run(context.Background(), argv, t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

func TestRunCapturesStderrTail(t *testing.T) {
	if !hasNode() {
		t.Skip("node not installed")
	}
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	argv := []string{"node", "-e", `console.error("boom-line-1"); console.error("boom-line-2"); process.exit(2)`}
	code, tail, err := Run(context.Background(), argv, t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(tail, "boom-line-1") || !strings.Contains(tail, "boom-line-2") {
		t.Fatalf("stderr tail missing lines: %q", tail)
	}
	raw, err := os.ReadFile(filepath.Join(home, "logs", "last-run.log"))
	if err != nil {
		t.Fatalf("read last-run.log: %v", err)
	}
	if !strings.Contains(string(raw), "boom-line-1") {
		t.Fatalf("raw log missing stderr: %q", raw)
	}
}

func TestRunCommandNotFound(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	_, _, err := Run(context.Background(), []string{"definitely-not-a-real-command-xyz"}, t.TempDir())
	if err == nil {
		t.Fatal("Run of a missing command returned nil error")
	}
}

func TestLineRingBounds(t *testing.T) {
	r := newLineRing(200)
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(r, "line-%d\n", i)
	}
	lines := strings.Split(r.Tail(), "\n")
	if len(lines) != 200 {
		t.Fatalf("ring holds %d lines, want 200", len(lines))
	}
	if lines[0] != "line-51" || lines[199] != "line-250" {
		t.Fatalf("ring window wrong: first=%q last=%q", lines[0], lines[199])
	}

	// Partial final line is included.
	r2 := newLineRing(3)
	r2.Write([]byte("a\nb\npartial"))
	if got := r2.Tail(); got != "a\nb\npartial" {
		t.Fatalf("Tail() = %q", got)
	}
}
