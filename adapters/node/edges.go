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
// Past the last node_modules the walk keeps climbing the directories of a
// workspace member ("apps/web", then "apps"), so a member's own copy in
// apps/web/node_modules wins over the hoisted one at the root.
//
// The nearest copy is the one that resolves even when it is a link: a
// symlink to a sibling workspace or a local folder is not a registry
// install, so the edge resolves to nothing rather than to a farther copy
// the package never loads.
//
// It returns the INSTALLED package's name beside the version. The
// dependencies key is the alias when one is in play (b: "npm:other@^1"
// installs other under node_modules/b), and an edge naming the alias names a
// package that was never installed — the same renaming the parent side of
// every edge already gets.
func resolveChild(packages map[string]npmLockEntry, parentPath, child string) (string, string) {
	key, ok := resolveChildPath(packages, parentPath, child)
	if !ok {
		return child, ""
	}
	e := packages[key]
	if e.Link {
		return child, ""
	}
	if e.Name != "" && e.Name != child {
		return e.Name, e.Version
	}
	return child, e.Version
}

// resolveChildPath is resolveChild's lookup alone: the lockfile key of the
// copy of child that the package at parentPath loads, which may be a link.
// The root's own entry has parentPath "".
func resolveChildPath(packages map[string]npmLockEntry, parentPath, child string) (string, bool) {
	for path := parentPath; path != ""; {
		key := path + "/node_modules/" + child
		if e, ok := packages[key]; ok && (e.Link || e.Version != "") {
			return key, true
		}
		if idx := strings.LastIndex(path, "/node_modules/"); idx >= 0 {
			path = path[:idx]
		} else if idx := strings.LastIndex(path, "/"); idx >= 0 && !strings.HasPrefix(path, "node_modules/") {
			path = path[:idx]
		} else {
			break
		}
	}
	key := "node_modules/" + child
	if e, ok := packages[key]; ok && !e.Link {
		return key, true
	}
	return "", false
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
		if parent == "" || e.Version == "" || e.Link {
			continue // a link is a local folder or a sibling workspace, not an install
		}
		if e.Name != "" && e.Name != parent {
			parent = e.Name // an alias installs one package under another name
		}
		children := make(map[string]struct{}, len(e.Dependencies)+len(e.OptionalDependencies))
		for child := range e.Dependencies {
			children[child] = struct{}{}
		}
		for child := range e.OptionalDependencies {
			children[child] = struct{}{}
		}
		for child := range children {
			if child == "" {
				continue
			}
			// An edge whose child is nowhere in the lockfile resolved to
			// nothing; reporting it would invent a dependency nobody
			// installed.
			name, version := resolveChild(lock.Packages, key, child)
			if version == "" {
				continue
			}
			out = append(out, lockEdge{
				Parent: parent, ParentVersion: e.Version,
				Child: name, ChildVersion: version,
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
