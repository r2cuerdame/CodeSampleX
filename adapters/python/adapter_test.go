package python_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func TestScanPackagesPyprojectCommentAndQuoteBoundaries(t *testing.T) {
	pyproject := `[project]
name = "comment-boundary-proj"
version = "0.1.0"
# dependencies = ["commented-header-pkg>=1.0"]
dependencies = [
    # "commented-line>=1.0",
    # 'single-commented-line>=1.0',
    "Fast_API>=0.116.0", # disabled "commented-inline>=1.5.0" ]
    "requests[security,socks]>=2.32.0; extra == \"#not-comment]\" and sys_platform == \"win32\"", # [todo 'another-inline>=1.0'
    'HTTPX.Client[http2]>=0.27.0; python_version >= "3.11" and extra == "#also-not-comment]"',
    "urllib3 @ https://files.pythonhosted.org/packages/urllib3-2.2.3.tar.gz#sha256=deadbeef",
]
classifiers = [
    "Framework :: Django",
]

[project.optional-dependencies]
# disabled-extra = ["commented-group-pkg>=1.0"]
test = [
    # "opt-commented-line>=1.0", # ]
    "Py.Test>=8.0.0", # "opt-commented-inline>=2.0" ]
    "coverage[toml]>=7.6.0; extra == \"#opt]\"",
]

[tool.poetry.group.lint.dependencies]
# ruff-old = "^0.5.0"
Ruff = "^0.8.0"
`

	type pkgExpectation struct {
		version    string
		direct     bool
		publicness string
	}
	want := map[string]pkgExpectation{
		"fast-api":              {"0.116.0", true, scanner.PublicnessUnknown},
		"requests":              {"2.32.3", true, scanner.PublicnessUnknown},
		"httpx-client":          {"0.27.2", true, scanner.PublicnessUnknown},
		"urllib3":               {"2.2.3", true, scanner.PublicnessUnknown},
		"py-test":               {"8.3.3", true, scanner.PublicnessUnknown},
		"coverage":              {"7.6.1", true, scanner.PublicnessUnknown},
		"ruff":                  {"0.8.1", true, scanner.PublicnessUnknown},
		"commented-header-pkg":  {"1.0.0", false, scanner.PublicnessUnknown},
		"commented-line":        {"1.1.0", false, scanner.PublicnessUnknown},
		"single-commented-line": {"1.2.0", false, scanner.PublicnessUnknown},
		"commented-inline":      {"1.5.0", false, scanner.PublicnessUnknown},
		"another-inline":        {"1.6.0", false, scanner.PublicnessUnknown},
		"and":                   {"0.1.0", false, scanner.PublicnessUnknown},
		"framework":             {"4.0.0", false, scanner.PublicnessUnknown},
		"commented-group-pkg":   {"1.3.0", false, scanner.PublicnessUnknown},
		"opt-commented-line":    {"1.4.0", false, scanner.PublicnessUnknown},
		"opt-commented-inline":  {"2.0.0", false, scanner.PublicnessUnknown},
		"ruff-old":              {"0.5.0", false, scanner.PublicnessUnknown},
		"transitive-only":       {"3.0.0", false, scanner.PublicnessUnknown},
		"private-local":         {"", false, scanner.PublicnessPrivate},
	}

	var uvLock, poetryLock string
	uvLock = "version = 1\n"
	for name, spec := range want {
		if spec.publicness == scanner.PublicnessPrivate {
			uvLock += fmt.Sprintf("\n[[package]]\nname = %q\nsource = { editable = \"./local\" }\n", name)
			poetryLock += fmt.Sprintf("\n[[package]]\nname = %q\n\n[package.source]\ntype = \"directory\"\nurl = \"./local\"\n", name)
			continue
		}
		uvLock += fmt.Sprintf("\n[[package]]\nname = %q\nversion = %q\nsource = { registry = \"https://pypi.org/simple\" }\n", name, spec.version)
		poetryLock += fmt.Sprintf("\n[[package]]\nname = %q\nversion = %q\n", name, spec.version)
	}
	poetryLock += "\n[metadata]\nlock-version = \"2.1\"\n"

	for _, tc := range []struct {
		lockFile string
		content  string
	}{
		{"uv.lock", uvLock},
		{"poetry.lock", poetryLock},
	} {
		t.Run(tc.lockFile, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.lockFile), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}

			pkgs, err := python.New().ScanPackages(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(pkgs) != len(want) {
				t.Fatalf("ScanPackages(%s) returned %d packages, want %d: %+v", tc.lockFile, len(pkgs), len(want), pkgs)
			}
			for name, exp := range want {
				got := findPkg(t, pkgs, name)
				if got.Direct != exp.direct {
					t.Errorf("%s (%s): Direct = %v, want %v", name, tc.lockFile, got.Direct, exp.direct)
				}
				if got.PURL.Version != exp.version {
					t.Errorf("%s (%s): Version = %q, want %q", name, tc.lockFile, got.PURL.Version, exp.version)
				}
				if got.Source != tc.lockFile {
					t.Errorf("%s (%s): Source = %q, want %q", name, tc.lockFile, got.Source, tc.lockFile)
				}
				if got.Publicness != exp.publicness {
					t.Errorf("%s (%s): Publicness = %q, want %q", name, tc.lockFile, got.Publicness, exp.publicness)
				}
			}
		})
	}
}

func TestScanPackagesRequirementsFallbackPreservedWithoutPyprojectDeps(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.ruff]\nline-length = 100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("Fast_API==0.116.0\n# commented-pin==1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgs, err := python.New().ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got := findPkg(t, pkgs, "fast-api")
	if !got.Direct || got.PURL.Version != "0.116.0" || got.Source != "requirements.txt" || got.Publicness != scanner.PublicnessUnknown {
		t.Fatalf("unexpected requirements fallback package: %+v", got)
	}
	if hasPkg(pkgs, "commented-pin") {
		t.Fatal("commented requirements line must remain ignored")
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

func TestScanSymbolsMultilineFromImport(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "requirements.txt"), []byte("fastapi==1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "app.py"), []byte("from fastapi import (\n    FastAPI,\n    APIRouter as Router,\n)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := python.New()
	pkgs, err := a.ScanPackages(context.Background(), tmp)
	if err != nil {
		t.Fatal(err)
	}
	usages, err := a.ScanSymbols(context.Background(), tmp, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"fastapi.FastAPI", "fastapi.APIRouter"} {
		u := findUsage(t, usages, family)
		if u.Confidence != domain.SymbolProbable || u.Kind != "property" {
			t.Errorf("usage %q = %+v, want probable property", family, u)
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
		{[]string{"python", "-m", "unittest"}, domain.StageProjectTest, true},
		{[]string{"python3", "-m", "unittest", "discover"}, domain.StageProjectTest, true},
		{[]string{"py", "-m", "pytest"}, domain.StageProjectTest, true},
		{[]string{"py", "-3", "-m", "unittest"}, domain.StageProjectTest, true},
		{[]string{"pypy", "-m", "unittest"}, domain.StageProjectTest, true},
		{[]string{"pypy3", "-m", "pytest"}, domain.StageProjectTest, true},
		{[]string{"mypy", "."}, domain.StageProjectTypecheck, true},
		{[]string{"python", "-m", "mypy", "src"}, domain.StageProjectTypecheck, true},
		{[]string{"py", "-3", "-m", "mypy", "src"}, domain.StageProjectTypecheck, true},
		{[]string{"ruff", "check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"ruff", "--isolated", "check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"ruff", "--config", "ruff.toml", "check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"ruff", "-q", "check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"ruff", "--config=ruff.toml", "check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"python", "-m", "ruff", "check"}, domain.StageProjectTypecheck, true},
		{[]string{"python", "-m", "ruff", "--isolated", "check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"python", "-m", "ruff", "--config", "ruff.toml", "check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"python", "-m", "ruff", "-q", "check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"python", "-m", "ruff", "format", "check"}, "", false},
		{[]string{"py", "-3", "-m", "ruff", "check"}, domain.StageProjectTypecheck, true},
		{[]string{"pyright"}, domain.StageProjectTypecheck, true},
		{[]string{"python", "-m", "pyright"}, domain.StageProjectTypecheck, true},
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

func TestClassifyUVRunPreparationOptions(t *testing.T) {
	a := python.New()
	tests := []struct {
		name  string
		args  []string
		stage domain.Stage
		known bool
		tool  string
	}{
		{"offline module", []string{"--offline", "python", "-m", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"no sync", []string{"--no-sync", "pytest"}, domain.StageProjectTest, true, "pytest"},
		{"boolean options", []string{"--offline", "--no-sync", "--no-project", "--no-python-downloads", "python3", "-m", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"python and separator", []string{"--python", "3.12", "--", "mypy", "."}, domain.StageProjectTypecheck, true, "mypy"},
		{"package is not command", []string{"--with", "pytest", "mypy", "."}, domain.StageProjectTypecheck, true, "mypy"},
		{"equals values", []string{"--python=3.12", "--with=pytest", "mypy", "."}, domain.StageProjectTypecheck, true, "mypy"},
		{"repeated packages", []string{"--with", "pytest", "--with=mypy", "--with", "ruff", "pyright"}, domain.StageProjectTypecheck, true, "pyright"},
		{"separator", []string{"--", "pytest", "--offline"}, domain.StageProjectTest, true, "pytest"},
		{"command arguments", []string{"mypy", "--with", "pytest", "--python", "3.12"}, domain.StageProjectTypecheck, true, "mypy"},
		{"incomplete command argument", []string{"pytest", "--with"}, domain.StageProjectTest, true, "pytest"},
		{"program arguments", []string{"--no-project", "python", "app.py", "--with", "pytest", "--unknown"}, domain.StageProjectProcess, true, "python"},
		{"generic command", []string{"--offline", "app.py", "--with", "pytest"}, domain.StageProjectProcess, true, "uv"},
		{"launcher alias", []string{"--no-sync", "py", "-3", "-m", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"pypy alias", []string{"--no-project", "pypy3", "-m", "pytest"}, domain.StageProjectTest, true, "pytest"},
		{"no command", nil, "", false, "uv"},
		{"only options", []string{"--offline", "--no-sync"}, "", false, "uv"},
		{"only separator", []string{"--"}, "", false, "uv"},
		{"empty command after separator", []string{"--", "", "pytest"}, "", false, "uv"},
		{"missing python value", []string{"--python"}, "", false, "uv"},
		{"missing package value", []string{"--with"}, "", false, "uv"},
		{"empty python value", []string{"--python", "", "pytest"}, "", false, "uv"},
		{"empty package equals", []string{"--with=", "pytest"}, "", false, "uv"},
		{"empty python equals", []string{"--python=", "mypy"}, "", false, "uv"},
		{"value is option", []string{"--with", "--offline", "pytest"}, "", false, "uv"},
		{"value is separator", []string{"--python", "--", "mypy"}, "", false, "uv"},
		{"equals value is option", []string{"--with=--offline", "pytest"}, "", false, "uv"},
		{"package without command", []string{"--with", "pytest"}, "", false, "uv"},
		{"unknown flag", []string{"--unknown", "pytest"}, "", false, "uv"},
		{"unsupported valued option", []string{"--directory", "pytest", "mypy"}, "", false, "uv"},
		{"unsupported equals option", []string{"--directory=pytest", "mypy"}, "", false, "uv"},
		{"boolean with value", []string{"--offline=true", "pytest"}, "", false, "uv"},
		{"empty command", []string{"", "pytest"}, "", false, "uv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv := append([]string{"uv", "run"}, tt.args...)
			original := slices.Clone(argv)
			got := a.ClassifyCommand(argv)
			if got.Stage != tt.stage || got.Known != tt.known || got.Tool != tt.tool {
				t.Errorf("ClassifyCommand(%v) = %+v, want Stage=%q Known=%v Tool=%q", argv, got, tt.stage, tt.known, tt.tool)
			}
			if !slices.Equal(argv, original) {
				t.Errorf("ClassifyCommand changed argv from %v to %v", original, argv)
			}
		})
	}
}

func TestClassifyCommandInterpreterBoundary(t *testing.T) {
	a := python.New()
	tests := []struct {
		name  string
		argv  []string
		stage domain.Stage
		known bool
		tool  string
	}{
		{"script pytest argument", []string{"python", "app.py", "-m", "pytest"}, domain.StageProjectProcess, true, "python"},
		{"command pytest argument", []string{"python", "-c", "pass", "-m", "pytest"}, domain.StageProjectProcess, true, "python"},
		{"attached command", []string{"python", "-cpass", "-m", "pytest"}, domain.StageProjectProcess, true, "python"},
		{"stdin unittest argument", []string{"python", "-", "-m", "unittest"}, domain.StageProjectProcess, true, "python"},
		{"explicit script boundary", []string{"python", "--", "app.py", "-m", "mypy"}, domain.StageProjectProcess, true, "python"},
		{"explicit option boundary", []string{"python", "--", "-m", "pytest"}, domain.StageProjectProcess, true, "python"},
		{"script ruff argument", []string{"python", "app.py", "-m", "ruff", "check"}, domain.StageProjectProcess, true, "python"},
		{"unrelated module arguments", []string{"python", "-m", "app", "-m", "pytest"}, domain.StageProjectProcess, true, "python"},
		{"warning value resembles module", []string{"python", "-W", "pytest", "app.py", "-m", "pytest"}, domain.StageProjectProcess, true, "python"},
		{"warning consumes module flag", []string{"python", "-W", "-m", "pytest"}, domain.StageProjectProcess, true, "python"},
		{"missing module", []string{"python", "-m"}, domain.StageProjectProcess, true, "python"},
		{"missing warning value", []string{"python", "-W"}, domain.StageProjectProcess, true, "python"},
		{"missing implementation value", []string{"python", "-X"}, domain.StageProjectProcess, true, "python"},
		{"unknown option", []string{"python", "--unknown", "-m", "pytest"}, domain.StageProjectProcess, true, "python"},
		{"help stops execution", []string{"python", "-h", "-m", "pytest"}, domain.StageProjectProcess, true, "python"},
		{"pytest module", []string{"python", "-m", "pytest"}, domain.StageProjectTest, true, "pytest"},
		{"unittest module", []string{"python", "-m", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"warning option", []string{"python", "-W", "ignore", "-m", "pytest"}, domain.StageProjectTest, true, "pytest"},
		{"implementation option", []string{"python", "-X", "dev", "-m", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"warning and implementation options", []string{"python", "-W", "ignore", "-X", "dev", "-m", "mypy", "src"}, domain.StageProjectTypecheck, true, "mypy"},
		{"attached option values", []string{"python", "-Wignore", "-Xdev", "-m", "pytest"}, domain.StageProjectTest, true, "pytest"},
		{"grouped flags and attached module", []string{"python", "-IB", "-mpytest"}, domain.StageProjectTest, true, "pytest"},
		{"grouped module option", []string{"python", "-Im", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"hash option value", []string{"python", "--check-hash-based-pycs", "always", "-m", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"launcher version", []string{"py", "-3", "-m", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"launcher warning option", []string{"py", "-3.11", "-W", "ignore", "-m", "pytest"}, domain.StageProjectTest, true, "pytest"},
		{"launcher version tag", []string{"py", "-V:3.11", "-m", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"versioned interpreter", []string{"python3.12.exe", "-m", "mypy", "."}, domain.StageProjectTypecheck, true, "mypy"},
		{"pypy alias", []string{"pypy", "-m", "unittest"}, domain.StageProjectTest, true, "unittest"},
		{"pypy3 alias", []string{"pypy3", "-m", "pytest"}, domain.StageProjectTest, true, "pytest"},
		{"module arguments preserved", []string{"python", "-m", "ruff", "--config", "ruff.toml", "check", ".", "-m", "pytest"}, domain.StageProjectTypecheck, true, "ruff"},
		{"module arguments not reinterpreted", []string{"python", "-m", "ruff", "format", "-m", "pytest"}, "", false, "ruff"},
		{"interpreter value not pip argument", []string{"python", "-W", "install", "-m", "pip", "freeze"}, "", false, "pip"},
		{"pip module install", []string{"python", "-W", "ignore", "-m", "pip", "install", "requests"}, domain.StageUsed, true, "pip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.ClassifyCommand(tt.argv)
			if got.Stage != tt.stage || got.Known != tt.known || got.Tool != tt.tool {
				t.Errorf("ClassifyCommand(%v) = %+v, want Stage=%q Known=%v Tool=%q", tt.argv, got, tt.stage, tt.known, tt.tool)
			}
		})
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
