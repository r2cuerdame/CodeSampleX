package node

import "testing"

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
