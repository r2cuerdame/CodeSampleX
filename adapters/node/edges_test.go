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

func TestPackageLockEdgesIncludeOptionalDependencies(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"dependencies": {"a": "^1.0.0"}},
	    "node_modules/a": {"version": "1.2.0", "optionalDependencies": {"b": "^1.0.0"}},
	    "node_modules/b": {"version": "2.1.0"},
	    "node_modules/a/node_modules/b": {"version": "1.9.0"}
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	want := []lockEdge{{Parent: "a", ParentVersion: "1.2.0", Child: "b", ChildVersion: "1.9.0"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want optional dependency edge %+v", got, want)
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

func TestNonRegistryParentsAndChildrenAreExcludedFromEdges(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {
	      "dependencies": {
	        "git-parent": "git+https://github.com/org/git-parent.git#abc",
	        "url-parent": "https://example.com/url-parent-1.0.0.tgz",
	        "a": "^1.0.0",
	        "c": "^1.0.0"
	      },
	      "devDependencies": {
	        "file-child": "file:../file-child.tgz"
	      }
	    },
	    "node_modules/git-parent": {
	      "version": "1.0.0",
	      "resolved": "git+https://github.com/org/git-parent.git#abc",
	      "dependencies": {"pub": "^1.0.0"}
	    },
	    "node_modules/url-parent": {
	      "version": "1.0.0",
	      "resolved": "https://example.com/url-parent-1.0.0.tgz",
	      "dependencies": {"pub": "^1.0.0"}
	    },
	    "node_modules/file-child": {
	      "version": "1.0.0",
	      "resolved": "file:../file-child.tgz"
	    },
	    "node_modules/pub": {"version": "1.0.0"},
	    "node_modules/fork-dep": {"version": "1.0.0"},
	    "node_modules/real-dep": {"version": "1.0.0"},
	    "node_modules/b": {
	      "version": "2.1.3",
	      "resolved": "git+https://github.com/fork/b.git#abc",
	      "dependencies": {"fork-dep": "^1.0.0"}
	    },
	    "node_modules/a": {
	      "version": "1.0.0",
	      "dependencies": {
	        "b": "^2.1.3",
	        "file-child": "^1.0.0",
	        "url-child": "https://example.com/url-child-1.0.0.tgz"
	      },
	      "optionalDependencies": {
	        "opt-git": "git+ssh://git@github.com/org/opt-git.git#def"
	      }
	    },
	    "node_modules/url-child": {"version": "1.0.0"},
	    "node_modules/opt-git": {"version": "1.0.0"},
	    "node_modules/c": {
	      "version": "1.0.0",
	      "dependencies": {"b": "^2.1.3"}
	    },
	    "node_modules/c/node_modules/b": {
	      "version": "2.1.3",
	      "resolved": "https://registry.npmjs.org/b/-/b-2.1.3.tgz",
	      "dependencies": {"real-dep": "^1.0.0"}
	    }
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	want := []lockEdge{
		{Parent: "b", ParentVersion: "2.1.3", Child: "real-dep", ChildVersion: "1.0.0"},
		{Parent: "c", ParentVersion: "1.0.0", Child: "b", ChildVersion: "2.1.3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want %+v", got, want)
	}
}

func TestNearestNonRegistryChildDoesNotFallBackToOuterRegistryCopy(t *testing.T) {
	for _, tc := range []struct {
		name, lock string
	}{
		{
			name: "workspace-url-tarball-blocks-root-registry",
			lock: `{
			  "lockfileVersion": 3,
			  "packages": {
			    "": {"workspaces": ["apps/*"]},
			    "apps/web": {"name": "web", "dependencies": {"b": "https://example.com/b-1.9.0.tgz", "a": "^1.0.0"}},
			    "node_modules/web": {"resolved": "apps/web", "link": true},
			    "apps/web/node_modules/a": {"version": "1.0.0", "dependencies": {"b": "^1.0.0"}},
			    "apps/web/node_modules/b": {"version": "1.9.0", "resolved": "https://example.com/b-1.9.0.tgz"},
			    "node_modules/b": {"version": "2.1.0", "resolved": "https://registry.npmjs.org/b/-/b-2.1.0.tgz"}
			  }
			}`,
		},
		{
			name: "nested-git-copy-blocks-hoisted-registry",
			lock: `{
			  "lockfileVersion": 3,
			  "packages": {
			    "": {},
			    "node_modules/a": {"version": "1.0.0", "dependencies": {"b": "^1.0.0"}},
			    "node_modules/a/node_modules/b": {"version": "1.9.0", "resolved": "git+https://github.com/fork/b.git#abc"},
			    "node_modules/b": {"version": "2.1.0", "resolved": "https://registry.npmjs.org/b/-/b-2.1.0.tgz"}
			  }
			}`,
		},
		{
			name: "nested-private-copy-without-version-blocks-hoisted-registry",
			lock: `{
			  "lockfileVersion": 3,
			  "packages": {
			    "": {},
			    "apps/web/node_modules/a": {"version": "1.0.0", "dependencies": {"b": "^1.0.0"}},
			    "apps/web/node_modules/b": {"resolved": "file:../b"},
			    "node_modules/b": {"version": "2.1.0", "resolved": "https://registry.npmjs.org/b/-/b-2.1.0.tgz"}
			  }
			}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePackageLockEdges([]byte(tc.lock))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("edges = %+v, want none: nearest non-registry copy must not fall back to outer registry copy", got)
			}
		})
	}
}

func TestDuplicatePackageLockEdgesAreDeduplicated(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {},
	    "node_modules/a": {"version": "1.2.0", "dependencies": {"b": "^2.0.0"}},
	    "node_modules/x": {"version": "1.0.0"},
	    "node_modules/x/node_modules/a": {"version": "1.2.0", "dependencies": {"b": "^2.0.0"}},
	    "node_modules/b": {"version": "2.1.0"}
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	want := []lockEdge{{Parent: "a", ParentVersion: "1.2.0", Child: "b", ChildVersion: "2.1.0"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want single deduplicated edge %+v", got, want)
	}
}
