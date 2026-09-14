package rust

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Parse the declaration, not the filtered edge list: an unresolved child
// cannot turn a non-leaf into an explicit absence claim.
func (Adapter) ScanNoDependencies(ctx context.Context, dir string) ([]domain.PURL, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "Cargo.lock"))
	if err != nil {
		return nil, err
	}
	var lock struct {
		Version  int `toml:"version"`
		Packages []struct {
			Name         string   `toml:"name"`
			Version      string   `toml:"version"`
			Source       string   `toml:"source"`
			Dependencies []string `toml:"dependencies"`
		} `toml:"package"`
	}
	if err := toml.Unmarshal(raw, &lock); err != nil {
		return nil, err
	}
	if lock.Version != 3 && lock.Version != 4 {
		return nil, fmt.Errorf("unsupported Cargo.lock version %d", lock.Version)
	}
	proved, blocked := map[string]domain.PURL{}, map[string]bool{}
	for _, p := range lock.Packages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if p.Name == "" || !domain.ConcreteResolvedVersion(p.Version) {
			continue
		}
		coord := domain.PURL{Ecosystem: "cargo", Name: p.Name, Version: p.Version}
		key := coord.String()
		if len(p.Dependencies) != 0 || (p.Source != cratesIOGitIndex && !strings.HasPrefix(p.Source, cratesIOSparsePrefix)) {
			blocked[key] = true
			continue
		}
		proved[key] = coord
	}
	var out []domain.PURL
	for key, p := range proved {
		if !blocked[key] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}
