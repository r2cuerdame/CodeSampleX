package node

import (
	"context"
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
	data, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return nil, err
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
