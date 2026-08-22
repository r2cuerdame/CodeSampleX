package python

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func edgeKeys(edges []edgePair) map[string]bool {
	out := map[string]bool{}
	for _, e := range edges {
		out[e.parent+" -> "+e.child] = true
	}
	return out
}

type edgePair struct{ parent, child string }

func scanEdgeKeys(t *testing.T, dir string) map[string]bool {
	t.Helper()
	edges, err := Adapter{}.ScanEdges(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	pairs := make([]edgePair, 0, len(edges))
	for _, e := range edges {
		if e.Parent.Ecosystem != "pypi" || e.Child.Ecosystem != "pypi" {
			t.Errorf("edge has ecosystem %q -> %q", e.Parent.Ecosystem, e.Child.Ecosystem)
		}
		pairs = append(pairs, edgePair{
			e.Parent.Name + "@" + e.Parent.Version,
			e.Child.Name + "@" + e.Child.Version,
		})
	}
	return edgeKeys(pairs)
}

// uv.lock writes each package's dependencies as inline tables naming the
// package but not its version — the version lives in that package's own
// [[package]] block, which is the only place it can be resolved from.
//
// Nothing read any of it. The network held 1,351 dependency edges and every
// one of them was npm, because adapters/node was the only adapter
// implementing EdgeScanner.
func TestScanEdgesReadsTheTreeFromUvLock(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "uv.lock", `
version = 1

[[package]]
name = "app"
version = "0.1.0"
dependencies = [
    { name = "jinja2" },
    { name = "requests" },
]

[[package]]
name = "jinja2"
version = "3.1.6"
dependencies = [
    { name = "MarkupSafe" },
]

[[package]]
name = "markupsafe"
version = "3.0.3"

[[package]]
name = "requests"
version = "2.32.3"
`)

	got := scanEdgeKeys(t, dir)
	for _, want := range []string{
		"app@0.1.0 -> jinja2@3.1.6",
		"app@0.1.0 -> requests@2.32.3",
		// PEP 503: MarkupSafe and markupsafe are one package.
		"jinja2@3.1.6 -> markupsafe@3.0.3",
	} {
		if !got[want] {
			t.Errorf("missing edge %q; got %v", want, got)
		}
	}
}

// poetry.lock puts them in a [package.dependencies] sub-table, and what it
// records there is a CONSTRAINT, not a resolved version. The version comes
// from the depended-on package's own block.
func TestScanEdgesReadsTheTreeFromPoetryLock(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "poetry.lock", `
[[package]]
name = "jinja2"
version = "3.1.6"

[package.dependencies]
MarkupSafe = ">=2.0"

[[package]]
name = "markupsafe"
version = "3.0.3"
`)

	got := scanEdgeKeys(t, dir)
	if !got["jinja2@3.1.6 -> markupsafe@3.0.3"] {
		t.Errorf("missing the poetry edge; got %v", got)
	}
	// The constraint string is not a version and must never be recorded as one.
	for key := range got {
		if key == "jinja2@3.1.6 -> markupsafe@>=2.0" {
			t.Error("recorded a constraint as a resolved version")
		}
	}
}

// A dependency the lockfile does not resolve has no version here, and an
// invented one would be worse than no edge.
func TestScanEdgesDropsAnUnresolvedPythonDependency(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "uv.lock", `
[[package]]
name = "app"
version = "0.1.0"
dependencies = [
    { name = "ghost" },
]
`)
	if got := scanEdgeKeys(t, dir); len(got) != 0 {
		t.Errorf("recorded an edge the lockfile never resolved: %v", got)
	}
}

// requirements.txt is a flat list. It has no tree, and reporting none is the
// honest answer rather than inventing one.
func TestScanEdgesHasNothingToSayAboutRequirementsTxt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "requirements.txt", "jinja2==3.1.6\nmarkupsafe==3.0.3\n")
	if _, err := (Adapter{}).ScanEdges(context.Background(), dir); err == nil {
		t.Error("a flat requirements.txt answered as though it recorded a tree")
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// An extra nobody selected is not a dependency the resolution made.
//
// uv writes optional dependencies in their own sub-table, and the scan read
// every inline { name = "..." } in the whole [[package]] block — so an
// unselected extra was published as a resolved edge. Verified against a real
// uv 0.12.1 lockfile: requests came back depending on pysocks because a
// "socks" extra existed, not because anything asked for it.
func TestScanEdgesIgnoresExtrasNobodySelected(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "uv.lock", `
version = 1

[[package]]
name = "requests"
version = "2.34.2"
dependencies = [
    { name = "idna" },
]

[package.optional-dependencies]
socks = [
    { name = "pysocks" },
]

[[package]]
name = "idna"
version = "3.19"

[[package]]
name = "pysocks"
version = "1.7.1"
`)

	got := scanEdgeKeys(t, dir)
	if !got["requests@2.34.2 -> idna@3.19"] {
		t.Errorf("lost the real dependency; got %v", got)
	}
	if got["requests@2.34.2 -> pysocks@1.7.1"] {
		t.Errorf("published an unselected extra as a resolved edge; got %v", got)
	}
}

// Development dependencies are not what the resolution installed either.
func TestScanEdgesIgnoresDevDependencies(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "uv.lock", `
[[package]]
name = "app"
version = "0.1.0"
dependencies = [
    { name = "idna" },
]

[package.dev-dependencies]
dev = [
    { name = "pytest" },
]

[[package]]
name = "idna"
version = "3.19"

[[package]]
name = "pytest"
version = "8.4.2"
`)
	got := scanEdgeKeys(t, dir)
	if got["app@0.1.0 -> pytest@8.4.2"] {
		t.Errorf("published a dev dependency as a resolved edge; got %v", got)
	}
	if !got["app@0.1.0 -> idna@3.19"] {
		t.Errorf("lost the real dependency; got %v", got)
	}
}
