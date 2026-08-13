package goadapter

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func scanFixtureSymbols(t *testing.T) []scanner.SymbolUsage {
	t.Helper()
	a := New()
	dir := fixtureDir(t)
	pkgs, err := a.ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	usages, err := a.ScanSymbols(context.Background(), dir, pkgs)
	if err != nil {
		t.Fatalf("ScanSymbols: %v", err)
	}
	return usages
}

func findUsage(usages []scanner.SymbolUsage, family string) (scanner.SymbolUsage, bool) {
	for _, u := range usages {
		if u.Family == family {
			return u, true
		}
	}
	return scanner.SymbolUsage{}, false
}

func TestScanSymbolsSelector(t *testing.T) {
	usages := scanFixtureSymbols(t)

	u, ok := findUsage(usages, "github.com/stretchr/testify/assert.Equal")
	if !ok {
		t.Fatalf("assert.Equal usage missing; got %+v", usages)
	}
	if u.Confidence != domain.SymbolProbable {
		t.Errorf("confidence %q, want PROBABLE", u.Confidence)
	}
	if u.Package.Name != "github.com/stretchr/testify" || u.Package.Version != "v1.9.0" {
		t.Errorf("package %+v, want testify v1.9.0", u.Package)
	}

	// Aliased import: req "github.com/stretchr/testify/require".
	if _, ok := findUsage(usages, "github.com/stretchr/testify/require.NoError"); !ok {
		t.Errorf("aliased require.NoError usage missing; got %+v", usages)
	}
}

func TestScanSymbolsDedup(t *testing.T) {
	usages := scanFixtureSymbols(t)
	n := 0
	for _, u := range usages {
		if u.Family == "github.com/stretchr/testify/assert.Equal" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("assert.Equal appears %d times, want deduplicated to 1", n)
	}
}

func TestScanSymbolsDotAndBlankImports(t *testing.T) {
	usages := scanFixtureSymbols(t)

	dot, ok := findUsage(usages, "github.com/davecgh/go-spew/spew")
	if !ok {
		t.Fatalf("dot-import package-level usage missing; got %+v", usages)
	}
	if dot.Confidence != domain.SymbolUnknown {
		t.Errorf("dot-import confidence %q, want UNKNOWN", dot.Confidence)
	}

	blank, ok := findUsage(usages, "github.com/pmezard/go-difflib/difflib")
	if !ok {
		t.Fatalf("blank-import package-level usage missing; got %+v", usages)
	}
	if blank.Confidence != domain.SymbolUnknown {
		t.Errorf("blank-import confidence %q, want UNKNOWN", blank.Confidence)
	}
}

func TestScanSymbolsSkipsVendorAndTests(t *testing.T) {
	usages := scanFixtureSymbols(t)
	if _, ok := findUsage(usages, "github.com/stretchr/testify/assert.ObjectsAreEqual"); ok {
		t.Error("usage from vendor/ must be skipped")
	}
	if _, ok := findUsage(usages, "github.com/stretchr/testify/assert.NotEqual"); ok {
		t.Error("usage from _test.go must be skipped")
	}
	// The scanned project's own testdata dir IS included.
	if _, ok := findUsage(usages, "github.com/stretchr/testify/assert.AnError"); !ok {
		t.Errorf("usage from project testdata/ dir missing; got %+v", usages)
	}
}

func TestScanSymbolsIgnoresUnrequiredImports(t *testing.T) {
	for _, u := range scanFixtureSymbols(t) {
		if u.Package.Name == "" {
			t.Errorf("usage %q has empty package", u.Family)
		}
		if u.Package.Name == "fmt" || u.Family == "fmt.Println" {
			t.Errorf("stdlib import must not produce usage: %+v", u)
		}
	}
}
