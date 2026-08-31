package goadapter

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeModuleGoMod puts a module's own go.mod where `go mod download` leaves
// it, so the scanner reads the same bytes it would in a real workspace.
func writeModuleGoMod(t *testing.T, dir, escapedPath, version, body string) {
	t.Helper()
	at := filepath.Join(dir, ".csx-vendor", "gomod", "cache", "download",
		filepath.FromSlash(escapedPath), "@v")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(at, version+".mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeBuildList(t *testing.T, dir, body string) {
	t.Helper()
	at := filepath.Join(dir, ".csx-vendor")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(at, "go-modules.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func goEdgeKeys(t *testing.T, dir string) []string {
	t.Helper()
	edges, err := New().ScanEdges(context.Background(), dir)
	if err != nil {
		t.Fatalf("ScanEdges: %v", err)
	}
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		if e.Parent.Ecosystem != "golang" || e.Child.Ecosystem != "golang" {
			t.Errorf("edge has ecosystem %q -> %q, want golang on both ends",
				e.Parent.Ecosystem, e.Child.Ecosystem)
		}
		out = append(out, e.Parent.Name+"@"+e.Parent.Version+" -> "+e.Child.Name+"@"+e.Child.Version)
	}
	sort.Strings(out)
	return out
}

// The version on an edge must be the one that RAN, not the one that was asked
// for.
//
// This is the whole difficulty of the ecosystem. A module's go.mod records the
// version it requested; Minimal Version Selection then takes the maximum
// across the entire graph, so a module that asks for v1.2.0 is compiled and
// tested against whatever won. Reporting the requested version would name a
// dependency at a version nobody installed — the same lie ResolvedEdges
// already refuses for unresolved ranges, and it would be invisible, because
// v1.2.0 is a real version of a real module.
//
// So edge EXISTENCE comes from the requiring module's go.mod, and both
// endpoints' VERSIONS come from the build list.
func TestAnEdgeNamesTheSelectedVersionNotTheRequestedOne(t *testing.T) {
	dir := t.TempDir()
	writeBuildList(t, dir, `
{"Path":"csxprobe","Main":true}
{"Path":"example.com/app","Version":"v1.0.0"}
{"Path":"example.com/lib","Version":"v1.5.0"}
`)
	writeModuleGoMod(t, dir, "example.com/app", "v1.0.0", `
module example.com/app
go 1.22
require example.com/lib v1.2.0
`)
	writeModuleGoMod(t, dir, "example.com/lib", "v1.5.0", "module example.com/lib\ngo 1.22\n")

	got := goEdgeKeys(t, dir)
	want := "example.com/app@v1.0.0 -> example.com/lib@v1.5.0"
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %v, want exactly [%s]", got, want)
	}
}

// A require that never made it into the build list never ran.
//
// go.mod files carry requirements that the selected build does not contain:
// modules needed only by another platform's build tags, or only by the
// dependency's own tests. There is no version to name for them, and inventing
// one from the requirement would report a package that was never downloaded.
func TestARequirementOutsideTheBuildListIsNotAnEdge(t *testing.T) {
	dir := t.TempDir()
	writeBuildList(t, dir, `
{"Path":"csxprobe","Main":true}
{"Path":"example.com/app","Version":"v1.0.0"}
`)
	writeModuleGoMod(t, dir, "example.com/app", "v1.0.0", `
module example.com/app
go 1.22
require example.com/never-selected v0.1.0
`)

	if got := goEdgeKeys(t, dir); len(got) != 0 {
		t.Errorf("got %v, want no edges", got)
	}
}

// The main module is the throwaway sample wrapper, not a package anyone can
// depend on. An edge from it would put a coordinate in the map that does not
// exist on any registry.
func TestTheMainModuleIsNeverAnEndpoint(t *testing.T) {
	dir := t.TempDir()
	writeBuildList(t, dir, `
{"Path":"csxprobe","Main":true}
{"Path":"example.com/lib","Version":"v1.5.0"}
`)
	// The sample's own go.mod sits at the workspace root, exactly where a real
	// one does, and requires the package under test.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module csxprobe\ngo 1.22\nrequire example.com/lib v1.5.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeModuleGoMod(t, dir, "example.com/lib", "v1.5.0", "module example.com/lib\ngo 1.22\n")

	for _, e := range goEdgeKeys(t, dir) {
		t.Errorf("main module appeared in an edge: %s", e)
	}
}

// Module paths are case-folded into the cache with a bang, so a path with a
// capital letter is stored somewhere the naive join never looks.
//
// github.com/Microsoft/go-winio lives at github.com/!microsoft/go-winio. It is
// in this project's own build list, so getting it wrong would silently drop a
// real dependency rather than fail anything.
func TestAnUppercaseModulePathIsFoundInTheCache(t *testing.T) {
	dir := t.TempDir()
	writeBuildList(t, dir, `
{"Path":"csxprobe","Main":true}
{"Path":"github.com/Microsoft/go-winio","Version":"v0.6.2"}
{"Path":"example.com/lib","Version":"v1.5.0"}
`)
	writeModuleGoMod(t, dir, "github.com/!microsoft/go-winio", "v0.6.2", `
module github.com/Microsoft/go-winio
go 1.22
require example.com/lib v1.5.0
`)
	writeModuleGoMod(t, dir, "example.com/lib", "v1.5.0", "module example.com/lib\ngo 1.22\n")

	got := goEdgeKeys(t, dir)
	want := "github.com/Microsoft/go-winio@v0.6.2 -> example.com/lib@v1.5.0"
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %v, want exactly [%s]", got, want)
	}
}

// A replacement that changes what the module IS cannot be named by the
// declared purl, so the module is omitted rather than reported under an
// identity that did not run.
//
// This is the rule goListResolved already applies to resolved package
// versions; an edge that disagreed with it would put the two answers about the
// same module in conflict.
func TestAnIdentityChangingReplaceIsOmitted(t *testing.T) {
	dir := t.TempDir()
	writeBuildList(t, dir, `
{"Path":"csxprobe","Main":true}
{"Path":"example.com/app","Version":"v1.0.0"}
{"Path":"example.com/lib","Version":"v1.5.0","Replace":{"Path":"example.com/fork","Version":"v2.0.0"}}
`)
	writeModuleGoMod(t, dir, "example.com/app", "v1.0.0", `
module example.com/app
go 1.22
require example.com/lib v1.5.0
`)

	for _, e := range goEdgeKeys(t, dir) {
		t.Errorf("a forked module was reported under its original identity: %s", e)
	}
}

// A replacement that only moves the version keeps the identity, so it stays —
// at the version that ran.
func TestAVersionOnlyReplaceReportsTheReplacementVersion(t *testing.T) {
	dir := t.TempDir()
	writeBuildList(t, dir, `
{"Path":"csxprobe","Main":true}
{"Path":"example.com/app","Version":"v1.0.0"}
{"Path":"example.com/lib","Version":"v1.5.0","Replace":{"Path":"example.com/lib","Version":"v1.9.0"}}
`)
	writeModuleGoMod(t, dir, "example.com/app", "v1.0.0", `
module example.com/app
go 1.22
require example.com/lib v1.5.0
`)
	writeModuleGoMod(t, dir, "example.com/lib", "v1.9.0", "module example.com/lib\ngo 1.22\n")

	got := goEdgeKeys(t, dir)
	want := "example.com/app@v1.0.0 -> example.com/lib@v1.9.0"
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %v, want exactly [%s]", got, want)
	}
}

// No build list means the resolve stage never ran here. Returning an error
// rather than an empty slice keeps "nothing to read" distinct from "read it,
// there were no edges" — the caller treats an error as no contribution and
// carries on either way, but the two are not the same fact.
func TestAWorkspaceWithoutAResolveIsAnError(t *testing.T) {
	if _, err := New().ScanEdges(context.Background(), t.TempDir()); err == nil {
		t.Error("a workspace with no build list returned no error")
	}
}

// An `// indirect` require is not a direct edge, and reporting it as one is
// the exact failure ResolvedEdges exists to prevent.
//
// go.mod records indirect requirements so the build is reproducible: they are
// the flattened closure, not a statement that this module imports them. Run
// against a real bbolt workspace before this was handled, the scanner produced
// `go.etcd.io/bbolt@v1.4.3 -> github.com/davecgh/go-spew@v1.1.1`. bbolt does
// not import go-spew; testify does. The edge names the wrong parent, and it
// looks exactly like the twelve correct edges beside it.
//
// The doc comment on ResolvedEdges already refuses to derive edges from a flat
// closure for this reason. An adapter that then reports go.mod's own flattened
// closure as direct edges would smuggle the same claim back in one layer down.
func TestAnIndirectRequireIsNotADirectEdge(t *testing.T) {
	dir := t.TempDir()
	writeBuildList(t, dir, `
{"Path":"csxprobe","Main":true}
{"Path":"example.com/app","Version":"v1.0.0"}
{"Path":"example.com/direct","Version":"v1.0.0"}
{"Path":"example.com/carried","Version":"v1.0.0"}
`)
	writeModuleGoMod(t, dir, "example.com/app", "v1.0.0", `
module example.com/app
go 1.22

require example.com/direct v1.0.0

require example.com/carried v1.0.0 // indirect
`)
	writeModuleGoMod(t, dir, "example.com/direct", "v1.0.0", "module example.com/direct\ngo 1.22\n")
	writeModuleGoMod(t, dir, "example.com/carried", "v1.0.0", "module example.com/carried\ngo 1.22\n")

	got := goEdgeKeys(t, dir)
	want := "example.com/app@v1.0.0 -> example.com/direct@v1.0.0"
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %v, want exactly [%s]", got, want)
	}
}
