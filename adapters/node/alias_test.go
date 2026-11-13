package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// An alias installs one package under another name:
//
//	"dependencies": {"lodash": "npm:lodash-es@4.17.21"}
//	"node_modules/lodash": {"name":"lodash-es","version":"4.17.21"}
//
// The lockfile key is the IMPORT name; the entry's own "name" is the
// package that is actually installed. Reading the key alone reported
// lodash@4.17.21 — a package this project never installs, and a real one on
// npm, so the publicness probe confirmed it and observations were uploaded
// about a build that never used it, while lodash-es, the package that was
// actually built, went unreported entirely.
//
// The alias string-width-cjs -> npm:string-width@^4.2.0 reaches a large
// share of real lockfiles transitively through @isaacs/cliui.
func TestAnAliasedDependencyIsReportedAsWhatIsInstalled(t *testing.T) {
	lock := []byte(`{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"dependencies": {"lodash": "npm:lodash-es@4.17.21"}},
	    "node_modules/lodash": {
	      "name": "lodash-es",
	      "version": "4.17.21",
	      "resolved": "https://registry.npmjs.org/lodash-es/-/lodash-es-4.17.21.tgz"
	    }
	  }
	}`)

	deps, err := parsePackageLock(lock, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(deps), deps)
	}
	if deps[0].Name != "lodash-es" {
		t.Errorf("reported %q@%s, want lodash-es — the project never installs lodash",
			deps[0].Name, deps[0].Version)
	}
	if deps[0].Version != "4.17.21" {
		t.Errorf("version = %q", deps[0].Version)
	}
}

// An ordinary dependency, where the key and the package are the same thing,
// is untouched — including a scoped one, whose key contains a slash.
func TestOrdinaryDependenciesAreUnchanged(t *testing.T) {
	lock := []byte(`{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"dependencies": {"axios": "^1.12.0", "@scope/pkg": "^2.0.0"}},
	    "node_modules/axios": {"version": "1.12.2", "resolved": "https://registry.npmjs.org/axios/-/axios-1.12.2.tgz"},
	    "node_modules/@scope/pkg": {"version": "2.0.1", "resolved": "https://registry.npmjs.org/@scope/pkg/-/pkg-2.0.1.tgz"}
	  }
	}`)

	deps, err := parsePackageLock(lock, "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, d := range deps {
		byName[d.Name] = d.Version
	}
	if byName["axios"] != "1.12.2" {
		t.Errorf("axios = %q, want 1.12.2 (%+v)", byName["axios"], deps)
	}
	if byName["@scope/pkg"] != "2.0.1" {
		t.Errorf("@scope/pkg = %q, want 2.0.1 (%+v)", byName["@scope/pkg"], deps)
	}
}

func TestPnpmLockAliasesReportInstalledPackagesAndDirectness(t *testing.T) {
	const lock = `lockfileVersion: '9.0'

importers:

  .:
    dependencies:
      lodash:
        specifier: npm:lodash-es@^4.17.21
        version: lodash-es@4.17.21
      prefixed-chalk:
        specifier: npm:chalk
        version: npm:chalk@5.3.0
      '@my/alias':
        specifier: 'npm:@scope/pkg@^2.0.0'
        version: '@scope/pkg@2.0.1'
      peer-alias:
        specifier: 'npm:@scope/peer-lib@^1.0.0'
        version: '@scope/peer-lib@1.2.0(@scope/react@18.3.1)'
      priv-alias:
        specifier: file:../priv-target
        version: priv-target@1.0.0
      '@npm/types':
        specifier: ^1.2.0
        version: 1.2.0
      local-lib:
        specifier: file:../local-lib
        version: file:../local-lib
      ws-lib:
        specifier: workspace:*
        version: link:packages/ws-lib

packages:

  lodash-es@4.17.21:
    resolution: {integrity: sha512-fixture1}

  lodash-es@4.17.15:
    resolution: {integrity: sha512-fixture2}

  chalk@5.3.0:
    resolution: {integrity: sha512-fixture3}

  '@scope/pkg@2.0.1':
    resolution: {integrity: sha512-fixture4}

  '@scope/peer-lib@1.2.0':
    resolution: {integrity: sha512-fixture5}

  priv-target@1.0.0:
    resolution: {integrity: sha512-fixture6}

  priv-target@2.0.0:
    resolution: {integrity: sha512-fixture7}

  '@npm/types@1.2.0':
    resolution: {integrity: sha512-fixture8}

  string-width@4.2.3:
    resolution: {integrity: sha512-fixture9}

  local-lib@file:../local-lib:
    resolution: {directory: ../local-lib, type: directory}
    version: 1.0.0

snapshots:

  cliui@8.0.2:
    dependencies:
      string-width-cjs: string-width@4.2.3
`
	deps, err := parsePnpmLock([]byte(lock), "")
	if err != nil {
		t.Fatal(err)
	}

	type key struct{ name, version string }
	got := map[key]lockDep{}
	for _, d := range deps {
		if strings.Contains(d.Name, "@npm:") || strings.Contains(d.Version, "@") {
			t.Errorf("corrupted alias package or version in lockDep: %+v", d)
		}
		for _, forbidden := range []string{"lodash", "prefixed-chalk", "@my/alias", "peer-alias", "priv-alias", "string-width-cjs"} {
			if d.Name == forbidden {
				t.Errorf("alias name %q leaked as package entry: %+v", forbidden, d)
			}
		}
		got[key{d.Name, d.Version}] = d
	}

	want := map[key]lockDep{
		{"lodash-es", "4.17.21"}:     {Name: "lodash-es", Version: "4.17.21", Direct: true, Private: false},
		{"lodash-es", "4.17.15"}:     {Name: "lodash-es", Version: "4.17.15", Direct: false, Private: false},
		{"chalk", "5.3.0"}:           {Name: "chalk", Version: "5.3.0", Direct: true, Private: false},
		{"@scope/pkg", "2.0.1"}:      {Name: "@scope/pkg", Version: "2.0.1", Direct: true, Private: false},
		{"@scope/peer-lib", "1.2.0"}: {Name: "@scope/peer-lib", Version: "1.2.0", Direct: true, Private: false},
		{"priv-target", "1.0.0"}:     {Name: "priv-target", Version: "1.0.0", Direct: true, Private: true},
		{"priv-target", "2.0.0"}:     {Name: "priv-target", Version: "2.0.0", Direct: false, Private: false},
		{"@npm/types", "1.2.0"}:      {Name: "@npm/types", Version: "1.2.0", Direct: true, Private: false},
		{"string-width", "4.2.3"}:    {Name: "string-width", Version: "4.2.3", Direct: false, Private: false},
		{"local-lib", "1.0.0"}:       {Name: "local-lib", Version: "1.0.0", Direct: true, Private: true},
		{"ws-lib", ""}:               {Name: "ws-lib", Version: "", Direct: true, Private: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d deps, want %d:\ngot:  %+v\nwant: %+v", len(got), len(want), deps, want)
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok || g != w {
			t.Errorf("dep %v = %+v (ok=%v), want %+v", k, g, ok, w)
		}
	}
}

func TestYarnLockAliasesReportInstalledPackagesAndDirectness(t *testing.T) {
	dir := t.TempDir()
	const pj = `{
	  "dependencies": {
	    "lodash": "npm:lodash-es@^4.17.21",
	    "@my/alias": "npm:@scope/pkg@^2.0.0",
	    "no-range": "npm:chalk",
	    "@my/no-range": "npm:@scope/no-range",
	    "@npm/types": "^1.0.0",
	    "same-as-alias": "^1.0.0",
	    "alias-only": "npm:real-target@^1.0.0",
	    "direct-target": "^5.0.0",
	    "priv-alias": "file:../priv"
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pj), 0o644); err != nil {
		t.Fatal(err)
	}

	const lock = `# yarn lockfile v1

"lodash@npm:lodash-es@^4.17.21":
  version "4.17.21"
  resolved "https://registry.yarnpkg.com/lodash-es/-/lodash-es-4.17.21.tgz#f1"

lodash-es@^4.17.0:
  version "4.17.15"
  resolved "https://registry.yarnpkg.com/lodash-es/-/lodash-es-4.17.15.tgz#f2"

"@my/alias@npm:@scope/pkg@^2.0.0":
  version "2.0.1"
  resolved "https://registry.yarnpkg.com/@scope/pkg/-/pkg-2.0.1.tgz#f3"

"no-range@npm:chalk":
  version "5.3.0"
  resolved "https://registry.yarnpkg.com/chalk/-/chalk-5.3.0.tgz#f4"

"@my/no-range@npm:@scope/no-range":
  version "1.0.0"
  resolved "https://registry.yarnpkg.com/@scope/no-range/-/no-range-1.0.0.tgz#f5"

"@npm/types@^1.0.0":
  version "1.2.0"
  resolved "https://registry.yarnpkg.com/@npm/types/-/types-1.2.0.tgz#f6"

"string-width-cjs@npm:string-width@^4.2.0":
  version "4.2.3"
  resolved "https://registry.yarnpkg.com/string-width/-/string-width-4.2.3.tgz#f7"

same-as-alias@^1.0.0:
  version "1.1.0"
  resolved "https://registry.yarnpkg.com/same-as-alias/-/same-as-alias-1.1.0.tgz#f8"

"same-as-alias@npm:aliased-other@^2.0.0":
  version "2.0.0"
  resolved "https://registry.yarnpkg.com/aliased-other/-/aliased-other-2.0.0.tgz#f9"

"alias-only@npm:real-target@^1.0.0":
  version "1.0.0"
  resolved "https://registry.yarnpkg.com/real-target/-/real-target-1.0.0.tgz#f10"

alias-only@^3.0.0:
  version "3.0.0"
  resolved "https://registry.yarnpkg.com/alias-only/-/alias-only-3.0.0.tgz#f11"

direct-target@^5.0.0:
  version "5.1.0"
  resolved "https://registry.yarnpkg.com/direct-target/-/direct-target-5.1.0.tgz#f12"

"trans-alias@npm:direct-target@^4.0.0":
  version "4.2.0"
  resolved "https://registry.yarnpkg.com/direct-target/-/direct-target-4.2.0.tgz#f13"

"priv-alias@npm:priv-target@^1.0.0":
  version "1.0.0"

priv-target@^2.0.0:
  version "2.0.0"
  resolved "https://registry.yarnpkg.com/priv-target/-/priv-target-2.0.0.tgz#f14"
`
	deps, err := parseYarnLock([]byte(lock), dir)
	if err != nil {
		t.Fatal(err)
	}

	type key struct{ name, version string }
	got := map[key]lockDep{}
	for _, d := range deps {
		if strings.Contains(d.Name, "@npm:") || strings.HasPrefix(d.Name, "npm:") {
			t.Errorf("corrupted package name %q in lockDep: %+v", d.Name, d)
		}
		for _, forbidden := range []string{"lodash", "@my/alias", "no-range", "@my/no-range", "string-width-cjs", "trans-alias", "priv-alias"} {
			if d.Name == forbidden {
				t.Errorf("alias name %q leaked as package entry: %+v", forbidden, d)
			}
		}
		got[key{d.Name, d.Version}] = d
	}

	want := map[key]lockDep{
		{"lodash-es", "4.17.21"}:     {Name: "lodash-es", Version: "4.17.21", Direct: true, Private: false},
		{"lodash-es", "4.17.15"}:     {Name: "lodash-es", Version: "4.17.15", Direct: false, Private: false},
		{"@scope/pkg", "2.0.1"}:      {Name: "@scope/pkg", Version: "2.0.1", Direct: true, Private: false},
		{"chalk", "5.3.0"}:           {Name: "chalk", Version: "5.3.0", Direct: true, Private: false},
		{"@scope/no-range", "1.0.0"}: {Name: "@scope/no-range", Version: "1.0.0", Direct: true, Private: false},
		{"@npm/types", "1.2.0"}:      {Name: "@npm/types", Version: "1.2.0", Direct: true, Private: false},
		{"string-width", "4.2.3"}:    {Name: "string-width", Version: "4.2.3", Direct: false, Private: false},
		{"same-as-alias", "1.1.0"}:   {Name: "same-as-alias", Version: "1.1.0", Direct: true, Private: false},
		{"aliased-other", "2.0.0"}:   {Name: "aliased-other", Version: "2.0.0", Direct: false, Private: false},
		{"real-target", "1.0.0"}:     {Name: "real-target", Version: "1.0.0", Direct: true, Private: false},
		{"alias-only", "3.0.0"}:      {Name: "alias-only", Version: "3.0.0", Direct: false, Private: false},
		{"direct-target", "5.1.0"}:   {Name: "direct-target", Version: "5.1.0", Direct: true, Private: false},
		{"direct-target", "4.2.0"}:   {Name: "direct-target", Version: "4.2.0", Direct: false, Private: false},
		{"priv-target", "1.0.0"}:     {Name: "priv-target", Version: "1.0.0", Direct: true, Private: true},
		{"priv-target", "2.0.0"}:     {Name: "priv-target", Version: "2.0.0", Direct: false, Private: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d deps, want %d:\ngot:  %+v\nwant: %+v", len(got), len(want), deps, want)
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok || g != w {
			t.Errorf("dep %v = %+v (ok=%v), want %+v", k, g, ok, w)
		}
	}
}

func TestScanPackagesAliasesAcrossLockfiles(t *testing.T) {
	a := Adapter{}
	ctx := context.Background()

	t.Run("pnpm-lock.yaml", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"p","dependencies":{"lodash":"npm:lodash-es@^4.17.21","@npm/types":"^1.0.0"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		const lock = `lockfileVersion: '9.0'

importers:
  .:
    dependencies:
      lodash:
        specifier: npm:lodash-es@^4.17.21
        version: lodash-es@4.17.21(peer@1.0.0)
      '@npm/types':
        specifier: ^1.0.0
        version: 1.0.0

packages:
  lodash-es@4.17.21:
    resolution: {integrity: sha512-f1}
  lodash-es@4.17.15:
    resolution: {integrity: sha512-f2}
  '@npm/types@1.0.0':
    resolution: {integrity: sha512-f3}
`
		if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte(lock), 0o644); err != nil {
			t.Fatal(err)
		}
		pkgs, err := a.ScanPackages(ctx, dir)
		if err != nil {
			t.Fatalf("ScanPackages: %v", err)
		}
		if len(pkgs) != 3 {
			t.Fatalf("got %d pkgs, want 3: %+v", len(pkgs), pkgs)
		}
		for _, p := range pkgs {
			if p.Source != "pnpm-lock.yaml" || p.Publicness != scanner.PublicnessUnknown {
				t.Errorf("unexpected metadata on %+v", p)
			}
			if p.PURL.Name == "lodash" {
				t.Errorf("alias name leaked: %+v", p)
			}
		}
		byPURL := map[string]bool{}
		for _, p := range pkgs {
			byPURL[p.PURL.String()] = p.Direct
		}
		if !byPURL["pkg:npm/lodash-es@4.17.21"] {
			t.Errorf("lodash-es@4.17.21 should be direct: %+v", pkgs)
		}
		if byPURL["pkg:npm/lodash-es@4.17.15"] {
			t.Errorf("lodash-es@4.17.15 should be indirect: %+v", pkgs)
		}
		if !byPURL["pkg:npm/%40npm/types@1.0.0"] {
			t.Errorf("@npm/types@1.0.0 should be direct: %+v", pkgs)
		}
	})

	t.Run("yarn.lock", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"p","dependencies":{"lodash":"npm:lodash-es","@my/alias":"npm:@scope/pkg@^2.0.0","@npm/types":"^1.0.0"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		const lock = `# yarn lockfile v1

"lodash@npm:lodash-es":
  version "4.17.21"
  resolved "https://registry.yarnpkg.com/lodash-es/-/lodash-es-4.17.21.tgz#f1"

lodash-es@^4.17.0:
  version "4.17.15"
  resolved "https://registry.yarnpkg.com/lodash-es/-/lodash-es-4.17.15.tgz#f2"

"@my/alias@npm:@scope/pkg@^2.0.0":
  version "2.0.1"
  resolved "https://registry.yarnpkg.com/@scope/pkg/-/pkg-2.0.1.tgz#f3"

"@npm/types@^1.0.0":
  version "1.0.0"
  resolved "https://registry.yarnpkg.com/@npm/types/-/types-1.0.0.tgz#f4"

"string-width-cjs@npm:string-width@^4.2.0":
  version "4.2.3"
  resolved "https://registry.yarnpkg.com/string-width/-/string-width-4.2.3.tgz#f5"
`
		if err := os.WriteFile(filepath.Join(dir, "yarn.lock"), []byte(lock), 0o644); err != nil {
			t.Fatal(err)
		}
		pkgs, err := a.ScanPackages(ctx, dir)
		if err != nil {
			t.Fatalf("ScanPackages: %v", err)
		}
		if len(pkgs) != 5 {
			t.Fatalf("got %d pkgs, want 5: %+v", len(pkgs), pkgs)
		}
		byPURL := map[string]bool{}
		for _, p := range pkgs {
			if p.Source != "yarn.lock" || p.Publicness != scanner.PublicnessUnknown {
				t.Errorf("unexpected metadata on %+v", p)
			}
			byPURL[p.PURL.String()] = p.Direct
		}
		for purl, wantDirect := range map[string]bool{
			"pkg:npm/lodash-es@4.17.21":  true,
			"pkg:npm/lodash-es@4.17.15":  false,
			"pkg:npm/%40scope/pkg@2.0.1": true,
			"pkg:npm/%40npm/types@1.0.0": true,
			"pkg:npm/string-width@4.2.3": false,
		} {
			gotDirect, ok := byPURL[purl]
			if !ok || gotDirect != wantDirect {
				t.Errorf("%s: present=%v direct=%v, want present=true direct=%v (%+v)", purl, ok, gotDirect, wantDirect, pkgs)
			}
		}
	})
}

func TestLockfileAliasMultiSectionAndMergedHeaders(t *testing.T) {
	t.Run("pnpm multi-section alias and plain key", func(t *testing.T) {
		const lock = `lockfileVersion: '9.0'

importers:
  .:
    dependencies:
      shared-key:
        specifier: npm:aliased-pkg@^1.0.0
        version: aliased-pkg@1.2.0
    peerDependencies:
      shared-key:
        specifier: ^2.0.0
        version: 2.3.0

packages:
  aliased-pkg@1.2.0:
    resolution: {integrity: sha512-f1}
  shared-key@2.3.0:
    resolution: {integrity: sha512-f2}
`
		deps, err := parsePnpmLock([]byte(lock), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(deps) != 2 {
			t.Fatalf("got %d deps, want 2: %+v", len(deps), deps)
		}
		for _, d := range deps {
			if !d.Direct || d.Private {
				t.Errorf("expected direct public-unknown dep, got %+v", d)
			}
		}
	})

	t.Run("yarn merged header and git ssh alias range", func(t *testing.T) {
		dir := t.TempDir()
		const pj = `{
		  "dependencies": {
		    "lodash": "npm:lodash-es@^4.17.21",
		    "git-alias": "npm:@scope/git-target@git+ssh://git@github.com/org/repo.git"
		  }
		}`
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pj), 0o644); err != nil {
			t.Fatal(err)
		}
		const lock = `# yarn lockfile v1

lodash-es@^4.17.0, "lodash@npm:lodash-es@^4.17.21":
  version "4.17.21"
  resolved "https://registry.yarnpkg.com/lodash-es/-/lodash-es-4.17.21.tgz#f1"

"git-alias@npm:@scope/git-target@git+ssh://git@github.com/org/repo.git":
  version "1.0.0"
`
		deps, err := parseYarnLock([]byte(lock), dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(deps) != 2 {
			t.Fatalf("got %d deps, want 2: %+v", len(deps), deps)
		}
		byName := map[string]lockDep{}
		for _, d := range deps {
			byName[d.Name] = d
		}
		if g := byName["lodash-es"]; g != (lockDep{Name: "lodash-es", Version: "4.17.21", Direct: true, Private: false}) {
			t.Errorf("lodash-es = %+v", g)
		}
		if g := byName["@scope/git-target"]; g != (lockDep{Name: "@scope/git-target", Version: "1.0.0", Direct: true, Private: true}) {
			t.Errorf("@scope/git-target = %+v", g)
		}
	})
}
