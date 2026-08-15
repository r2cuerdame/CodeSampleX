package goadapter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func scanGoMod(t *testing.T, src string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	pkgs, err := (&Adapter{}).ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range pkgs {
		out[p.PURL.Name] = p.PURL.Version
	}
	return out
}

// `replace old vX => new vY` applies ONLY when the build selects exactly
// vX; go ignores it otherwise. The left-hand version was parsed and thrown
// away, so a stale directive — the ordinary residue of a `go get -u` —
// was applied to whatever version the require line now names.
//
// Evidence from a build of v0.21.0 was then filed under v0.17.0, and the
// next agent asking about v0.17.0 got a HIT backed by a build that never
// used it. That is the exact failure the comment at the call site says this
// code prevents.
func TestAStaleReplaceDoesNotRenameTheVersionThatCompiled(t *testing.T) {
	got := scanGoMod(t, `module example.com/app

go 1.24

require golang.org/x/crypto v0.21.0

replace golang.org/x/crypto v0.0.0-20220314234659-1baeb1ce4c0b => golang.org/x/crypto v0.17.0
`)
	if v := got["golang.org/x/crypto"]; v != "v0.21.0" {
		t.Errorf("reported golang.org/x/crypto@%s, want v0.21.0 — the version go actually compiles", v)
	}
}

// A replace that DOES match the selected version still decides what
// compiled, which is the whole reason this code exists.
func TestAMatchingReplaceStillDecidesTheVersion(t *testing.T) {
	got := scanGoMod(t, `module example.com/app

go 1.24

require golang.org/x/net v0.17.0

replace golang.org/x/net v0.17.0 => golang.org/x/net v0.38.0
`)
	if v := got["golang.org/x/net"]; v != "v0.38.0" {
		t.Errorf("reported golang.org/x/net@%s, want v0.38.0 — the replace applies here", v)
	}
}

// A version-less left side governs every version and stays unconditional.
func TestAVersionlessReplaceAppliesToWhateverIsRequired(t *testing.T) {
	got := scanGoMod(t, `module example.com/app

go 1.24

require golang.org/x/net v0.21.0

replace golang.org/x/net => golang.org/x/net v0.38.0
`)
	if v := got["golang.org/x/net"]; v != "v0.38.0" {
		t.Errorf("reported golang.org/x/net@%s, want v0.38.0", v)
	}
}
