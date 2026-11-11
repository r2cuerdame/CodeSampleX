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

func TestScanPackagesQuotedGoModTokens(t *testing.T) {
	dir := t.TempDir()
	gomod := `module example.com/quoted

go 1.26

require (
	"example.com/private" "v1.2.3"
	"example.com/old" "v1.0.0"
	"example.com/quoted/symbol" "v1.4.0"
	"example.com/share" "v0.3.0" // indirect
)

replace (
	"example.com/private" "v1.2.3" => "../local module"
	"example.com/old" "v1.0.0" => "example.com/new" "v2.0.0"
	"example.com/share" => "//server/share/mod" // a // inside quotes is not a comment
)
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgs, err := New().ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	byName := make(map[string]scanner.ResolvedPackage, len(pkgs))
	for _, pkg := range pkgs {
		byName[pkg.PURL.Name] = pkg
	}
	private, ok := byName["example.com/private"]
	if !ok || private.PURL.Version != "v1.2.3" || private.Publicness != scanner.PublicnessPrivate || !private.Direct {
		t.Errorf("quoted local replacement = %+v, want unquoted path/version, PRIVATE and direct", private)
	}
	replaced, ok := byName["example.com/new"]
	if !ok || replaced.PURL.Version != "v2.0.0" || !replaced.Direct {
		t.Errorf("quoted module replacement = %+v, want example.com/new v2.0.0 direct", replaced)
	}
	symbol, ok := byName["example.com/quoted/symbol"]
	if !ok || symbol.PURL.Version != "v1.4.0" || !symbol.Direct {
		t.Errorf("quoted require = %+v, want normalized module path/version and direct", symbol)
	}
	share, ok := byName["example.com/share"]
	if !ok || share.PURL.Version != "v0.3.0" || share.Publicness != scanner.PublicnessPrivate || share.Direct {
		t.Errorf("quoted share replacement = %+v, want PRIVATE and indirect", share)
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
		// -C is cmd/go's working-directory flag, legal only in first position.
		{[]string{"go", "-C", "subpkg", "test", "./..."}, domain.StageProjectTest, true},
		{[]string{"go", "-C=subpkg", "test", "./..."}, domain.StageProjectTest, true},
		{[]string{"go", "--C", "subpkg", "test", "./..."}, domain.StageProjectTest, true},
		{[]string{"go", "--C=subpkg", "test", "./..."}, domain.StageProjectTest, true},
		{[]string{"go", "-C", "subpkg", "build", "./..."}, domain.StageProjectCompile, true},
		{[]string{"go", "-C=subpkg", "build", "./..."}, domain.StageProjectCompile, true},
		{[]string{"go", "-C", "subpkg", "vet", "./..."}, domain.StageProjectCompile, true},
		{[]string{"go", "-C", "subpkg", "install", "./cmd/tool"}, domain.StageProjectCompile, true},
		{[]string{"go", "-C", "subpkg", "run", "."}, domain.StageProjectProcess, true},
		{[]string{`C:\Go\bin\go.exe`, "-C", `sub\pkg`, "test"}, domain.StageProjectTest, true},
		{[]string{"go", "-C", "subpkg", "mod", "tidy"}, "", false},
		{[]string{"go", "-C", "subpkg"}, "", false},
		{[]string{"go", "-C"}, "", false},
		// The directory operand is never read as the subcommand.
		{[]string{"go", "-C", "test"}, "", false},
		{[]string{"go", "tool", "compile", "main.go"}, domain.StageProjectCompile, true},
		{[]string{"go", "tool", "vet", "cfg.json"}, domain.StageProjectCompile, true},
		{[]string{"go", "tool", "-n", "compile"}, "", false},
		{[]string{"go", "-C", "subpkg", "tool", "compile", "main.go"}, domain.StageProjectCompile, true},
		{[]string{"go", "tool", "pprof", "cpu.out"}, "", false},
		{[]string{"go", "tool"}, "", false},
		// Module tools declared with go.mod's tool directive run by name or by
		// package path, and are the tool they name.
		{[]string{"go", "tool", "gotestsum", "--", "./..."}, domain.StageProjectTest, true},
		{[]string{"go", "tool", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint", "run"}, domain.StageProjectTypecheck, true},
		{[]string{"go", "tool", "golangci-lint", "linters"}, "", false},
		{[]string{"gotestsum"}, domain.StageProjectTest, true},
		{[]string{"gotestsum", "--", "./..."}, domain.StageProjectTest, true},
		{[]string{"gotestsum", "run", "--", "./..."}, domain.StageProjectTest, true},
		{[]string{"gotestsum", "--format", "testname", "--", "-count=1", "./..."}, domain.StageProjectTest, true},
		{[]string{`C:\tools\gotestsum.exe`, "--", "./..."}, domain.StageProjectTest, true},
		{[]string{"gotestsum", "tool", "slowest", "--jsonfile", "run.json"}, "", false},
		{[]string{"gotestsum", "help"}, "", false},
		{[]string{"golangci-lint", "run", "./..."}, domain.StageProjectTypecheck, true},
		{[]string{"golangci-lint", "-v", "run"}, domain.StageProjectTypecheck, true},
		{[]string{"/usr/local/bin/golangci-lint", "run"}, domain.StageProjectTypecheck, true},
		{[]string{`C:\tools\golangci-lint.exe`, "run", "./..."}, domain.StageProjectTypecheck, true},
		{[]string{"golangci-lint"}, "", false},
		{[]string{"golangci-lint", "linters"}, "", false},
		{[]string{"golangci-lint", "fmt"}, "", false},
		// Global flags may precede the subcommand, and a value-taking one
		// carries its value with it in either spelling.
		{[]string{"golangci-lint", "--color", "never", "run", "./..."}, domain.StageProjectTypecheck, true},
		{[]string{"golangci-lint", "--color=never", "run", "./..."}, domain.StageProjectTypecheck, true},
		{[]string{"golangci-lint", "-v", "--color", "never", "run"}, domain.StageProjectTypecheck, true},
		{[]string{"golangci-lint", "--verbose", "--help=false", "run"}, domain.StageProjectTypecheck, true},
		{[]string{"golangci-lint", "-j", "4", "run", "./..."}, domain.StageProjectTypecheck, true},
		{[]string{"golangci-lint", "-c", ".golangci.yml", "run", "./..."}, domain.StageProjectTypecheck, true},
		{[]string{"golangci-lint", "--color", "never", "linters"}, "", false},
		{[]string{"golangci-lint", "--help", "run", "./..."}, "", false},
		{[]string{"golangci-lint", "-h", "run", "./..."}, "", false},
		{[]string{"golangci-lint", "run", "--help", "./..."}, "", false},
		{[]string{"golangci-lint", "run", "-h", "./..."}, "", false},
		{[]string{"golangci-lint", "run", "--help=true", "./..."}, "", false},
		{[]string{"golangci-lint", "run", "--help=false", "./..."}, domain.StageProjectTypecheck, true},
		// A flag's value is never read as the subcommand: cobra skips the
		// argument after any flag the root does not know as a boolean, so
		// `--fix run` dispatches to the root rather than to run.
		{[]string{"golangci-lint", "--color", "run"}, "", false},
		{[]string{"golangci-lint", "--fix", "run"}, "", false},
		{[]string{"golangci-lint", "--", "run"}, "", false},
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

// The tool a command is filed under is the program that ran, not the go
// command that launched it.
func TestClassifyCommandNamesTheTool(t *testing.T) {
	a := New()
	for _, tc := range []struct {
		argv []string
		tool string
	}{
		{[]string{"go", "-C", "subpkg", "test", "./..."}, "go"},
		{[]string{"go", "tool", "compile", "main.go"}, "go"},
		{[]string{"go", "tool", "pprof", "cpu.out"}, "go"},
		{[]string{"gotestsum", "--", "./..."}, "gotestsum"},
		{[]string{"gotestsum", "tool", "slowest"}, "gotestsum"},
		{[]string{"go", "tool", "gotestsum", "--", "./..."}, "gotestsum"},
		{[]string{"golangci-lint", "run", "./..."}, "golangci-lint"},
		{[]string{"golangci-lint", "linters"}, "golangci-lint"},
		{[]string{"go", "tool", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint", "run"}, "golangci-lint"},
	} {
		if got := a.ClassifyCommand(tc.argv).Tool; got != tc.tool {
			t.Errorf("ClassifyCommand(%v).Tool = %q, want %q", tc.argv, got, tc.tool)
		}
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
