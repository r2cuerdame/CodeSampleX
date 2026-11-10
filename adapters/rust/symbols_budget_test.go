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

func clocklessRustBudget() symbolBudget { b := defaultSymbolBudget; b.elapsed = time.Hour; return b }

func rustScanDir(t *testing.T, dir string) string {
	t.Helper()
	root := filepath.Join(dir, "src")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

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

func TestScanSymbolsFileAndTotalByteBudgets(t *testing.T) {
	dir := t.TempDir()
	root := rustScanDir(t, dir)
	writeRustUse(t, root, "a.rs", "A", 100)
	writeRustUse(t, root, "oversized.rs", "Oversized", 200)
	writeRustUse(t, root, "b.rs", "B", 100)
	writeRustUse(t, root, "c.rs", "C", 100)
	b := clocklessRustBudget()
	b.fileSize, b.bytes = 150, 250
	got, err := scanSymbols(context.Background(), dir, budgetRustLib, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"serde::A", "serde::B"} {
		if !hasRustUse(got, family) {
			t.Errorf("missing %s after oversized file skipped: %+v", family, got)
		}
	}
	for _, family := range []string{"serde::Oversized", "serde::C"} {
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
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("zero time budget scanned files: %+v", got)
	}
}

func TestScanSymbolsSymlinkTargetBudget(t *testing.T) {
	outside, dir := t.TempDir(), t.TempDir()
	root := rustScanDir(t, dir)
	writeRustUse(t, outside, "target.rs", "Linked", 200)
	writeRustUse(t, root, "a.rs", "A", 100)
	if err := os.Symlink(filepath.Join(outside, "target.rs"), filepath.Join(root, "b-link.rs")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	writeRustUse(t, root, "c.rs", "C", 100)
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
}
