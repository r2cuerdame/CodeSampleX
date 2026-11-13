package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

var budgetNodeLib = []scanner.ResolvedPackage{{PURL: domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.0.0"}}}

// clocklessNodeBudget is the default budget with the time limit taken out:
// under a full parallel `go test ./...` on Windows, Defender scanning every
// freshly written temp file can spend 250 ms a few files into a walk, so a
// test of anything but the clock must not depend on it.
func clocklessNodeBudget() symbolBudget { b := defaultSymbolBudget; b.elapsed = time.Hour; return b }

// writeNodeUse writes a file at rel under dir that imports and calls
// axios's <sym>, padded with a comment to exactly size bytes (0 = unpadded)
// so byte budgets can be exercised.
func writeNodeUse(t *testing.T, dir, rel, sym string, size int) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	src := "import { " + sym + " } from 'axios';\n" + sym + "();\n"
	if size > 0 {
		if size < len(src)+3 {
			t.Fatalf("size %d too small", size)
		}
		src += "//" + strings.Repeat("x", size-len(src)-3) + "\n"
	}
	if err := os.WriteFile(p, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
}

func hasNodeUse(uses []scanner.SymbolUsage, family string) bool {
	_, ok := findUse(uses, "axios", family)
	return ok
}

func TestDefaultSymbolBudget(t *testing.T) {
	want := symbolBudget{files: 64, fileSize: 64 << 10, bytes: 512 << 10, elapsed: 250 * time.Millisecond}
	if got := defaultSymbolBudget; got.files != want.files || got.fileSize != want.fileSize || got.bytes != want.bytes || got.elapsed != want.elapsed {
		t.Fatalf("defaultSymbolBudget = %+v, want %+v", got, want)
	}
}

// The public entry point is bounded: a tree one file larger than the default
// file budget is never read to the end. Which limit stops it first depends on
// how loaded the machine is, so only the bound is asserted here; the tests
// below pin what each limit keeps.
func TestScanSymbolsDefaultBudgetBoundsLargeTree(t *testing.T) {
	dir := t.TempDir()
	n := defaultSymbolBudget.files + 1
	for i := 0; i < n; i++ {
		writeNodeUse(t, dir, fmt.Sprintf("f%04d.js", i), fmt.Sprintf("F%04d", i), 0)
	}
	got, err := (Adapter{}).ScanSymbols(context.Background(), dir, budgetNodeLib)
	if err != nil {
		t.Fatal(err)
	}
	if last := fmt.Sprintf("axios.F%04d", n-1); hasNodeUse(got, last) {
		t.Fatalf("%s recorded: the scan read past its file budget", last)
	}
	if len(got) > defaultSymbolBudget.files {
		t.Fatalf("%d usages from one-usage files, budget is %d files", len(got), defaultSymbolBudget.files)
	}
}

func TestScanSymbolsFileBudgetKeepsEarlierResults(t *testing.T) {
	dir := t.TempDir()
	for _, sym := range []string{"A", "B", "C"} {
		writeNodeUse(t, dir, strings.ToLower(sym)+".js", sym, 0)
	}
	b := clocklessNodeBudget()
	b.files = 2
	got, err := scanSymbols(context.Background(), dir, budgetNodeLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hasNodeUse(got, "axios.A") || !hasNodeUse(got, "axios.B") {
		t.Fatalf("earlier results lost: %+v", got)
	}
	if hasNodeUse(got, "axios.C") {
		t.Fatalf("scanned past file budget: %+v", got)
	}
}

func TestScanSymbolsFileAndTotalByteBudgets(t *testing.T) {
	dir := t.TempDir()
	writeNodeUse(t, dir, "a.js", "A", 100)
	writeNodeUse(t, dir, "b-oversized.js", "Oversized", 200)
	writeNodeUse(t, dir, "c.js", "C", 100)
	writeNodeUse(t, dir, "d.js", "D", 100)
	writeNodeUse(t, dir, "e.js", "E", 100)
	b := clocklessNodeBudget()
	// The oversized file would still fit the total (100+200 <= 350), so only
	// the per-file limit can keep it out. a, c and d then use 300 bytes, and
	// e would bring the total to 400.
	b.fileSize, b.bytes = 150, 350
	got, err := scanSymbols(context.Background(), dir, budgetNodeLib, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"axios.A", "axios.C", "axios.D"} {
		if !hasNodeUse(got, family) {
			t.Errorf("missing %s after oversized file skipped: %+v", family, got)
		}
	}
	for _, family := range []string{"axios.Oversized", "axios.E"} {
		if hasNodeUse(got, family) {
			t.Errorf("unexpected %s beyond byte budget: %+v", family, got)
		}
	}
}

// The read is bounded on its own, so a file that grows after the stat is
// refused rather than admitted on the size the stat saw.
func TestReadAtMostRefusesFileOverLimit(t *testing.T) {
	dir := t.TempDir()
	writeNodeUse(t, dir, "a.js", "A", 100)
	p := filepath.Join(dir, "a.js")
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
		writeNodeUse(t, dir, strings.ToLower(sym)+".js", sym, 0)
	}

	t.Run("spent part-way", func(t *testing.T) {
		// One reading for the start, one for the root directory and one for
		// a.js; the clock has moved on by the time the walk reaches b.js.
		b := defaultSymbolBudget
		b.now = (&stepClock{t: time.Now(), n: 3}).now
		got, err := scanSymbols(context.Background(), dir, budgetNodeLib, b)
		if err != nil {
			t.Fatalf("a spent time budget must end the scan cleanly, got %v", err)
		}
		if !hasNodeUse(got, "axios.A") {
			t.Fatalf("usage read before the time budget ran out was dropped: %+v", got)
		}
		if hasNodeUse(got, "axios.B") || hasNodeUse(got, "axios.C") {
			t.Fatalf("scanned past the time budget: %+v", got)
		}
	})

	t.Run("spent before the walk", func(t *testing.T) {
		b := defaultSymbolBudget
		b.elapsed = 0
		got, err := scanSymbols(context.Background(), dir, budgetNodeLib, b)
		if err != nil {
			t.Fatalf("a spent time budget must end the scan cleanly, got %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("zero time budget scanned files: %+v", got)
		}
	})
}

// A symlinked .js file is charged for the file the read reaches, not for the
// link's few bytes of target path.
func TestScanSymbolsSymlinkTargetBudget(t *testing.T) {
	outside, dir := t.TempDir(), t.TempDir()
	writeNodeUse(t, outside, "target.js", "Linked", 200)
	writeNodeUse(t, dir, "a.js", "A", 100)
	if err := os.Symlink(filepath.Join(outside, "target.js"), filepath.Join(dir, "b-link.js")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	writeNodeUse(t, dir, "c.js", "C", 100)

	t.Run("per-file", func(t *testing.T) {
		b := clocklessNodeBudget()
		b.fileSize = 150
		got, err := scanSymbols(context.Background(), dir, budgetNodeLib, b)
		if err != nil {
			t.Fatal(err)
		}
		if hasNodeUse(got, "axios.Linked") {
			t.Fatalf("read oversized symlink target: %+v", got)
		}
		if !hasNodeUse(got, "axios.A") || !hasNodeUse(got, "axios.C") {
			t.Fatalf("skipping target lost valid files: %+v", got)
		}
	})

	t.Run("total", func(t *testing.T) {
		b := clocklessNodeBudget()
		// a.js and the link's 200-byte target would use 300 bytes; a.js and
		// c.js fit.
		b.bytes = 250
		got, err := scanSymbols(context.Background(), dir, budgetNodeLib, b)
		if err != nil {
			t.Fatal(err)
		}
		if hasNodeUse(got, "axios.Linked") {
			t.Fatalf("read symlink target past the total byte budget: %+v", got)
		}
		if !hasNodeUse(got, "axios.A") || !hasNodeUse(got, "axios.C") {
			t.Fatalf("skipping target lost valid files: %+v", got)
		}
	})

	t.Run("within budget", func(t *testing.T) {
		got, err := scanSymbols(context.Background(), dir, budgetNodeLib, clocklessNodeBudget())
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range []string{"axios.A", "axios.Linked", "axios.C"} {
			if !hasNodeUse(got, family) {
				t.Errorf("missing %s: a link whose target fits the budget is still followed; got %+v", family, got)
			}
		}
	})
}

// A .js name that resolves to a directory is not a source file; it is skipped
// without ending the scan or being charged as a file.
func TestScanSymbolsSkipsDirectoryLinkTarget(t *testing.T) {
	outside, dir := t.TempDir(), t.TempDir()
	writeNodeUse(t, outside, "inner/x.js", "Inner", 0)
	writeNodeUse(t, dir, "a.js", "A", 0)
	if err := os.Symlink(filepath.Join(outside, "inner"), filepath.Join(dir, "b-dir.js")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	writeNodeUse(t, dir, "c.js", "C", 0)

	b := clocklessNodeBudget()
	b.files = 2
	got, err := scanSymbols(context.Background(), dir, budgetNodeLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hasNodeUse(got, "axios.A") || !hasNodeUse(got, "axios.C") {
		t.Fatalf("a directory link was charged as a file or ended the scan: %+v", got)
	}
	if hasNodeUse(got, "axios.Inner") {
		t.Fatalf("a directory link was walked: %+v", got)
	}
}
