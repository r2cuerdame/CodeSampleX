package scanner_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/adapters"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// The edge scanner is reached by a type assertion, which fails silently: an
// adapter whose method has the wrong receiver, or that is registered by a
// type the assertion does not match, simply contributes no edges and looks
// exactly like an ecosystem with no dependencies. This proves the wiring by
// running a real scan over a real lockfile.
func TestARealNpmScanProducesEdges(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"app","version":"1.0.0","dependencies":{"a":"^1.0.0"}}`)
	write("package-lock.json", `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"dependencies": {"a": "^1.0.0"}},
	    "node_modules/a": {"version": "1.2.0", "dependencies": {"b": "^2.0.0"}},
	    "node_modules/b": {"version": "2.1.0"}
	  }
	}`)

	res, err := scanner.Scan(t.Context(), dir, adapters.Detect(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Edges) == 0 {
		t.Fatal("a real npm lockfile produced no edges: the EdgeScanner assertion never matched")
	}
	found := false
	for _, e := range res.Edges {
		if e.Parent.Name == "a" && e.Parent.Version == "1.2.0" &&
			e.Child.Name == "b" && e.Child.Version == "2.1.0" {
			found = true
		}
	}
	if !found {
		t.Errorf("edges = %+v, want a@1.2.0 -> b@2.1.0", res.Edges)
	}
}

// Same proof for the two ecosystems that had no edge scanner at all. The
// network held 1,351 dependency edges and every one of them was npm, so
// "which package pulled this version of that library" simply had no answer
// outside one ecosystem.
func TestARealCargoScanProducesEdges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"app\"\nversion = \"0.1.0\"\n\n[dependencies]\nserde = \"1\"\n")
	writeFile(t, dir, "Cargo.lock", `
[[package]]
name = "app"
version = "0.1.0"
dependencies = [
 "serde 1.0.219",
]

[[package]]
name = "serde"
version = "1.0.219"
source = "registry+https://github.com/rust-lang/crates.io-index"
`)
	assertEdge(t, dir, "app", "0.1.0", "serde", "1.0.219")
}

func TestARealUvScanProducesEdges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"app\"\nversion = \"0.1.0\"\n")
	writeFile(t, dir, "uv.lock", `
version = 1

[[package]]
name = "app"
version = "0.1.0"
dependencies = [
    { name = "jinja2" },
]

[[package]]
name = "jinja2"
version = "3.1.6"
`)
	assertEdge(t, dir, "app", "0.1.0", "jinja2", "3.1.6")
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertEdge(t *testing.T, dir, pName, pVer, cName, cVer string) {
	t.Helper()
	res, err := scanner.Scan(t.Context(), dir, adapters.Detect(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Edges) == 0 {
		t.Fatal("a real lockfile produced no edges: the EdgeScanner assertion never matched")
	}
	for _, e := range res.Edges {
		if e.Parent.Name == pName && e.Parent.Version == pVer &&
			e.Child.Name == cName && e.Child.Version == cVer {
			return
		}
	}
	t.Errorf("edges = %+v, want %s@%s -> %s@%s", res.Edges, pName, pVer, cName, cVer)
}
