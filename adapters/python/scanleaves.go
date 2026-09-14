package python

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

// Requirements pins alone carry no dependency declaration. Only a validated
// uv/Poetry package block can report an explicit leaf, with nonempty optional
// or development declarations conservatively kept unknown.
func (*Adapter) ScanNoDependencies(ctx context.Context, dir string) ([]domain.PURL, error) {
	name := "uv.lock"
	if !fileExists(filepath.Join(dir, name)) {
		name = "poetry.lock"
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	var lock struct {
		Version  int `toml:"version"`
		Metadata struct {
			Version string `toml:"lock-version"`
		} `toml:"metadata"`
		Packages []struct {
			Name         string         `toml:"name"`
			Version      string         `toml:"version"`
			Source       map[string]any `toml:"source"`
			Dependencies any            `toml:"dependencies"`
			Optional     map[string]any `toml:"optional-dependencies"`
			Dev          map[string]any `toml:"dev-dependencies"`
			Metadata     map[string]any `toml:"metadata"`
			Extras       map[string]any `toml:"extras"`
		} `toml:"package"`
	}
	if err := toml.Unmarshal(raw, &lock); err != nil {
		return nil, err
	}
	uv := name == "uv.lock"
	if uv && lock.Version != 1 {
		return nil, fmt.Errorf("unsupported uv.lock version %d", lock.Version)
	}
	if !uv && lock.Metadata.Version != "1.1" && lock.Metadata.Version != "2.0" && lock.Metadata.Version != "2.1" {
		return nil, fmt.Errorf("unsupported poetry.lock version %q", lock.Metadata.Version)
	}
	proved, blocked := map[string]domain.PURL{}, map[string]bool{}
	for _, p := range lock.Packages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count := 0
		switch d := p.Dependencies.(type) {
		case nil:
		case []any:
			if !uv {
				return nil, fmt.Errorf("invalid Poetry dependency declaration")
			}
			count = len(d)
		case map[string]any:
			if uv {
				return nil, fmt.Errorf("invalid uv dependency declaration")
			}
			count = len(d)
		default:
			return nil, fmt.Errorf("invalid dependency declaration")
		}
		if p.Name == "" || !domain.ConcreteResolvedVersion(p.Version) {
			continue
		}
		coord := domain.PURL{Ecosystem: "pypi", Name: normalizeDist(p.Name), Version: p.Version}
		key := coord.String()
		publicSource := !uv && len(p.Source) == 0
		if registry, ok := p.Source["registry"].(string); uv && ok {
			publicSource = len(p.Source) == 1 && strings.TrimRight(registry, "/") == "https://pypi.org/simple"
		}
		if count != 0 || len(p.Optional)+len(p.Dev)+len(p.Metadata)+len(p.Extras) != 0 || !publicSource {
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
