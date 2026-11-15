package rust

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// Cargo.lock records the whole resolved tree: every [[package]] lists the
// packages it pulled. Nothing read it, so cargo contributed no dependency
// edges at all — the network held 1,351 edges and every one of them was npm,
// because adapters/node was the only adapter implementing EdgeScanner.
//
// The tree is already in the file. It cost a parser, not a build.
func TestScanEdgesReadsTheResolvedTreeFromCargoLock(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, `
version = 3

[[package]]
name = "app"
version = "0.1.0"
dependencies = [
 "serde",
 "tokio 1.47.1",
]

[[package]]
name = "serde"
version = "1.0.219"
source = "registry+https://github.com/rust-lang/crates.io-index"
dependencies = [
 "serde_derive 1.0.219 (registry+https://github.com/rust-lang/crates.io-index)",
]

[[package]]
name = "serde_derive"
version = "1.0.219"
source = "registry+https://github.com/rust-lang/crates.io-index"

[[package]]
name = "tokio"
version = "1.47.1"
source = "registry+https://github.com/rust-lang/crates.io-index"
`)

	edges, err := Adapter{}.ScanEdges(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	// Only crates.io-to-crates.io edges belong in the reported tree: the
	// sourceless root app package must contribute no edges, while
	// serde -> serde_derive is preserved.
	assertEdgeSet(t, edges, []string{
		"serde@1.0.219 -> serde_derive@1.0.219",
	})
}

func TestScanEdgesDropsNonCratesIOParentsAndChildren(t *testing.T) {
	const crates = "registry+https://github.com/rust-lang/crates.io-index"
	dir := t.TempDir()
	writeLock(t, dir, `
version = 4

[[package]]
name = "app"
version = "0.1.0"
dependencies = [
 "pub_parent",
 "local_dep",
]

[[package]]
name = "local_dep"
version = "0.1.0"
dependencies = [
 "pub_leaf",
]

[[package]]
name = "git_parent"
version = "0.2.0"
source = "git+https://example.invalid/git_parent.git#0000000000000000000000000000000000000000"
dependencies = [
 "pub_leaf",
]

[[package]]
name = "private_registry_parent"
version = "0.3.0"
source = "registry+https://private.invalid/index"
dependencies = [
 "pub_leaf",
]

[[package]]
name = "pub_parent"
version = "1.0.0"
source = "`+crates+`"
dependencies = [
 "git_child",
 "local_dep 0.1.0",
 "private_registry_child 0.4.0 (registry+https://private.invalid/index)",
 "pub_leaf 1.2.3",
]

[[package]]
name = "git_child"
version = "0.5.0"
source = "git+https://example.invalid/git_child.git#1111111111111111111111111111111111111111"

[[package]]
name = "private_registry_child"
version = "0.4.0"
source = "registry+https://private.invalid/index"

[[package]]
name = "pub_leaf"
version = "1.2.3"
source = "`+crates+`"
`)

	edges, err := Adapter{}.ScanEdges(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	assertEdgeSet(t, edges, []string{
		"pub_parent@1.0.0 -> pub_leaf@1.2.3",
	})
}

func TestScanEdgesDisambiguatesSameNameAndVersionBySource(t *testing.T) {
	const (
		crates = "registry+https://github.com/rust-lang/crates.io-index"
		gitSrc = "git+https://example.invalid/foo.git#2222222222222222222222222222222222222222"
	)
	dir := t.TempDir()
	writeLock(t, dir, `
version = 3

[[package]]
name = "foo"
version = "1.0.0"
source = "`+crates+`"

[[package]]
name = "foo"
version = "1.0.0"
source = "`+gitSrc+`"
dependencies = [
 "git_only_dep",
]

[[package]]
name = "git_only_dep"
version = "0.1.0"
source = "`+crates+`"

[[package]]
name = "points_crates"
version = "0.1.0"
source = "`+crates+`"
dependencies = [
 "foo 1.0.0 (`+crates+`)",
]

[[package]]
name = "points_git"
version = "0.1.0"
source = "`+crates+`"
dependencies = [
 "foo 1.0.0 (`+gitSrc+`)",
]

[[package]]
name = "ambiguous_version_only"
version = "0.1.0"
source = "`+crates+`"
dependencies = [
 "foo 1.0.0",
]

[[package]]
name = "ambiguous_name_only"
version = "0.1.0"
source = "`+crates+`"
dependencies = [
 "foo",
]
`)

	edges, err := Adapter{}.ScanEdges(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	assertEdgeSet(t, edges, []string{
		"points_crates@0.1.0 -> foo@1.0.0",
	})
}

func TestScanEdgesAcceptsSparseCratesIOSourceAndRejectsLookalikes(t *testing.T) {
	const (
		sparse      = "sparse+https://index.crates.io/"
		spoofSparse = "sparse+https://index.crates.io.evil.invalid/"
	)
	dir := t.TempDir()
	writeLock(t, dir, `
version = 4

[[package]]
name = "alpha"
version = "2.0.0"
source = "`+sparse+`"
dependencies = [
 "beta",
 "gamma 1.5.0",
 "delta 0.9.0 (`+sparse+`)",
 "spoof",
 "missing_ver 9.9.9",
]

[[package]]
name = "beta"
version = "0.1.0"
source = "`+sparse+`"

[[package]]
name = "gamma"
version = "1.5.0"
source = "`+sparse+`"

[[package]]
name = "delta"
version = "0.9.0"
source = "`+sparse+`"

[[package]]
name = "spoof"
version = "0.1.0"
source = "`+spoofSparse+`"
dependencies = [
 "beta",
]
`)

	edges, err := Adapter{}.ScanEdges(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	assertEdgeSet(t, edges, []string{
		"alpha@2.0.0 -> beta@0.1.0",
		"alpha@2.0.0 -> gamma@1.5.0",
		"alpha@2.0.0 -> delta@0.9.0",
	})
}

func assertEdgeSet(t *testing.T, edges []scanner.Edge, wantKeys []string) {
	t.Helper()
	want := make(map[string]bool, len(wantKeys))
	for _, k := range wantKeys {
		want[k] = false
	}
	for _, e := range edges {
		key := e.Parent.Name + "@" + e.Parent.Version + " -> " + e.Child.Name + "@" + e.Child.Version
		seen, ok := want[key]
		if !ok {
			t.Errorf("unexpected edge %q", key)
			continue
		}
		if seen {
			t.Errorf("duplicate edge %q", key)
			continue
		}
		want[key] = true
		if e.Parent.Ecosystem != "cargo" || e.Child.Ecosystem != "cargo" {
			t.Errorf("edge %q has ecosystem %q -> %q", key, e.Parent.Ecosystem, e.Child.Ecosystem)
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("missing edge %q", key)
		}
	}
}

// A bare "name" resolves through the lockfile, which is the only place the
// version lives. A name (or version) that resolves to nothing is dropped
// rather than recorded at an invented version.
func TestScanEdgesDropsADependencyTheLockfileDoesNotResolve(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, `
[[package]]
name = "serde"
version = "1.0.219"
source = "registry+https://github.com/rust-lang/crates.io-index"
dependencies = [
 "ghost",
 "ghost_ver 1.0.0",
]
`)
	edges, err := Adapter{}.ScanEdges(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Errorf("recorded an edge to a package the lockfile never resolved: %+v", edges)
	}
}

// No lockfile is no answer, not an empty tree.
func TestScanEdgesWithoutALockfileReportsNothing(t *testing.T) {
	if _, err := (Adapter{}).ScanEdges(context.Background(), t.TempDir()); err == nil {
		t.Error("a directory with no Cargo.lock answered as though it had one")
	}
}

func writeLock(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.lock"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
