package python

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

var budgetPyLib = []scanner.ResolvedPackage{{PURL: domain.PURL{Ecosystem: "pypi", Name: "fastapi", Version: "0.115.0"}}}

// clocklessPyBudget is the default budget with the time limit taken out:
// under a full parallel `go test ./...` on Windows, Defender scanning every
// freshly written temp file can spend 250 ms a few files into a walk, so a
// test of anything but the clock must not depend on it.
func clocklessPyBudget() symbolBudget { b := defaultSymbolBudget; b.elapsed = time.Hour; return b }

// writePyUse writes a file at rel under dir that imports fastapi.<sym>, padded
// with a comment to exactly size bytes (0 = unpadded) so byte budgets can be
// exercised.
func writePyUse(t *testing.T, dir, rel, sym string, size int) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	src := "from fastapi import " + sym + "\n"
	if size > 0 {
		if size < len(src)+2 {
			t.Fatalf("size %d too small", size)
		}
		src += "#" + strings.Repeat("x", size-len(src)-2) + "\n"
	}
	if err := os.WriteFile(p, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
}

func hasPyUse(usages []scanner.SymbolUsage, family string) bool {
	for _, u := range usages {
		if u.Family == family {
			return true
		}
	}
	return false
}

func TestScanSymbolsFileBudgetKeepsEarlierResults(t *testing.T) {
	dir := t.TempDir()
	for _, sym := range []string{"A", "B", "C"} {
		writePyUse(t, dir, strings.ToLower(sym)+".py", sym, 0)
	}
	b := clocklessPyBudget()
	b.files = 2
	got, err := scanSymbols(context.Background(), dir, budgetPyLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPyUse(got, "fastapi.A") || !hasPyUse(got, "fastapi.B") {
		t.Fatalf("earlier results lost: %+v", got)
	}
	if hasPyUse(got, "fastapi.C") {
		t.Fatalf("scanned past file budget: %+v", got)
	}
}

func TestScanSymbolsFileAndTotalByteBudgets(t *testing.T) {
	dir := t.TempDir()
	writePyUse(t, dir, "a.py", "A", 100)
	writePyUse(t, dir, "b_oversized.py", "Oversized", 200)
	writePyUse(t, dir, "c.py", "C", 100)
	writePyUse(t, dir, "d.py", "D", 100)
	writePyUse(t, dir, "e.py", "E", 100)
	b := clocklessPyBudget()
	// The oversized file would still fit the total (100+200 <= 350), so only
	// the per-file limit can keep it out. a, c and d then use 300 bytes, and
	// e would bring the total to 400.
	b.fileSize, b.bytes = 150, 350
	got, err := scanSymbols(context.Background(), dir, budgetPyLib, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"fastapi.A", "fastapi.C", "fastapi.D"} {
		if !hasPyUse(got, family) {
			t.Errorf("missing %s after oversized file skipped: %+v", family, got)
		}
	}
	for _, family := range []string{"fastapi.Oversized", "fastapi.E"} {
		if hasPyUse(got, family) {
			t.Errorf("unexpected %s beyond byte budget: %+v", family, got)
		}
	}
}

// The read is bounded on its own, so a file that grows after the stat is
// refused rather than admitted on the size the stat saw.
func TestReadAtMostRefusesFileOverLimit(t *testing.T) {
	dir := t.TempDir()
	writePyUse(t, dir, "a.py", "A", 100)
	p := filepath.Join(dir, "a.py")
	if _, ok := readAtMost(p, 99); ok {
		t.Fatal("read a 100-byte file under a 99-byte limit")
	}
	data, ok := readAtMost(p, 100)
	if !ok || len(data) != 100 {
		t.Fatalf("readAtMost at the limit = %d bytes, ok %v; want 100, true", len(data), ok)
	}
}

// stepClock reads t for its first n calls and an hour later after that, so a
// time budget runs out part-way through a walk without a real wait.
type stepClock struct {
	t time.Time
	n int
}

func (c *stepClock) now() time.Time {
	if c.n <= 0 {
		return c.t.Add(time.Hour)
	}
	c.n--
	return c.t
}

func TestScanSymbolsTimeBudgetKeepsEarlierResults(t *testing.T) {
	dir := t.TempDir()
	for _, sym := range []string{"A", "B", "C"} {
		writePyUse(t, dir, strings.ToLower(sym)+".py", sym, 0)
	}

	t.Run("spent part-way", func(t *testing.T) {
		// One reading for the start, one for the root directory and one for
		// a.py; the clock has moved on by the time the walk reaches b.py.
		b := defaultSymbolBudget
		b.now = (&stepClock{t: time.Now(), n: 3}).now
		got, err := scanSymbols(context.Background(), dir, budgetPyLib, b)
		if err != nil {
			t.Fatalf("a spent time budget must end the scan cleanly, got %v", err)
		}
		if !hasPyUse(got, "fastapi.A") {
			t.Fatalf("usage read before the time budget ran out was dropped: %+v", got)
		}
		if hasPyUse(got, "fastapi.B") || hasPyUse(got, "fastapi.C") {
			t.Fatalf("scanned past the time budget: %+v", got)
		}
	})

	t.Run("spent before the walk", func(t *testing.T) {
		b := defaultSymbolBudget
		b.elapsed = 0
		got, err := scanSymbols(context.Background(), dir, budgetPyLib, b)
		if err != nil {
			t.Fatalf("a spent time budget must end the scan cleanly, got %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("zero time budget scanned files: %+v", got)
		}
	})
}

// A symlinked .py file is charged for the file the read reaches, not for the
// link's few bytes of target path.
func TestScanSymbolsSymlinkTargetBudget(t *testing.T) {
	outside, dir := t.TempDir(), t.TempDir()
	writePyUse(t, outside, "target.py", "Linked", 200)
	writePyUse(t, dir, "a.py", "A", 100)
	if err := os.Symlink(filepath.Join(outside, "target.py"), filepath.Join(dir, "b_link.py")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	writePyUse(t, dir, "c.py", "C", 100)

	t.Run("per-file", func(t *testing.T) {
		b := clocklessPyBudget()
		b.fileSize = 150
		got, err := scanSymbols(context.Background(), dir, budgetPyLib, b)
		if err != nil {
			t.Fatal(err)
		}
		if hasPyUse(got, "fastapi.Linked") {
			t.Fatalf("read oversized symlink target: %+v", got)
		}
		if !hasPyUse(got, "fastapi.A") || !hasPyUse(got, "fastapi.C") {
			t.Fatalf("skipping target lost valid files: %+v", got)
		}
	})

	t.Run("total", func(t *testing.T) {
		b := clocklessPyBudget()
		// a.py and the link's 200-byte target would use 300 bytes; a.py and
		// c.py fit.
		b.bytes = 250
		got, err := scanSymbols(context.Background(), dir, budgetPyLib, b)
		if err != nil {
			t.Fatal(err)
		}
		if hasPyUse(got, "fastapi.Linked") {
			t.Fatalf("read symlink target past the total byte budget: %+v", got)
		}
		if !hasPyUse(got, "fastapi.A") || !hasPyUse(got, "fastapi.C") {
			t.Fatalf("skipping target lost valid files: %+v", got)
		}
	})

	t.Run("within budget", func(t *testing.T) {
		got, err := scanSymbols(context.Background(), dir, budgetPyLib, clocklessPyBudget())
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range []string{"fastapi.A", "fastapi.Linked", "fastapi.C"} {
			if !hasPyUse(got, family) {
				t.Errorf("missing %s: a link whose target fits the budget is still followed; got %+v", family, got)
			}
		}
	})
}

// A .py name that resolves to a directory is not a source file; it is skipped
// without ending the scan or being charged as a file.
func TestScanSymbolsSkipsDirectoryLinkTarget(t *testing.T) {
	outside, dir := t.TempDir(), t.TempDir()
	writePyUse(t, outside, "inner/x.py", "Inner", 0)
	writePyUse(t, dir, "a.py", "A", 0)
	if err := os.Symlink(filepath.Join(outside, "inner"), filepath.Join(dir, "b_dir.py")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	writePyUse(t, dir, "c.py", "C", 0)

	b := clocklessPyBudget()
	b.files = 2
	got, err := scanSymbols(context.Background(), dir, budgetPyLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPyUse(got, "fastapi.A") || !hasPyUse(got, "fastapi.C") {
		t.Fatalf("a directory link was charged as a file or ended the scan: %+v", got)
	}
	if hasPyUse(got, "fastapi.Inner") {
		t.Fatalf("a directory link was walked: %+v", got)
	}
}
