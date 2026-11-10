package rust

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

var budgetRustLib = []scanner.ResolvedPackage{{PURL: domain.PURL{Ecosystem: "cargo", Name: "serde", Version: "1.0.0"}}}

// clocklessRustBudget is the default budget with the time limit taken out:
// under a full parallel `go test ./...` on Windows, Defender scanning every
// freshly written temp file can spend 250 ms a few files into a walk, so a
// test of anything but the clock must not depend on it.
func clocklessRustBudget() symbolBudget { b := defaultSymbolBudget; b.elapsed = time.Hour; return b }

func rustScanDir(t *testing.T, dir string) string {
	t.Helper()
	root := filepath.Join(dir, "src")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

// writeRustUse writes a file at rel under dir that uses serde::<sym>, padded
// with a comment to exactly size bytes (0 = unpadded) so byte budgets can be
// exercised.
func writeRustUse(t *testing.T, dir, rel, sym string, size int) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	src := "use serde::" + sym + ";\n"
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

func hasRustUse(usages []scanner.SymbolUsage, family string) bool {
	for _, u := range usages {
		if u.Family == family {
			return true
		}
	}
	return false
}

// The public entry point is bounded: a tree one file larger than the default
// file budget is never read to the end. Which limit stops it first depends on
// how loaded the machine is, so only the bound is asserted here; the tests
// below pin what each limit keeps.
func TestScanSymbolsDefaultBudgetBoundsLargeTree(t *testing.T) {
	dir := t.TempDir()
	root := rustScanDir(t, dir)
	n := defaultSymbolBudget.files + 1
	for i := 0; i < n; i++ {
		writeRustUse(t, root, fmt.Sprintf("f%04d.rs", i), fmt.Sprintf("F%04d", i), 0)
	}
	got, err := New().ScanSymbols(context.Background(), dir, budgetRustLib)
	if err != nil {
		t.Fatal(err)
	}
	if last := fmt.Sprintf("serde::F%04d", n-1); hasRustUse(got, last) {
		t.Fatalf("%s recorded: the scan read past its file budget", last)
	}
	if len(got) > defaultSymbolBudget.files {
		t.Fatalf("%d usages from one-usage files, budget is %d files", len(got), defaultSymbolBudget.files)
	}
}

func TestScanSymbolsFileBudgetKeepsEarlierResults(t *testing.T) {
	dir := t.TempDir()
	root := rustScanDir(t, dir)
	for _, sym := range []string{"A", "B", "C"} {
		writeRustUse(t, root, fmt.Sprintf("%s.rs", strings.ToLower(sym)), sym, 0)
	}
	b := clocklessRustBudget()
	b.files = 2
	got, err := scanSymbols(context.Background(), dir, budgetRustLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRustUse(got, "serde::A") || !hasRustUse(got, "serde::B") {
		t.Fatalf("earlier results lost: %+v", got)
	}
	if hasRustUse(got, "serde::C") {
		t.Fatalf("scanned past file budget: %+v", got)
	}
}

// src/ and tests/ are two walks under one budget: the second starts with what
// the first spent, not with a fresh allowance.
func TestScanSymbolsBudgetSpansSrcAndTests(t *testing.T) {
	dir := t.TempDir()
	root := rustScanDir(t, dir)
	writeRustUse(t, root, "a.rs", "A", 0)
	writeRustUse(t, root, "b.rs", "B", 0)
	writeRustUse(t, dir, "tests/c.rs", "C", 0)

	b := clocklessRustBudget()
	b.files = 2
	got, err := scanSymbols(context.Background(), dir, budgetRustLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRustUse(got, "serde::A") || !hasRustUse(got, "serde::B") {
		t.Fatalf("src/ results lost: %+v", got)
	}
	if hasRustUse(got, "serde::C") {
		t.Fatalf("tests/ walked with a fresh budget after src/ spent it: %+v", got)
	}

	b.files = 3
	got, err = scanSymbols(context.Background(), dir, budgetRustLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRustUse(got, "serde::C") {
		t.Fatalf("tests/ not walked while budget remained: %+v", got)
	}
}

func TestScanSymbolsFileAndTotalByteBudgets(t *testing.T) {
	dir := t.TempDir()
	root := rustScanDir(t, dir)
	writeRustUse(t, root, "a.rs", "A", 100)
	writeRustUse(t, root, "b-oversized.rs", "Oversized", 200)
	writeRustUse(t, root, "c.rs", "C", 100)
	writeRustUse(t, root, "d.rs", "D", 100)
	writeRustUse(t, root, "e.rs", "E", 100)
	b := clocklessRustBudget()
	// The oversized file would still fit the total (100+200 <= 350), so only
	// the per-file limit can keep it out. a, c and d then use 300 bytes, and
	// e would bring the total to 400.
	b.fileSize, b.bytes = 150, 350
	got, err := scanSymbols(context.Background(), dir, budgetRustLib, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"serde::A", "serde::C", "serde::D"} {
		if !hasRustUse(got, family) {
			t.Errorf("missing %s after oversized file skipped: %+v", family, got)
		}
	}
	for _, family := range []string{"serde::Oversized", "serde::E"} {
		if hasRustUse(got, family) {
			t.Errorf("unexpected %s beyond byte budget: %+v", family, got)
		}
	}
}

func TestScanSymbolsTimeBudgetKeepsEarlierResults(t *testing.T) {
	dir := t.TempDir()
	root := rustScanDir(t, dir)
	writeRustUse(t, root, "a.rs", "A", 0)
	writeRustUse(t, root, "b.rs", "B", 0)
	b := defaultSymbolBudget
	b.elapsed = 0
	got, err := scanSymbols(context.Background(), dir, budgetRustLib, b)
	if err != nil {
		t.Fatalf("a spent time budget must end the scan cleanly, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("zero time budget scanned files: %+v", got)
	}
}

// A symlinked .rs file is charged for the file the read reaches, not for the
// link's few bytes of target path.
func TestScanSymbolsSymlinkTargetBudget(t *testing.T) {
	outside, dir := t.TempDir(), t.TempDir()
	root := rustScanDir(t, dir)
	writeRustUse(t, outside, "target.rs", "Linked", 200)
	writeRustUse(t, root, "a.rs", "A", 100)
	if err := os.Symlink(filepath.Join(outside, "target.rs"), filepath.Join(root, "b-link.rs")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	writeRustUse(t, root, "c.rs", "C", 100)

	t.Run("per-file", func(t *testing.T) {
		b := clocklessRustBudget()
		b.fileSize = 150
		got, err := scanSymbols(context.Background(), dir, budgetRustLib, b)
		if err != nil {
			t.Fatal(err)
		}
		if hasRustUse(got, "serde::Linked") {
			t.Fatalf("read oversized symlink target: %+v", got)
		}
		if !hasRustUse(got, "serde::A") || !hasRustUse(got, "serde::C") {
			t.Fatalf("skipping target lost valid files: %+v", got)
		}
	})

	t.Run("total", func(t *testing.T) {
		b := clocklessRustBudget()
		// a.rs and the link's 200-byte target would use 300 bytes; a.rs and
		// c.rs fit.
		b.bytes = 250
		got, err := scanSymbols(context.Background(), dir, budgetRustLib, b)
		if err != nil {
			t.Fatal(err)
		}
		if hasRustUse(got, "serde::Linked") {
			t.Fatalf("read symlink target past the total byte budget: %+v", got)
		}
		if !hasRustUse(got, "serde::A") || !hasRustUse(got, "serde::C") {
			t.Fatalf("skipping target lost valid files: %+v", got)
		}
	})

	t.Run("within budget", func(t *testing.T) {
		got, err := scanSymbols(context.Background(), dir, budgetRustLib, clocklessRustBudget())
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range []string{"serde::A", "serde::Linked", "serde::C"} {
			if !hasRustUse(got, family) {
				t.Errorf("missing %s: a link whose target fits the budget is still followed; got %+v", family, got)
			}
		}
	})
}

// A .rs name that resolves to a directory is not a source file; it is
// skipped without ending the scan.
func TestScanSymbolsSkipsDirectoryLinkTarget(t *testing.T) {
	outside, dir := t.TempDir(), t.TempDir()
	root := rustScanDir(t, dir)
	writeRustUse(t, outside, "inner/x.rs", "Inner", 0)
	writeRustUse(t, root, "a.rs", "A", 0)
	if err := os.Symlink(filepath.Join(outside, "inner"), filepath.Join(root, "b-dir.rs")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	writeRustUse(t, root, "c.rs", "C", 0)

	b := clocklessRustBudget()
	b.files = 2
	got, err := scanSymbols(context.Background(), dir, budgetRustLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRustUse(got, "serde::A") || !hasRustUse(got, "serde::C") {
		t.Fatalf("a directory link was charged as a file or ended the scan: %+v", got)
	}
	if hasRustUse(got, "serde::Inner") {
		t.Fatalf("a directory link was walked: %+v", got)
	}
}

// cancelAfter reports nil from its first n Err calls and Canceled after
// that, so a walk can be cancelled part-way through without a hook into it.
type cancelAfter struct {
	context.Context
	n int
}

func (c *cancelAfter) Err() error {
	if c.n <= 0 {
		return context.Canceled
	}
	c.n--
	return nil
}

func TestScanSymbolsCancelledKeepsEarlierResults(t *testing.T) {
	dir := t.TempDir()
	root := rustScanDir(t, dir)
	writeRustUse(t, root, "a.rs", "A", 0)
	for i := 0; i < 8; i++ {
		writeRustUse(t, root, fmt.Sprintf("m%d.rs", i), fmt.Sprintf("M%d", i), 0)
	}
	writeRustUse(t, root, "z.rs", "Z", 0)

	// Room for the src directory and a.rs however often a walk step asks,
	// and far too little to reach z.rs.
	ctx := &cancelAfter{Context: context.Background(), n: 4}
	got, err := scanSymbols(ctx, dir, budgetRustLib, clocklessRustBudget())
	if err != nil {
		t.Fatalf("cancellation must end the scan cleanly, got %v", err)
	}
	if !hasRustUse(got, "serde::A") {
		t.Errorf("usage read before cancellation was dropped; got %+v", got)
	}
	if hasRustUse(got, "serde::Z") {
		t.Errorf("serde::Z recorded: the walk kept reading after cancellation; got %+v", got)
	}
}
