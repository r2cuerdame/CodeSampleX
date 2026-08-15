package goadapter

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "fixturemod"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestDetect(t *testing.T) {
	a := New()
	if !a.Detect(fixtureDir(t)) {
		t.Error("Detect = false for dir with go.mod, want true")
	}
	if a.Detect(t.TempDir()) {
		t.Error("Detect = true for empty dir, want false")
	}
}

func TestEcosystemAndCapabilities(t *testing.T) {
	a := New()
	if got := a.Ecosystem(); got != "golang" {
		t.Errorf("Ecosystem = %q, want %q", got, "golang")
	}
	if got, want := a.Capabilities(), []string{"A0", "A1", "A2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Capabilities = %v, want %v", got, want)
	}
}

func TestScanPackages(t *testing.T) {
	a := New()
	pkgs, err := a.ScanPackages(context.Background(), fixtureDir(t))
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	byName := map[string]scanner.ResolvedPackage{}
	for _, p := range pkgs {
		if p.PURL.Ecosystem != "golang" {
			t.Errorf("%s: ecosystem %q, want golang", p.PURL.Name, p.PURL.Ecosystem)
		}
		byName[p.PURL.Name] = p
	}
	if len(pkgs) != 4 {
		t.Fatalf("got %d packages %v, want 4", len(pkgs), byName)
	}

	tf, ok := byName["github.com/stretchr/testify"]
	if !ok {
		t.Fatal("testify missing")
	}
	if tf.PURL.Version != "v1.9.0" {
		t.Errorf("testify version %q, want v1.9.0", tf.PURL.Version)
	}
	if !tf.Direct {
		t.Error("testify should be Direct")
	}
	if tf.Publicness != scanner.PublicnessUnknown {
		t.Errorf("testify publicness %q, want UNKNOWN", tf.Publicness)
	}
	if tf.Source != "go.mod+go.sum" {
		t.Errorf("testify Source %q, want go.mod+go.sum (go.sum present)", tf.Source)
	}

	for _, ind := range []string{"github.com/davecgh/go-spew", "github.com/pmezard/go-difflib"} {
		p, ok := byName[ind]
		if !ok {
			t.Fatalf("%s missing", ind)
		}
		if p.Direct {
			t.Errorf("%s marked Direct, want indirect", ind)
		}
	}

	priv, ok := byName["example.com/privatemod"]
	if !ok {
		t.Fatal("example.com/privatemod missing")
	}
	if priv.Publicness != scanner.PublicnessPrivate {
		t.Errorf("replaced-to-local module publicness %q, want PRIVATE", priv.Publicness)
	}
	if !priv.Direct {
		t.Error("privatemod should be Direct")
	}
}

func TestScanPackagesNoGoSum(t *testing.T) {
	dir := t.TempDir()
	gomod := "module example.com/nosum\n\ngo 1.26\n\nrequire github.com/stretchr/testify v1.9.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgs, err := New().ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].Source != "go.mod" {
		t.Errorf("got %+v, want single package with Source go.mod", pkgs)
	}
}

func TestScanPackagesReplaceForms(t *testing.T) {
	dir := t.TempDir()
	gomod := `module example.com/replaceforms

go 1.26

require (
	example.com/rel v0.1.0
	example.com/drive v0.1.0
	example.com/remote v0.1.0
	example.com/blockrel v0.1.0
)

replace example.com/rel v0.1.0 => ../rel
replace example.com/drive => C:\work\drivemod
replace example.com/remote => example.com/remote2 v0.2.0

replace (
	example.com/blockrel => ./local/blockrel
)
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgs, err := New().ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// A module replace decides what actually compiles, so the scan reports
	// the REPLACEMENT — example.com/remote2 at v0.2.0 — not the require
	// line. Reporting the require line filed evidence produced by one
	// version under another, and an agent asking about that other version
	// got a HIT backed by a build that never used it.
	want := map[string]string{
		"example.com/rel":      scanner.PublicnessPrivate,
		"example.com/drive":    scanner.PublicnessPrivate,
		"example.com/remote2":  scanner.PublicnessUnknown,
		"example.com/blockrel": scanner.PublicnessPrivate,
	}
	wantVersion := map[string]string{"example.com/remote2": "v0.2.0"}
	if len(pkgs) != len(want) {
		t.Fatalf("got %d packages, want %d", len(pkgs), len(want))
	}
	for _, p := range pkgs {
		if w := want[p.PURL.Name]; p.Publicness != w {
			t.Errorf("%s publicness %q, want %q", p.PURL.Name, p.Publicness, w)
		}
		if v, ok := wantVersion[p.PURL.Name]; ok && p.PURL.Version != v {
			t.Errorf("%s version %q, want %q — the replacement decides", p.PURL.Name, p.PURL.Version, v)
		}
	}
}

func TestClassifyCommand(t *testing.T) {
	a := New()
	tests := []struct {
		argv  []string
		stage domain.Stage
		known bool
	}{
		{[]string{"go", "build", "./..."}, domain.StageProjectCompile, true},
		{[]string{"go", "vet", "./..."}, domain.StageProjectCompile, true},
		{[]string{"go", "install", "example.com/tool@latest"}, domain.StageProjectCompile, true},
		{[]string{"go", "test", "-count=1", "./..."}, domain.StageProjectTest, true},
		{[]string{"go", "run", "."}, domain.StageProjectProcess, true},
		{[]string{`C:\Go\bin\go.exe`, "build"}, domain.StageProjectCompile, true},
		{[]string{"go", "mod", "tidy"}, "", false},
		{[]string{"go"}, "", false},
		{[]string{"gofmt", "-l", "."}, "", false},
		{[]string{"python", "x.py"}, "", false},
		{nil, "", false},
	}
	for _, tc := range tests {
		got := a.ClassifyCommand(tc.argv)
		if got.Known != tc.known {
			t.Errorf("ClassifyCommand(%v).Known = %v, want %v", tc.argv, got.Known, tc.known)
			continue
		}
		if tc.known && got.Stage != tc.stage {
			t.Errorf("ClassifyCommand(%v).Stage = %q, want %q", tc.argv, got.Stage, tc.stage)
		}
	}
	if p := a.ClassifyCommand([]string{"gofmt", "-l", "."}); p.Tool != "gofmt" {
		t.Errorf("gofmt Tool = %q, want gofmt", p.Tool)
	}
}

func TestEnvironmentHints(t *testing.T) {
	got := New().EnvironmentHints(context.Background(), fixtureDir(t))
	want := map[string]string{
		"ecosystem":      "golang",
		"runtime":        "go",
		"language":       "go",
		"packageManager": "go",
		"moduleSystem":   "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnvironmentHints = %v, want %v", got, want)
	}
}
