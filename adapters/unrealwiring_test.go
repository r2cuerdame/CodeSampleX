package adapters

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// The adapter has to be in the registry, or it recognises nothing.
//
// An adapter that exists and is never wired is the failure this project met
// three times on 2026-09-01 alone: a rehydrate that shipped and never ran, a
// Defender check documented as done and invoked by nothing, a launcher fix
// that reached a release page and stopped. So the wiring is asserted rather
// than assumed.
func TestAnUnrealProjectIsDetectedThroughTheRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MyGame.uproject"),
		[]byte(`{"EngineAssociation":"5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	found := Detect(dir)
	if len(found) == 0 {
		t.Fatal("Detect found no adapter for a .uproject directory")
	}

	res, err := scanner.Scan(context.Background(), dir, found, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The whole point: the scan now produces a subject where it produced none.
	var descriptor string
	for _, f := range res.Env.Frameworks {
		if _, ok := domain.WantedTargetFromFramework(f); ok {
			descriptor = f
		}
	}
	if descriptor == "" {
		t.Fatalf("the scan named no public target; frameworks=%v", res.Env.Frameworks)
	}
	p, _ := domain.WantedTargetFromFramework(descriptor)
	if p.Name != "engine/unreal" || p.Version != "5.5" {
		t.Errorf("scan produced %+v, want engine/unreal 5.5", p)
	}
	// And it still claims no packages, so nothing invents coordinates.
	if len(res.Packages) != 0 {
		t.Errorf("the scan claimed %d packages for an Unreal project", len(res.Packages))
	}
}

// It must not change what any other project reports. The adapter runs on
// every scan, and a hint it emitted anywhere else would land in an unrelated
// project's environment.
func TestTheUnrealAdapterIsSilentEverywhereElse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"x","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, a := range All() {
		if a.Detect(dir) && a.Ecosystem() == "generic" {
			t.Errorf("the generic adapter claimed an npm project")
		}
	}
	res, err := scanner.Scan(context.Background(), dir, Detect(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Env.Frameworks {
		if _, ok := domain.WantedTargetFromFramework(f); ok {
			t.Errorf("an npm project reported the engine target %q", f)
		}
	}
}
