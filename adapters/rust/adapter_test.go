package rust

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "simple"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestEcosystemAndCapabilities(t *testing.T) {
	var a scanner.Adapter = New()
	if got := a.Ecosystem(); got != "cargo" {
		t.Errorf("Ecosystem() = %q, want %q", got, "cargo")
	}
	caps := a.Capabilities()
	want := []string{"A0", "A1", "A2"}
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
	a := New()
	if !a.Detect(fixtureDir(t)) {
		t.Error("Detect(fixture) = false, want true")
	}
	if a.Detect(t.TempDir()) {
		t.Error("Detect(empty dir) = true, want false")
	}
}

func TestScanPackages(t *testing.T) {
	a := New()
	pkgs, err := a.ScanPackages(context.Background(), fixtureDir(t))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]scanner.ResolvedPackage{}
	for _, p := range pkgs {
		if p.PURL.Ecosystem != "cargo" {
			t.Errorf("%s: ecosystem = %q, want cargo", p.PURL.Name, p.PURL.Ecosystem)
		}
		if p.Source != "Cargo.lock" {
			t.Errorf("%s: source = %q, want Cargo.lock", p.PURL.Name, p.Source)
		}
		byName[p.PURL.Name] = p
	}
	if len(pkgs) != 5 {
		t.Errorf("got %d packages, want 5: %v", len(pkgs), byName)
	}
	if _, ok := byName["fixture-app"]; ok {
		t.Error("root package fixture-app must not be reported")
	}
	tests := []struct {
		name, version, publicness string
		direct                    bool
	}{
		{"serde", "1.0.219", scanner.PublicnessUnknown, true},
		{"serde_derive", "1.0.219", scanner.PublicnessUnknown, false},
		{"serde_json", "1.0.140", scanner.PublicnessUnknown, true},
		{"local-helper", "0.1.0", scanner.PublicnessPrivate, true},
		{"gitdep", "0.2.0", scanner.PublicnessPrivate, true},
	}
	for _, tt := range tests {
		p, ok := byName[tt.name]
		if !ok {
			t.Errorf("missing package %q", tt.name)
			continue
		}
		if p.PURL.Version != tt.version {
			t.Errorf("%s: version = %q, want %q", tt.name, p.PURL.Version, tt.version)
		}
		if p.Publicness != tt.publicness {
			t.Errorf("%s: publicness = %q, want %q", tt.name, p.Publicness, tt.publicness)
		}
		if p.Direct != tt.direct {
			t.Errorf("%s: direct = %v, want %v", tt.name, p.Direct, tt.direct)
		}
	}
}

func TestScanPackagesNoLockfile(t *testing.T) {
	a := New()
	pkgs, err := a.ScanPackages(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Errorf("got %d packages without a lockfile, want 0", len(pkgs))
	}
}

func TestClassifyCommand(t *testing.T) {
	a := New()
	tests := []struct {
		argv  []string
		stage domain.Stage
		known bool
	}{
		{[]string{"cargo", "build", "--release"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "check"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "clippy"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "+nightly", "build"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "test"}, domain.StageProjectTest, true},
		{[]string{`C:\tools\cargo.EXE`, "test"}, domain.StageProjectTest, true},
		{[]string{"cargo", "run"}, domain.StageProjectProcess, true},
		{[]string{"rustc", "main.rs"}, domain.StageProjectCompile, true},
		{[]string{"cargo", "fmt"}, "", false},
		{[]string{"npm", "test"}, "", false},
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
	a := New()
	hints := a.EnvironmentHints(context.Background(), fixtureDir(t))
	want := map[string]string{
		"ecosystem":      "cargo",
		"runtime":        "",
		"language":       "rust",
		"packageManager": "cargo",
		"moduleSystem":   "",
	}
	if len(hints) != len(want) {
		t.Errorf("hints = %v, want %v", hints, want)
	}
	for k, v := range want {
		got, ok := hints[k]
		if !ok || got != v {
			t.Errorf("hints[%q] = %q (present=%v), want %q", k, got, ok, v)
		}
	}
}
