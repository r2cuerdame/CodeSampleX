//go:build !windows

package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// A named pipe with a .js name is opened by nothing: an open for reading
// blocks until a writer appears, which on a pipe nobody writes is forever,
// and the scan runs after every observed command.
func TestScanSymbolsSkipsNamedPipe(t *testing.T) {
	dir := t.TempDir()
	writeNodeUse(t, dir, "a.js", "A", 0)
	if err := unix.Mkfifo(filepath.Join(dir, "b-pipe.js"), 0600); err != nil {
		t.Skipf("cannot create a named pipe here: %v", err)
	}
	writeNodeUse(t, dir, "c.js", "C", 0)

	type result struct {
		got []scanner.SymbolUsage
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, err := scanSymbols(context.Background(), dir, budgetNodeLib, clocklessNodeBudget())
		done <- result{got, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if !hasNodeUse(r.got, "axios.A") || !hasNodeUse(r.got, "axios.C") {
			t.Fatalf("a named pipe must be skipped, not end the scan; got %+v", r.got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scan blocked on a named pipe")
	}
}
