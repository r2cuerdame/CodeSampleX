package rust

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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

	// A dependency entry is "name", "name version", or "name version
	// (source)" — all three appear in one real lockfile, and the version is
	// the part that decides which package was actually pulled.
	want := map[string]bool{
		"app@0.1.0 -> serde@1.0.219":            false,
		"app@0.1.0 -> tokio@1.47.1":             false,
		"serde@1.0.219 -> serde_derive@1.0.219": false,
	}
	for _, e := range edges {
		key := e.Parent.Name + "@" + e.Parent.Version + " -> " + e.Child.Name + "@" + e.Child.Version
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected edge %q", key)
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
// version lives. A name that resolves to nothing is dropped rather than
// recorded at an invented version.
func TestScanEdgesDropsADependencyTheLockfileDoesNotResolve(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, `
[[package]]
name = "app"
version = "0.1.0"
dependencies = [
 "ghost",
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
