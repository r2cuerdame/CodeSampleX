package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func findPkg(pkgs []scanner.ResolvedPackage, name string) (scanner.ResolvedPackage, bool) {
	for _, p := range pkgs {
		if p.PURL.Name == name {
			return p, true
		}
	}
	return scanner.ResolvedPackage{}, false
}

func mustFindPkg(t *testing.T, pkgs []scanner.ResolvedPackage, name string) scanner.ResolvedPackage {
	t.Helper()
	p, ok := findPkg(pkgs, name)
	if !ok {
		t.Fatalf("package %q not found in %v", name, pkgs)
	}
	return p
}

func assertNoPublic(t *testing.T, pkgs []scanner.ResolvedPackage) {
	t.Helper()
	for _, p := range pkgs {
		if p.Publicness == scanner.PublicnessPublic {
			t.Errorf("adapter claimed PUBLIC for %s; publicness upgrades belong to the registry check", p.PURL)
		}
	}
}

func checkCommonFixture(t *testing.T, pkgs []scanner.ResolvedPackage, wantSource string) {
	t.Helper()

	ax := mustFindPkg(t, pkgs, "axios")
	if ax.PURL.Version != "1.12.0" {
		t.Errorf("axios version = %q, want 1.12.0", ax.PURL.Version)
	}
	if !ax.Direct {
		t.Error("axios should be direct")
	}
	if ax.Publicness != scanner.PublicnessUnknown {
		t.Errorf("axios publicness = %q, want UNKNOWN", ax.Publicness)
	}
	if ax.Source != wantSource {
		t.Errorf("axios source = %q, want %q", ax.Source, wantSource)
	}

	tn := mustFindPkg(t, pkgs, "@types/node")
	if tn.PURL.Version != "22.5.4" {
		t.Errorf("@types/node version = %q, want 22.5.4", tn.PURL.Version)
	}
	if !tn.Direct {
		t.Error("@types/node should be direct (devDependency)")
	}
	if tn.Publicness != scanner.PublicnessUnknown {
		t.Errorf("@types/node publicness = %q, want UNKNOWN (scoped default)", tn.Publicness)
	}

	ll := mustFindPkg(t, pkgs, "local-lib")
	if ll.Publicness != scanner.PublicnessPrivate {
		t.Errorf("local-lib publicness = %q, want PRIVATE (file: dep)", ll.Publicness)
	}
	if !ll.Direct {
		t.Error("local-lib should be direct")
	}

	fr := mustFindPkg(t, pkgs, "follow-redirects")
	if fr.PURL.Version != "1.15.6" {
		t.Errorf("follow-redirects version = %q, want 1.15.6", fr.PURL.Version)
	}
	if fr.Direct {
		t.Error("follow-redirects should be transitive")
	}
	if fr.Publicness != scanner.PublicnessUnknown {
		t.Errorf("follow-redirects publicness = %q, want UNKNOWN", fr.Publicness)
	}

	assertNoPublic(t, pkgs)
}

func TestScanPackagesPackageLock(t *testing.T) {
	a := Adapter{}
	pkgs, err := a.ScanPackages(context.Background(), "testdata/npmproj")
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	checkCommonFixture(t, pkgs, "package-lock.json")

	pfe := mustFindPkg(t, pkgs, "proxy-from-env")
	if pfe.PURL.Version != "1.1.0" {
		t.Errorf("proxy-from-env version = %q, want 1.1.0 (nested node_modules)", pfe.PURL.Version)
	}
	if pfe.Direct {
		t.Error("proxy-from-env should be transitive")
	}

	if _, ok := findPkg(pkgs, "npmproj"); ok {
		t.Error("root project must not appear as a package")
	}
	ll := mustFindPkg(t, pkgs, "local-lib")
	if ll.PURL.Version != "1.0.0" {
		t.Errorf("local-lib version = %q, want 1.0.0 (from link target entry)", ll.PURL.Version)
	}
}

func TestPackageLockOptionalAndPeerDependenciesAreDirect(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {
	      "optionalDependencies": {"optional-lib": "^1.0.0", "local-optional": "file:../optional"},
	      "peerDependencies": {"peer-lib": "^2.0.0", "local-peer": "../peer"}
	    },
	    "node_modules/optional-lib": {"version": "1.2.0", "resolved": "https://registry.npmjs.org/optional-lib/-/optional-lib-1.2.0.tgz"},
	    "node_modules/local-optional": {"version": "1.0.0", "resolved": "file:../optional"},
	    "node_modules/peer-lib": {"version": "2.1.0", "resolved": "https://registry.npmjs.org/peer-lib/-/peer-lib-2.1.0.tgz"},
	    "node_modules/local-peer": {"version": "1.0.0", "resolved": "../peer"}
	  }
	}`
	deps, err := parsePackageLock([]byte(lock), "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]lockDep{}
	for _, dep := range deps {
		byName[dep.Name] = dep
	}
	for _, name := range []string{"optional-lib", "local-optional", "peer-lib", "local-peer"} {
		if !byName[name].Direct {
			t.Errorf("%s should be direct: %+v", name, byName[name])
		}
	}
	for _, name := range []string{"local-optional", "local-peer"} {
		if !byName[name].Private {
			t.Errorf("%s should be private based on its root spec: %+v", name, byName[name])
		}
	}
}

func TestYarnLockPeerDependenciesAreDirect(t *testing.T) {
	dir := t.TempDir()
	const pj = `{
	  "devDependencies": {"dual-lib": "file:../dual"},
	  "peerDependencies": {"peer-lib": "^2.0.0", "local-peer": "../peer", "dual-lib": "^1.0.0"}
	}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pj), 0o644); err != nil {
		t.Fatal(err)
	}
	// No lock entry carries a private range or resolution, so privateness
	// can only come from the package.json specifier.
	const lock = `# yarn lockfile v1

peer-lib@^2.0.0:
  version "2.1.0"
  resolved "https://registry.yarnpkg.com/peer-lib/-/peer-lib-2.1.0.tgz#fixture1"

local-peer@^1.0.0:
  version "1.0.0"

dual-lib@^1.0.0:
  version "1.0.0"

transitive-lib@^3.0.0:
  version "3.0.0"
  resolved "https://registry.yarnpkg.com/transitive-lib/-/transitive-lib-3.0.0.tgz#fixture2"
`
	deps, err := parseYarnLock([]byte(lock), dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]lockDep{}
	for _, dep := range deps {
		byName[dep.Name] = dep
	}
	for _, name := range []string{"peer-lib", "local-peer", "dual-lib"} {
		if !byName[name].Direct {
			t.Errorf("%s should be direct: %+v", name, byName[name])
		}
	}
	for _, name := range []string{"local-peer", "dual-lib"} {
		if !byName[name].Private {
			t.Errorf("%s should be private based on its package.json spec: %+v", name, byName[name])
		}
	}
	if byName["peer-lib"].Private {
		t.Errorf("peer-lib has a registry spec and must not be private: %+v", byName["peer-lib"])
	}
	if byName["transitive-lib"].Direct {
		t.Errorf("transitive-lib is not declared in package.json: %+v", byName["transitive-lib"])
	}
}

func TestPnpmLockPeerDependenciesAreDirect(t *testing.T) {
	const lock = `lockfileVersion: '9.0'

importers:

  .:
    devDependencies:
      dual-lib:
        specifier: link:../dual
        version: link:../dual
      unlisted-lib:
        specifier: link:../unlisted
        version: link:../unlisted
    peerDependencies:
      peer-lib:
        specifier: ^2.0.0
        version: 2.1.0(react@18.3.1)
      local-peer:
        specifier: file:../peer
        version: 1.0.0
      dual-lib:
        specifier: ^1.0.0
        version: 1.0.0
      linked-peer:
        specifier: link:../linked
        version: link:../linked
      unlisted-lib:
        specifier: ^4.0.0
        version: 4.0.0

packages:

  peer-lib@2.1.0:
    resolution: {integrity: sha512-fixture1}

  local-peer@1.0.0:
    resolution: {integrity: sha512-fixture2}

  dual-lib@1.0.0:
    resolution: {integrity: sha512-fixture3}

  transitive-lib@3.0.0:
    resolution: {integrity: sha512-fixture4}
`
	deps, err := parsePnpmLock([]byte(lock), "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]lockDep{}
	for _, dep := range deps {
		byName[dep.Name] = dep
	}
	for _, name := range []string{"peer-lib", "local-peer", "dual-lib", "linked-peer", "unlisted-lib"} {
		if !byName[name].Direct {
			t.Errorf("%s should be direct: %+v", name, byName[name])
		}
	}
	for _, name := range []string{"local-peer", "dual-lib", "linked-peer", "unlisted-lib"} {
		if !byName[name].Private {
			t.Errorf("%s should be private based on its importer spec: %+v", name, byName[name])
		}
	}
	if p := byName["peer-lib"]; p.Private || p.Version != "2.1.0" {
		t.Errorf("peer-lib = %+v, want public-unknown 2.1.0", p)
	}
	if byName["linked-peer"].Version != "" {
		t.Errorf("linked-peer version = %q, want empty for a link-only importer dep", byName["linked-peer"].Version)
	}
	// Absent from packages and listed as a link before its peer range: the
	// registry version survives regardless of section order.
	if v := byName["unlisted-lib"].Version; v != "4.0.0" {
		t.Errorf("unlisted-lib version = %q, want 4.0.0 from its peer entry", v)
	}
	if byName["transitive-lib"].Direct {
		t.Errorf("transitive-lib is not an importer dependency: %+v", byName["transitive-lib"])
	}
}

func TestScanPackagesPnpmLock(t *testing.T) {
	a := Adapter{}
	pkgs, err := a.ScanPackages(context.Background(), "testdata/pnpmproj")
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	checkCommonFixture(t, pkgs, "pnpm-lock.yaml")

	ll := mustFindPkg(t, pkgs, "local-lib")
	if ll.PURL.Version != "1.0.0" {
		t.Errorf("local-lib version = %q, want 1.0.0 (version field of directory dep)", ll.PURL.Version)
	}
}

func TestScanPackagesYarnLock(t *testing.T) {
	a := Adapter{}
	pkgs, err := a.ScanPackages(context.Background(), "testdata/yarnproj")
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	checkCommonFixture(t, pkgs, "yarn.lock")

	ll := mustFindPkg(t, pkgs, "local-lib")
	if ll.PURL.Version != "1.0.0" {
		t.Errorf("local-lib version = %q, want 1.0.0", ll.PURL.Version)
	}
}

func TestScanPackagesNoLockfile(t *testing.T) {
	a := Adapter{}
	pkgs, err := a.ScanPackages(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("ScanPackages on empty dir: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected no packages without a lockfile, got %v", pkgs)
	}
}

func TestIsPrivateSpec(t *testing.T) {
	private := []string{
		"file:../local-lib", "link:../x", "portal:../x", "workspace:*",
		"git+ssh://git@github.com/a/b.git", "git://github.com/a/b.git",
		"github:a/b", "../local-lib", "./x", "/abs/path", "C:\\proj\\x",
	}
	for _, s := range private {
		if !isPrivateSpec(s) {
			t.Errorf("isPrivateSpec(%q) = false, want true", s)
		}
	}
	public := []string{
		"^1.12.0", "1.12.0", "~2.0.0", "*",
		"https://registry.npmjs.org/axios/-/axios-1.12.0.tgz",
	}
	for _, s := range public {
		if isPrivateSpec(s) {
			t.Errorf("isPrivateSpec(%q) = true, want false", s)
		}
	}
}
