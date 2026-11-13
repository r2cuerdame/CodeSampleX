package node

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// npmMemberTreeLock is npmMonorepoLock with a tree under it: each axios pulls
// follow-redirects, and apps/web carries its own newer copy, which its axios
// must resolve to instead of the hoisted one admin's axios gets.
const npmMemberTreeLock = `{"name":"mono","lockfileVersion":3,"packages":{
 "":{"name":"mono","workspaces":["apps/*","packages/*"],"devDependencies":{"turbo":"^2.0.0"}},
 "apps/web":{"name":"web","dependencies":{"axios":"^1.12.0","@repo/ui":"*","local-lib":"file:../../vendor/local-lib"}},
 "apps/admin":{"name":"admin","dependencies":{"lodash":"^4.17.21","axios":"^0.27.0"}},
 "packages/ui":{"name":"@repo/ui","version":"0.0.0","dependencies":{"lodash":"^4.17.21"}},
 "vendor/local-lib":{"name":"local-lib","version":"1.0.0"},
 "node_modules/web":{"resolved":"apps/web","link":true},
 "node_modules/admin":{"resolved":"apps/admin","link":true},
 "node_modules/@repo/ui":{"resolved":"packages/ui","link":true},
 "apps/web/node_modules/local-lib":{"resolved":"vendor/local-lib","link":true},
 "node_modules/turbo":{"version":"2.1.0","dev":true},
 "node_modules/lodash":{"version":"4.17.21"},
 "node_modules/follow-redirects":{"version":"1.15.6"},
 "node_modules/axios":{"version":"0.27.2","dependencies":{"follow-redirects":"^1.14.9"}},
 "apps/web/node_modules/axios":{"version":"1.12.0","dependencies":{"follow-redirects":"^1.15.6"}},
 "apps/web/node_modules/follow-redirects":{"version":"1.15.9"}
}}`

func scanEdgeKeys(t *testing.T, dir string) []string {
	t.Helper()
	edges, err := (Adapter{}).ScanEdges(context.Background(), dir)
	if err != nil {
		t.Fatalf("ScanEdges(%s): %v", dir, err)
	}
	var out []string
	for _, e := range edges {
		out = append(out, e.Parent.String()+" -> "+e.Child.String())
	}
	sort.Strings(out)
	return out
}

func scanLeafKeys(t *testing.T, dir string) []string {
	t.Helper()
	leaves, err := (Adapter{}).ScanNoDependencies(context.Background(), dir)
	if err != nil {
		t.Fatalf("ScanNoDependencies(%s): %v", dir, err)
	}
	var out []string
	for _, p := range leaves {
		out = append(out, p.String())
	}
	return out
}

// A member folder has no lockfile of its own; the tree and the leaves come
// from the root's, the same file its packages already do, and the member's
// own copy wins over the hoisted one.
func TestWorkspaceMemberReadsRootLockfileTree(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, monorepoFiles(map[string]string{
		"package.json":      rootWorkspacesJSON,
		"package-lock.json": npmMemberTreeLock,
	}))
	wantEdges := []string{
		"pkg:npm/axios@0.27.2 -> pkg:npm/follow-redirects@1.15.6",
		"pkg:npm/axios@1.12.0 -> pkg:npm/follow-redirects@1.15.9",
	}
	wantLeaves := []string{
		"pkg:npm/follow-redirects@1.15.6",
		"pkg:npm/follow-redirects@1.15.9",
		"pkg:npm/lodash@4.17.21",
		"pkg:npm/turbo@2.1.0",
	}
	for _, dir := range []string{filepath.Join(root, "apps", "web"), root} {
		if got := scanEdgeKeys(t, dir); !reflect.DeepEqual(got, wantEdges) {
			t.Errorf("%s edges = %v, want %v", dir, got, wantEdges)
		}
		if got := scanLeafKeys(t, dir); !reflect.DeepEqual(got, wantLeaves) {
			t.Errorf("%s leaves = %v, want %v", dir, got, wantLeaves)
		}
	}
}

func TestWorkspaceMemberTreeBoundaries(t *testing.T) {
	noTree := func(t *testing.T, dir string) {
		t.Helper()
		edges, edgeErr := (Adapter{}).ScanEdges(context.Background(), dir)
		leaves, leafErr := (Adapter{}).ScanNoDependencies(context.Background(), dir)
		if len(edges) != 0 || len(leaves) != 0 {
			t.Errorf("%s borrowed the root tree: edges=%v (%v) leaves=%v (%v)", dir, edges, edgeErr, leaves, leafErr)
		}
	}
	t.Run("undeclared folder", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, monorepoFiles(map[string]string{
			"package.json":      rootWorkspacesJSON,
			"package-lock.json": npmMemberTreeLock,
		}))
		noTree(t, filepath.Join(root, "tools", "script"))
	})
	t.Run("negated pattern", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, monorepoFiles(map[string]string{
			"package.json":      `{"workspaces":["apps/*","!apps/web"]}`,
			"package-lock.json": npmMemberTreeLock,
		}))
		noTree(t, filepath.Join(root, "apps", "web"))
	})
	t.Run("stale lockfile", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, monorepoFiles(map[string]string{
			"package.json": rootWorkspacesJSON,
			"package-lock.json": `{"lockfileVersion":3,"packages":{"":{},
				"node_modules/axios":{"version":"1.12.0","dependencies":{"follow-redirects":"^1.15.6"}},
				"node_modules/follow-redirects":{"version":"1.15.9"}}}`,
		}))
		noTree(t, filepath.Join(root, "apps", "web"))
	})
	t.Run("member with another lockfile", func(t *testing.T) {
		// Its own install is pnpm's, which this reader does not parse; the
		// root's package-lock.json describes a different install.
		root := t.TempDir()
		writeTree(t, root, monorepoFiles(map[string]string{
			"package.json":            rootWorkspacesJSON,
			"package-lock.json":       npmMemberTreeLock,
			"apps/web/pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		}))
		noTree(t, filepath.Join(root, "apps", "web"))
	})
	t.Run("root with another lockfile", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, monorepoFiles(map[string]string{
			"package.json": rootWorkspacesJSON,
			"yarn.lock":    yarnMonorepoLock,
		}))
		noTree(t, filepath.Join(root, "apps", "web"))
	})
	t.Run("member own lockfile wins", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, monorepoFiles(map[string]string{
			"package.json":      rootWorkspacesJSON,
			"package-lock.json": npmMemberTreeLock,
			"apps/web/package-lock.json": `{"lockfileVersion":3,"packages":{
				"":{"dependencies":{"own-lib":"^1.0.0"}},
				"node_modules/own-lib":{"version":"1.0.0"}}}`,
		}))
		web := filepath.Join(root, "apps", "web")
		if got := scanEdgeKeys(t, web); len(got) != 0 {
			t.Errorf("own lockfile edges = %v, want none", got)
		}
		if got, want := scanLeafKeys(t, web), []string{"pkg:npm/own-lib@1.0.0"}; !reflect.DeepEqual(got, want) {
			t.Errorf("own lockfile leaves = %v, want %v", got, want)
		}
	})
}

// A member nested below another member resolves through each enclosing
// folder's node_modules before the root's, as Node does.
func TestEdgeChildResolvesThroughEnclosingWorkspaceFolders(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {},
	    "apps/web/packages/x/node_modules/a": {"version": "1.0.0", "dependencies": {"b": "^1.0.0"}},
	    "apps/web/node_modules/b": {"version": "1.9.0"},
	    "node_modules/b": {"version": "2.1.0"}
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	want := []lockEdge{{Parent: "a", ParentVersion: "1.0.0", Child: "b", ChildVersion: "1.9.0"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want the enclosing member's copy %+v", got, want)
	}
}

// A link is a sibling workspace or a local folder. It is never a parent, and
// as the nearest copy of a child it resolves to nothing — not to a farther
// registry copy the package never loads.
func TestLinkEntriesAreNotEdgeEnds(t *testing.T) {
	const lock = `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {},
	    "vendor/ms": {"name": "ms", "version": "2.1.3"},
	    "node_modules/linked": {"version": "1.0.0", "resolved": "packages/linked", "link": true, "dependencies": {"b": "^2.0.0"}},
	    "apps/web/node_modules/ms": {"resolved": "vendor/ms", "link": true},
	    "apps/web/node_modules/debug": {"version": "4.3.4", "dependencies": {"ms": "^2.1.2"}},
	    "node_modules/c": {"version": "1.0.0", "dependencies": {"root-link": "^1.0.0"}},
	    "node_modules/root-link": {"version": "1.0.0", "resolved": "packages/root-link", "link": true},
	    "node_modules/ms": {"version": "2.1.2"},
	    "node_modules/b": {"version": "2.1.0"}
	  }
	}`
	got, err := parsePackageLockEdges([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("edges = %+v, want none: every edge here ends at a link", got)
	}
}
