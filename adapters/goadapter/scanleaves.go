package goadapter

import (
	"context"
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A selected module's own go.mod must explicitly declare no requirements.
// Missing cache files and requirements whose children were not selected do
// not prove absence. This uses the same resolved inventory as ScanEdges.
func (a *Adapter) ScanNoDependencies(ctx context.Context, dir string) ([]domain.PURL, error) {
	selected, err := goBuildList(dir)
	if err != nil {
		return nil, err
	}
	var out []domain.PURL
	for name, version := range selected {
		if !domain.ConcreteResolvedVersion(version) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := parseCachedGoMod(dir, name, version)
		if err != nil || f.Module == nil || f.Module.Mod.Path != name || len(f.Require) != 0 {
			continue
		}
		out = append(out, domain.PURL{Ecosystem: goEcosystem, Name: name, Version: version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}
