package python_test

import (
	"context"
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
