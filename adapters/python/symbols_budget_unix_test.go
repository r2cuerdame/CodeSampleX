//go:build !windows

package python

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// A named pipe with a .py name is opened by nothing: an open for reading
// blocks until a writer appears, which on a pipe nobody writes is forever,
// and the scan runs after every observed command.
func TestScanSymbolsSkipsNamedPipe(t *testing.T) {
	dir := t.TempDir()
	writePyUse(t, dir, "a.py", "A", 0)
	if err := unix.Mkfifo(filepath.Join(dir, "b_pipe.py"), 0600); err != nil {
		t.Skipf("cannot create a named pipe here: %v", err)
	}
	writePyUse(t, dir, "c.py", "C", 0)

	type result struct {
		got []scanner.SymbolUsage
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, err := scanSymbols(context.Background(), dir, budgetPyLib, clocklessPyBudget())
		done <- result{got, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if !hasPyUse(r.got, "fastapi.A") || !hasPyUse(r.got, "fastapi.C") {
			t.Fatalf("a named pipe must be skipped, not end the scan; got %+v", r.got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scan blocked on a named pipe")
	}
}
