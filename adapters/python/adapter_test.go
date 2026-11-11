package python_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	python "github.com/r2cuerdame/codesamplex/adapters/python"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func findPkg(t *testing.T, pkgs []scanner.ResolvedPackage, name string) scanner.ResolvedPackage {
	t.Helper()
	for _, p := range pkgs {
		if p.PURL.Name == name {
			return p
		}
	}
	t.Fatalf("package %q not found in %+v", name, pkgs)
	return scanner.ResolvedPackage{}
}

func hasPkg(pkgs []scanner.ResolvedPackage, name string) bool {
	for _, p := range pkgs {
		if p.PURL.Name == name {
			return true
		}
	}
	return false
}

func findUsage(t *testing.T, usages []scanner.SymbolUsage, family string) scanner.SymbolUsage {
	t.Helper()
	for _, u := range usages {
		if u.Family == family {
			return u
		}
	}
	t.Fatalf("usage %q not found in %+v", family, usages)
	return scanner.SymbolUsage{}
}

func TestIdentity(t *testing.T) {
	a := python.New()
	if got := a.Ecosystem(); got != "pypi" {
		t.Errorf("Ecosystem() = %q, want pypi", got)
	}
	want := []string{"A0", "A1", "A2"}
	caps := a.Capabilities()
	if len(caps) != len(want) {
		t.Fatalf("Capabilities() = %v, want %v", caps, want)
	}
	for i := range want {
		if caps[i] != want[i] {
			t.Errorf("Capabilities()[%d] = %q, want %q", i, caps[i], want[i])
		}
	}
}

func TestDetect(t *testing.T) {
	a := python.New()
	empty := t.TempDir()
	if a.Detect(empty) {
		t.Error("Detect(empty dir) = true, want false")
	}
	for _, marker := range []string{"pyproject.toml", "requirements.txt", "uv.lock"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, marker), []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !a.Detect(dir) {
			t.Errorf("Detect with %s = false, want true", marker)
		}
	}
}

func TestScanPackagesUvLock(t *testing.T) {
	a := python.New()
	pkgs, err := a.ScanPackages(context.Background(), filepath.Join("testdata", "uvproj"))
	if err != nil {
		t.Fatal(err)
	}

	fastapi := findPkg(t, pkgs, "fastapi")
	if fastapi.PURL.Version != "0.116.0" {
		t.Errorf("fastapi version = %q, want 0.116.0", fastapi.PURL.Version)
	}
	if fastapi.PURL.Ecosystem != "pypi" {
		t.Errorf("fastapi ecosystem = %q, want pypi", fastapi.PURL.Ecosystem)
	}
	if fastapi.Publicness != scanner.PublicnessUnknown {
		t.Errorf("fastapi publicness = %q, want UNKNOWN", fastapi.Publicness)
	}
	if !fastapi.Direct {
		t.Error("fastapi should be direct (listed in pyproject [project] dependencies)")
	}

	pyyaml := findPkg(t, pkgs, "pyyaml")
	if pyyaml.PURL.Version != "6.0.2" || !pyyaml.Direct {
		t.Errorf("pyyaml = %+v, want version 6.0.2 direct", pyyaml)
	}

	anno := findPkg(t, pkgs, "annotated-types")
	if anno.Direct {
		t.Error("annotated-types should be transitive")
	}

	self := findPkg(t, pkgs, "uvproj")
	if self.Publicness != scanner.PublicnessPrivate {
		t.Errorf("editable project entry publicness = %q, want PRIVATE", self.Publicness)
	}
}

func TestScanPackagesRequirements(t *testing.T) {
	a := python.New()
	pkgs, err := a.ScanPackages(context.Background(), filepath.Join("testdata", "reqproj"))
	if err != nil {
		t.Fatal(err)
	}

	fastapi := findPkg(t, pkgs, "fastapi")
	if fastapi.PURL.Version != "0.116.0" {
		t.Errorf("fastapi version = %q, want 0.116.0", fastapi.PURL.Version)
	}
	if fastapi.Publicness != scanner.PublicnessUnknown {
		t.Errorf("fastapi publicness = %q, want UNKNOWN", fastapi.Publicness)
	}
	if !fastapi.Direct {
		t.Error("requirements pins should be direct")
	}

	// PyYAML normalizes to pyyaml per PEP 503.
	pyyaml := findPkg(t, pkgs, "pyyaml")
	if pyyaml.PURL.Version != "6.0.2" {
		t.Errorf("pyyaml version = %q, want 6.0.2", pyyaml.PURL.Version)
	}

	local := findPkg(t, pkgs, "local")
	if local.Publicness != scanner.PublicnessPrivate {
		t.Errorf("-e ./local publicness = %q, want PRIVATE", local.Publicness)
	}

	git := findPkg(t, pkgs, "private-lib")
	if git.Publicness != scanner.PublicnessPrivate {
		t.Errorf("git+ dep publicness = %q, want PRIVATE", git.Publicness)
	}

	if hasPkg(pkgs, "uvicorn") {
		t.Error("range spec uvicorn>=0.30 must not produce a resolved package")
	}
}

func TestScanPackagesPoetryLock(t *testing.T) {
	a := python.New()
	pkgs, err := a.ScanPackages(context.Background(), filepath.Join("testdata", "poetryproj"))
	if err != nil {
		t.Fatal(err)
	}

	fastapi := findPkg(t, pkgs, "fastapi")
	if fastapi.PURL.Version != "0.116.0" {
		t.Errorf("fastapi version = %q, want 0.116.0", fastapi.PURL.Version)
	}
	if !fastapi.Direct {
		t.Error("fastapi should be direct via pyproject [project] dependencies")
	}

	pydantic := findPkg(t, pkgs, "pydantic")
	if pydantic.PURL.Version != "2.11.0" || pydantic.Direct {
		t.Errorf("pydantic = %+v, want version 2.11.0 transitive", pydantic)
	}
}

func TestScanPackagesPyprojectOptionalDependenciesAreDirect(t *testing.T) {
	dir := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("pyproject.toml", `[project]
name = "optional-project"
dependencies = ["Base_Pkg>=1"]

[project.optional-dependencies]
test = [
    "Extra.Pkg>=2",
]
`)
	writeFixture("poetry.lock", `[[package]]
name = "base-pkg"
version = "1.0.0"

[[package]]
name = "extra-pkg"
version = "2.0.0"

[[package]]
name = "transitive-pkg"
version = "3.0.0"
`)

	pkgs, err := python.New().ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"base-pkg", "extra-pkg"} {
		if got := findPkg(t, pkgs, name); !got.Direct {
			t.Errorf("%s should be direct from pyproject.toml", name)
		}
	}
	if got := findPkg(t, pkgs, "transitive-pkg"); got.Direct {
		t.Error("transitive-pkg should remain indirect")
	}
}

func TestScanPackagesPoetryDependencyGroupsAreDirect(t *testing.T) {
	dir := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("pyproject.toml", `[tool.poetry.dependencies]
python = "^3.11"
Fast_API = { version = "^0.1", optional = true }

[tool.poetry.group.dev.dependencies]
Pytest = "^8.0"
`)
	writeFixture("poetry.lock", `[[package]]
name = "fast-api"
version = "0.1.0"

[[package]]
name = "pytest"
version = "8.0.0"

[[package]]
name = "transitive-pkg"
version = "3.0.0"
`)

	pkgs, err := python.New().ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fast-api", "pytest"} {
		if got := findPkg(t, pkgs, name); !got.Direct {
			t.Errorf("%s should be direct from Poetry dependencies", name)
		}
	}
	if got := findPkg(t, pkgs, "transitive-pkg"); got.Direct {
		t.Error("transitive-pkg should remain indirect")
	}
	if hasPkg(pkgs, "python") {
		t.Error("the Poetry Python interpreter constraint must not become a package")
	}
}

func TestScanPackagesPoetryArrayTableEndsDependencySection(t *testing.T) {
	dir := t.TempDir()
	writeFixture := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("pyproject.toml", `[tool.poetry.dependencies]
python = "^3.11"
requests = "^2.0"

[[tool.poetry.source]]
name = "internal"
url = "https://packages.example.invalid/simple"
priority = "primary"
`)
	writeFixture("poetry.lock", `[[package]]
name = "requests"
version = "2.32.0"

[[package]]
name = "priority"
version = "1.0.0"

[[package]]
name = "name"
version = "1.0.0"

[[package]]
name = "url"
version = "1.0.0"
`)

	pkgs, err := python.New().ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := findPkg(t, pkgs, "requests"); !got.Direct {
		t.Error("requests should remain direct from Poetry dependencies")
	}
	for _, name := range []string{"priority", "name", "url"} {
		if got := findPkg(t, pkgs, name); got.Direct {
			t.Errorf("%s from the Poetry source array table must not be direct", name)
		}
	}
}

func TestScanSymbols(t *testing.T) {
	a := python.New()
	dir := filepath.Join("testdata", "reqproj")
	ctx := context.Background()
	pkgs, err := a.ScanPackages(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	usages, err := a.ScanSymbols(ctx, dir, pkgs)
	if err != nil {
		t.Fatal(err)
	}

	fa := findUsage(t, usages, "fastapi.FastAPI")
	if fa.Confidence != domain.SymbolProbable {
		t.Errorf("fastapi.FastAPI confidence = %q, want PROBABLE", fa.Confidence)
	}
	if fa.Package.Name != "fastapi" {
		t.Errorf("fastapi.FastAPI package = %q, want fastapi", fa.Package.Name)
	}
	router := findUsage(t, usages, "fastapi.APIRouter")
	if router.Confidence != domain.SymbolProbable {
		t.Errorf("fastapi.APIRouter confidence = %q, want PROBABLE", router.Confidence)
	}

	// Plain `import yaml` -> package-level UNKNOWN, mapped to dist pyyaml.
	y := findUsage(t, usages, "yaml")
	if y.Confidence != domain.SymbolUnknown {
		t.Errorf("yaml confidence = %q, want UNKNOWN", y.Confidence)
	}
	if y.Package.Name != "pyyaml" {
		t.Errorf("yaml maps to dist %q, want pyyaml", y.Package.Name)
	}
	if y.Kind != "module" {
		t.Errorf("yaml kind = %q, want module", y.Kind)
	}

	// requests is imported only inside .venv, which must be skipped.
	for _, u := range usages {
		if u.Package.Name == "requests" {
			t.Errorf("found usage of requests from .venv: %+v", u)
		}
	}
}

func TestScanSymbolsDynamicDegrade(t *testing.T) {
	a := python.New()
	dir := filepath.Join("testdata", "dynproj")
	ctx := context.Background()
	pkgs, err := a.ScanPackages(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	usages, err := a.ScanSymbols(ctx, dir, pkgs)
	if err != nil {
		t.Fatal(err)
	}

	// getattr(fastapi, ...) degrades every fastapi symbol to UNKNOWN (§13.3).
	fa := findUsage(t, usages, "fastapi.FastAPI")
	if fa.Confidence != domain.SymbolUnknown {
		t.Errorf("fastapi.FastAPI confidence = %q, want UNKNOWN after getattr", fa.Confidence)
	}

	// importlib.import_module("yaml") degrades pyyaml symbols too.
	sl := findUsage(t, usages, "yaml.safe_load")
	if sl.Confidence != domain.SymbolUnknown {
		t.Errorf("yaml.safe_load confidence = %q, want UNKNOWN after importlib", sl.Confidence)
	}

	// stdlib importlib is not a scanned package and must not appear.
	for _, u := range usages {
		if u.Package.Name == "importlib" {
			t.Errorf("stdlib importlib reported as usage: %+v", u)
		}
	}
}

// The public entry point is bounded: a project one file past the 64-file
// budget is never read to the end. Which limit stops it first depends on how
// loaded the machine is, so only the bound is asserted here;
// symbols_budget_test.go pins what each limit keeps.
func TestScanSymbolsDefaultBudgetBoundsLargeTree(t *testing.T) {
	const fileBudget = 64
	dir := t.TempDir()
	for i := 0; i <= fileBudget; i++ {
		src := fmt.Sprintf("from fastapi import F%04d\n", i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.py", i)), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkgs := []scanner.ResolvedPackage{{PURL: domain.PURL{Ecosystem: "pypi", Name: "fastapi", Version: "0.115.0"}}}
	got, err := python.New().ScanSymbols(context.Background(), dir, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	last := fmt.Sprintf("fastapi.F%04d", fileBudget)
	for _, u := range got {
		if u.Family == last {
			t.Fatalf("%s recorded: the scan read past its file budget", last)
		}
	}
	if len(got) > fileBudget {
		t.Fatalf("%d usages from one-usage files, budget is %d files", len(got), fileBudget)
	}
}

func TestClassifyCommand(t *testing.T) {
	a := python.New()
	tests := []struct {
		argv  []string
		stage domain.Stage
		known bool
	}{
		{[]string{"pytest"}, domain.StageProjectTest, true},
		{[]string{"pytest", "-q", "tests/"}, domain.StageProjectTest, true},
		{[]string{"python", "-m", "pytest"}, domain.StageProjectTest, true},
		{[]string{"uv", "run", "pytest"}, domain.StageProjectTest, true},
		{[]string{"mypy", "."}, domain.StageProjectTypecheck, true},
		{[]string{"python", "-m", "mypy", "src"}, domain.StageProjectTypecheck, true},
		{[]string{"uv", "run", "mypy", "."}, domain.StageProjectTypecheck, true},
		{[]string{"python", "app.py"}, domain.StageProjectProcess, true},
		{[]string{"python3", "app.py"}, domain.StageProjectProcess, true},
		{[]string{"uv", "run", "app.py"}, domain.StageProjectProcess, true},
		{[]string{"pip", "install", "fastapi"}, domain.StageUsed, true},
		{[]string{"pip3", "install", "-r", "requirements.txt"}, domain.StageUsed, true},
		{[]string{"uv", "sync"}, domain.StageUsed, true},
		{[]string{"uv", "pip", "install", "fastapi"}, domain.StageUsed, true},
		{[]string{"cargo", "build"}, "", false},
		{[]string{"pip", "freeze"}, "", false},
		{nil, "", false},
	}
	for _, tt := range tests {
		got := a.ClassifyCommand(tt.argv)
		if got.Known != tt.known {
			t.Errorf("ClassifyCommand(%v).Known = %v, want %v", tt.argv, got.Known, tt.known)
			continue
		}
		if tt.known && got.Stage != tt.stage {
			t.Errorf("ClassifyCommand(%v).Stage = %q, want %q", tt.argv, got.Stage, tt.stage)
		}
	}
}

func TestEnvironmentHints(t *testing.T) {
	a := python.New()
	ctx := context.Background()

	uv := a.EnvironmentHints(ctx, filepath.Join("testdata", "uvproj"))
	if uv["ecosystem"] != "pypi" || uv["runtime"] != "python" || uv["language"] != "python" {
		t.Errorf("uvproj hints = %v", uv)
	}
	if uv["packageManager"] != "uv" {
		t.Errorf("uvproj packageManager = %q, want uv", uv["packageManager"])
	}
	if ms, ok := uv["moduleSystem"]; !ok || ms != "" {
		t.Errorf("moduleSystem hint = %q (present=%v), want empty string present", ms, ok)
	}

	pip := a.EnvironmentHints(ctx, filepath.Join("testdata", "reqproj"))
	if pip["packageManager"] != "pip" {
		t.Errorf("reqproj packageManager = %q, want pip", pip["packageManager"])
	}
}
