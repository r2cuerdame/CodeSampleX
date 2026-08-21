package node

import (
	"reflect"
	"testing"
)

// The lockfile says who pulled what and the parser already reads it: every
// npmLockEntry carries its own dependencies map, and it was unmarshalled and
// thrown away. Two versions of one library in a tree is the commonest reason
// a build breaks, and "there are two" is half an answer — the half nobody can
// act on. The other half is which parent wanted which.
func TestPackageLockEdgesNameTheParentOfEachDependency(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"dependencies": {"a": "^1.0.0"}},
	    "node_modules/a": {"version": "1.2.0", "dependencies": {"b": "^2.0.0"}},
	    "node_modules/b": {"version": "2.1.0"},
	    "node_modules/a/node_modules/b": {"version": "1.9.0"}
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	// a@1.2.0 declares b, and node_modules/a/node_modules/b is the copy IT
	// resolved to — not the top-level 2.1.0 everyone else gets. Reporting the
	// name alone would lose exactly the fact worth having.
	want := []lockEdge{{Parent: "a", ParentVersion: "1.2.0", Child: "b", ChildVersion: "1.9.0"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want %+v", got, want)
	}
}

// A root entry's dependencies are the project's own choices, not a package's,
// and the project is not something that travels.
func TestRootDependenciesAreNotEdges(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"dependencies": {"a": "^1.0.0"}},
	    "node_modules/a": {"version": "1.2.0"}
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("edges = %+v, want none: the root is the project", got)
	}
}

// Nothing nested means the child resolves to the top-level copy, which is
// npm's rule: walk up from the parent's own path until a node_modules holds
// it.
func TestAnUnnestedChildResolvesToTheTopLevelCopy(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"dependencies": {"a": "^1.0.0"}},
	    "node_modules/a": {"version": "1.2.0", "dependencies": {"b": "^2.0.0"}},
	    "node_modules/b": {"version": "2.1.0"}
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	want := []lockEdge{{Parent: "a", ParentVersion: "1.2.0", Child: "b", ChildVersion: "2.1.0"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want %+v", got, want)
	}
}

// An alias installs one package under another name, and the dependencies key
// is the ALIAS. The parent side already renames to the installed package; an
// edge's child must too, or it names a package that was never installed —
// fabricated public evidence for a name the build never used, and rows that
// never join with the same package's parent-side edges.
func TestEdgeChildResolvesAnAliasToTheInstalledPackage(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"dependencies": {"a": "^1.0.0"}},
	    "node_modules/a": {"version": "1.2.0", "dependencies": {"b": "npm:other@^1"}},
	    "node_modules/b": {"name": "other", "version": "1.9.0"}
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	want := []lockEdge{{Parent: "a", ParentVersion: "1.2.0", Child: "other", ChildVersion: "1.9.0"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want the installed package, not the alias: %+v", got, want)
	}
}

// An edge whose child is nowhere in the lockfile resolved to nothing, and
// reporting a dependency nobody installed would be inventing one.
func TestAnUnresolvableChildIsDropped(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {},
	    "node_modules/a": {"version": "1.2.0", "dependencies": {"ghost": "^1.0.0"}}
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("edges = %+v, want none", got)
	}
}

// A lockfile with no packages map is a version this parser does not read, and
// guessing edges from a manifest range would report a dependency nobody
// resolved.
func TestUnsupportedLockfileYieldsNoEdges(t *testing.T) {
	if _, err := parsePackageLockEdges([]byte(`{"lockfileVersion":1}`)); err == nil {
		t.Error("an unsupported lockfile version was accepted")
	}
}
