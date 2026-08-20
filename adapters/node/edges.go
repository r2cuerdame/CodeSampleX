package node

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// lockEdge is one "this package pulled that one" relationship, as the
// lockfile records it.
//
// The child is resolved to a VERSION, not left as a name. Which copy an edge
// points at depends on nesting — node_modules/a/node_modules/b is a's own,
// node_modules/b is everyone else's — and the name alone loses precisely the
// fact worth having, which is who wanted 1.9.0 and who wanted 2.1.0.
type lockEdge struct {
	Parent        string
	ParentVersion string
	Child         string
	ChildVersion  string
}

// resolveChild applies npm's lookup: from the requiring package's own path,
// walk up through each enclosing node_modules until one holds the child.
func resolveChild(packages map[string]npmLockEntry, parentPath, child string) string {
	for path := parentPath; ; {
		if e, ok := packages[path+"/node_modules/"+child]; ok && e.Version != "" {
			return e.Version
		}
		idx := strings.LastIndex(path, "/node_modules/")
		if idx < 0 {
			break
		}
		path = path[:idx]
	}
	if e, ok := packages["node_modules/"+child]; ok {
		return e.Version
	}
	return ""
}

// parsePackageLockEdges reads who pulled what out of package-lock.json.
//
// The parser already unmarshalled this map and discarded it. Two versions of
// one library in a tree is the commonest reason a build breaks, and knowing
// only that there are two is the half of the answer nobody can act on.
//
// The ROOT entry is skipped: its dependencies are the project's own choices,
// and the project is not a public package and does not travel.
func parsePackageLockEdges(data []byte) ([]lockEdge, error) {
	var lock npmLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	if lock.Packages == nil {
		return nil, fmt.Errorf("unsupported lockfileVersion %d (no packages map)", lock.LockfileVersion)
	}
	var out []lockEdge
	for key, e := range lock.Packages {
		if key == "" || !strings.Contains(key, "node_modules/") {
			continue
		}
		idx := strings.LastIndex(key, "node_modules/")
		parent := key[idx+len("node_modules/"):]
		if parent == "" || e.Version == "" {
			continue
		}
		if e.Name != "" && e.Name != parent {
			parent = e.Name // an alias installs one package under another name
		}
		for child := range e.Dependencies {
			if child == "" {
				continue
			}
			// An edge whose child is nowhere in the lockfile resolved to
			// nothing; reporting it would invent a dependency nobody
			// installed.
			version := resolveChild(lock.Packages, key, child)
			if version == "" {
				continue
			}
			out = append(out, lockEdge{
				Parent: parent, ParentVersion: e.Version,
				Child: child, ChildVersion: version,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Parent != out[j].Parent {
			return out[i].Parent < out[j].Parent
		}
		if out[i].ParentVersion != out[j].ParentVersion {
			return out[i].ParentVersion < out[j].ParentVersion
		}
		if out[i].Child != out[j].Child {
			return out[i].Child < out[j].Child
		}
		return out[i].ChildVersion < out[j].ChildVersion
	})
	return out, nil
}
