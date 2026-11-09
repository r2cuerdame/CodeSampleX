package goadapter

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

var budgetLib = []scanner.ResolvedPackage{{PURL: domain.PURL{Ecosystem: "golang", Name: "example.com/lib", Version: "v1.0.0"}}}

// clocklessBudget is the default budget with the time limit taken out. The
// limit is wall clock, and under a full parallel `go test ./...` on Windows
// (Defender scanning every freshly written temp file) 250 ms ran out two
// files into a seven-file walk, so a test of anything but the clock must not
// depend on it.
func clocklessBudget() symbolBudget {
	b := defaultSymbolBudget
	b.elapsed = time.Hour
	return b
}

// writeGoUse writes a file at rel under dir that selects lib.<sym>, padded
// with a comment to exactly pad bytes (0 = unpadded) so byte budgets can be
// exercised.
func writeGoUse(t *testing.T, dir, rel, sym string, pad int) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	src := "package p\n\nimport \"example.com/lib\"\n\nvar _ = lib." + sym + "\n"
	if n := pad - len(src); n >= 3 {
		src += "//" + strings.Repeat("x", n-3) + "\n"
	} else if pad > 0 {
		t.Fatalf("pad %d is too small for %s", pad, rel)
	}
	if err := os.WriteFile(p, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
}

func hasLibUse(usages []scanner.SymbolUsage, sym string) bool {
	_, ok := findUsage(usages, "example.com/lib."+sym)
	return ok
}

// Reproduces #632: a dependency tree checked out beside Go source (a JS
// front end's node_modules, a goreleaser dist/) was walked and parsed on
// every observed command. Below the root, build and dist are package names.
func TestScanSymbolsSkipsExternalAndBuildOutputDirs(t *testing.T) {
	dir := t.TempDir()
	writeGoUse(t, dir, "main.go", "Main", 0)
	writeGoUse(t, dir, "node_modules/esbuild/pkg/api.go", "NodeModules", 0)
	writeGoUse(t, dir, "web/node_modules/x/x.go", "NestedNodeModules", 0)
	writeGoUse(t, dir, "dist/linux_amd64/gen.go", "Dist", 0)
	writeGoUse(t, dir, "build/ci.go", "Build", 0)
	writeGoUse(t, dir, "internal/build/build.go", "InternalBuild", 0)
	writeGoUse(t, dir, "cmd/dist/dist.go", "CmdDist", 0)

	usages, err := scanSymbols(context.Background(), dir, budgetLib, clocklessBudget())
	if err != nil {
		t.Fatal(err)
	}
	for _, sym := range []string{"Main", "InternalBuild", "CmdDist"} {
		if !hasLibUse(usages, sym) {
			t.Errorf("lib.%s missing: a Go package directory was pruned; got %+v", sym, usages)
		}
	}
	for _, sym := range []string{"NodeModules", "NestedNodeModules", "Dist", "Build"} {
		if hasLibUse(usages, sym) {
			t.Errorf("lib.%s recorded: an external or build-output directory was scanned", sym)
		}
	}
}

// The public entry point is bounded: a tree one file larger than the default
// file budget is never parsed to the end. Which limit stops it first depends
// on how loaded the machine is, so only the bound is asserted here; the
// tests below pin what each limit keeps.
func TestScanSymbolsDefaultBudgetBoundsLargeTree(t *testing.T) {
	dir := t.TempDir()
	n := defaultSymbolBudget.files + 1
	for i := 0; i < n; i++ {
		writeGoUse(t, dir, fmt.Sprintf("%04d.go", i), fmt.Sprintf("F%04d", i), 0)
	}
	usages, err := New().ScanSymbols(context.Background(), dir, budgetLib)
	if err != nil {
		t.Fatal(err)
	}
	if last := fmt.Sprintf("F%04d", n-1); hasLibUse(usages, last) {
		t.Fatalf("lib.%s recorded: the scan parsed past its file budget", last)
	}
	if len(usages) > defaultSymbolBudget.files {
		t.Fatalf("%d usages from one-usage files, budget is %d files", len(usages), defaultSymbolBudget.files)
	}
}

func TestScanSymbolsFileBudgetKeepsEarlierResults(t *testing.T) {
	dir := t.TempDir()
	writeGoUse(t, dir, "a.go", "A", 0)
	writeGoUse(t, dir, "b.go", "B", 0)
	writeGoUse(t, dir, "c.go", "C", 0)

	b := clocklessBudget()
	b.files = 2
	usages, err := scanSymbols(context.Background(), dir, budgetLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLibUse(usages, "A") || !hasLibUse(usages, "B") {
		t.Errorf("usages parsed before the budget ran out were dropped; got %+v", usages)
	}
	if hasLibUse(usages, "C") {
		t.Error("lib.C recorded: the third file was parsed past a two-file budget")
	}
}

func TestScanSymbolsByteBudgets(t *testing.T) {
	dir := t.TempDir()
	writeGoUse(t, dir, "a.go", "A", 100)
	writeGoUse(t, dir, "b-oversized.go", "Oversized", 200)
	writeGoUse(t, dir, "c.go", "C", 100)
	writeGoUse(t, dir, "d.go", "D", 100)
	writeGoUse(t, dir, "e.go", "E", 100)

	b := clocklessBudget()
	// The oversized file would still fit the total (100+200 <= 350), so only
	// the per-file limit can keep it out. a, c and d then use 300 bytes, and
	// e would bring the total to 400.
	b.fileSize = 150
	b.bytes = 350
	usages, err := scanSymbols(context.Background(), dir, budgetLib, b)
	if err != nil {
		t.Fatal(err)
	}
	if hasLibUse(usages, "Oversized") {
		t.Error("a file over the per-file size limit was parsed")
	}
	for _, sym := range []string{"A", "C", "D"} {
		if !hasLibUse(usages, sym) {
			t.Errorf("lib.%s missing: an oversized file must be skipped, not end the scan; got %+v", sym, usages)
		}
	}
	if hasLibUse(usages, "E") {
		t.Error("lib.E recorded: the total byte budget was exceeded")
	}
}

func TestScanSymbolsSpentTimeBudgetCompletes(t *testing.T) {
	dir := t.TempDir()
	writeGoUse(t, dir, "a.go", "A", 0)

	b := defaultSymbolBudget
	b.elapsed = 0
	usages, err := scanSymbols(context.Background(), dir, budgetLib, b)
	if err != nil {
		t.Fatalf("a spent time budget must end the scan cleanly, got %v", err)
	}
	if len(usages) != 0 {
		t.Errorf("a zero time budget parsed files: %+v", usages)
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
	writeGoUse(t, dir, "a.go", "A", 0)
	for i := 0; i < 8; i++ {
		writeGoUse(t, dir, fmt.Sprintf("m%d.go", i), fmt.Sprintf("M%d", i), 0)
	}
	writeGoUse(t, dir, "z.go", "Z", 0)

	// Room for the root directory and a.go however often a walk step asks,
	// and far too little to reach z.go.
	ctx := &cancelAfter{Context: context.Background(), n: 4}
	usages, err := scanSymbols(ctx, dir, budgetLib, clocklessBudget())
	if err != nil {
		t.Fatalf("cancellation must end the scan cleanly, got %v", err)
	}
	if !hasLibUse(usages, "A") {
		t.Errorf("usage parsed before cancellation was dropped; got %+v", usages)
	}
	if hasLibUse(usages, "Z") {
		t.Error("lib.Z recorded: the walk kept parsing after cancellation")
	}
}
