package unreal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// An Unreal project must be recognisable as one.
//
// Measured 2026-09-01 on a minimal .uproject fixture: evidence.Scan returned
// packages=0, edges=0, and an environment with no ecosystem, no runtime, no
// language and no frameworks at all. Nothing downstream had a subject to hang
// anything on, so wrapping a UBT build in run_observed_command recorded
// nothing whatsoever -- the exit code passed through and the network learned
// no more than if the command had never run.
//
// The vocabulary for the missing subject already exists: "unreal" maps to the
// public coordinate engine/unreal. What did not exist was anything that
// produced it.
func TestAnUnrealProjectIsRecognised(t *testing.T) {
	dir := writeProject(t, `{
  "FileVersion": 3,
  "EngineAssociation": "5.5",
  "Modules": [ { "Name": "MyGame", "Type": "Runtime" } ]
}`)
	a := New()
	if !a.Detect(dir) {
		t.Fatal("a directory holding a .uproject was not detected as an Unreal project")
	}
	hints := a.EnvironmentHints(context.Background(), dir)
	if got := hints["frameworks"]; got != "unreal@5.5" {
		t.Errorf("frameworks hint = %q, want %q", got, "unreal@5.5")
	}
}

// The version is not decoration. WantedTargetFromFramework refuses a bare
// name -- it needs "<name>@<version>" and a concrete version -- so an
// EngineAssociation this adapter fails to read is an adapter that produces
// nothing usable at all.
func TestTheHintConvertsToThePublicEngineCoordinate(t *testing.T) {
	dir := writeProject(t, `{"EngineAssociation":"5.4"}`)
	hint := New().EnvironmentHints(context.Background(), dir)["frameworks"]
	p, ok := domain.WantedTargetFromFramework(hint)
	if !ok {
		t.Fatalf("the hint %q does not convert to a public target", hint)
	}
	if p.Ecosystem != "generic" || p.Name != "engine/unreal" || p.Version != "5.4" {
		t.Errorf("converted to %+v, want generic engine/unreal 5.4", p)
	}
	if !domain.IsWantedTarget(p) {
		t.Error("the coordinate is not inside the public target boundary")
	}
	// And a bare name is refused, which is why reading the version matters.
	if _, ok := domain.WantedTargetFromFramework("unreal"); ok {
		t.Error("a bare framework name converted; the version would not be needed")
	}
}

// An EngineAssociation that is not a version is common: a source build writes
// a GUID there. A hint that cannot become a coordinate is worse than none --
// it would travel as an arbitrary framework string and mean nothing.
func TestAnEngineAssociationThatIsNotAVersionProducesNoHint(t *testing.T) {
	for _, assoc := range []string{
		"{5F4E1C3A-0000-0000-0000-000000000000}", // a source build
		"",
		"   ",
	} {
		dir := writeProject(t, `{"EngineAssociation":`+quote(assoc)+`}`)
		if got := New().EnvironmentHints(context.Background(), dir)["frameworks"]; got != "" {
			t.Errorf("EngineAssociation %q produced the hint %q, want none", assoc, got)
		}
	}
}

// It claims no ecosystem and no packages. Unreal dependencies are engine
// modules and marketplace plugins, and neither has a stable public
// identifier this network could hold anyone to.
func TestTheAdapterClaimsNoPackages(t *testing.T) {
	dir := writeProject(t, `{"EngineAssociation":"5.5"}`)
	a := New()
	pkgs, err := a.ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Errorf("claimed %d packages; Unreal plugins have no stable public identifier", len(pkgs))
	}
	syms, err := a.ScanSymbols(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 0 {
		t.Errorf("claimed %d symbols", len(syms))
	}
}

// A directory with no .uproject is not an Unreal project, however much else
// it contains.
func TestAnOrdinaryDirectoryIsNotUnreal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if New().Detect(dir) {
		t.Error("a directory with no .uproject was detected as Unreal")
	}
}

func writeProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MyGame.uproject"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
