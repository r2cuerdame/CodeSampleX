package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// ScanEdges reports the dependency tree from package-lock.json.
//
// Only package-lock.json for now: pnpm and yarn record the tree too, in their
// own shapes, and each is its own parser. An ecosystem that cannot answer
// contributes nothing rather than a guess.
//
// Publicness is not decided here. Every edge is returned and the caller drops
// the ones whose ends are not public, the same way it does for packages —
// deciding it twice in two places is how the two answers drift apart.
func (Adapter) ScanEdges(_ context.Context, dir string) ([]scanner.Edge, error) {
	data, member, err := readPackageLock(dir)
	if err != nil {
		return nil, err
	}
	if !lockHasMember(data, member) {
		return nil, nil // a stale lockfile that predates this member proves nothing about it
	}
	lockEdges, err := parsePackageLockEdges(data)
	if err != nil {
		return nil, err
	}
	out := make([]scanner.Edge, 0, len(lockEdges))
	for _, e := range lockEdges {
		out = append(out, scanner.Edge{
			Parent: domain.PURL{Ecosystem: "npm", Name: e.Parent, Version: e.ParentVersion},
			Child:  domain.PURL{Ecosystem: "npm", Name: e.Child, Version: e.ChildVersion},
		})
	}
	return out, nil
}

// readPackageLock finds the package-lock.json that describes dir, the same
// one ScanPackages reads: dir's own, or — only when dir has no supported
// lockfile at all — the root's of a workspace that declares dir as a member.
// member is dir relative to that root ("apps/web"), or "" for dir's own.
//
// A directory holding a different lockfile (pnpm, yarn, bun) never borrows an
// ancestor's package-lock.json: that file describes some other install.
func readPackageLock(dir string) (data []byte, member string, err error) {
	switch firstLockfile(dir) {
	case "package-lock.json":
		data, err = os.ReadFile(filepath.Join(dir, "package-lock.json"))
		return data, "", err
	case "":
		root, member, file, ok := workspaceRoot(dir)
		if ok && file == "package-lock.json" {
			data, err = os.ReadFile(filepath.Join(root, file))
			return data, member, err
		}
	}
	return nil, "", fmt.Errorf("package-lock.json for %s: %w", dir, fs.ErrNotExist)
}

// lockHasMember reports whether the lockfile already records member's own
// entry. One written before the member was added describes the other apps
// only, and borrowing their edges would attribute them to it. Unparseable
// data answers true so the caller's parser reports the error.
func lockHasMember(data []byte, member string) bool {
	if member == "" {
		return true
	}
	var lock struct {
		Packages map[string]json.RawMessage `json:"packages"`
	}
	if json.Unmarshal(data, &lock) != nil || lock.Packages == nil {
		return true
	}
	_, ok := lock.Packages[member]
	return ok
}
