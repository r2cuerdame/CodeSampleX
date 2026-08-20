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
