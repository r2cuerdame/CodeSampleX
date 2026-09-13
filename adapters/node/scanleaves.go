package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ScanNoDependencies reads only installed package-lock entries. Missing
// children, unsupported lockfiles, and private/link entries never prove a leaf.
func (Adapter) ScanNoDependencies(_ context.Context, dir string) ([]domain.PURL, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return nil, err
	}
	var lock struct {
		Packages map[string]struct {
			Name                 string            `json:"name"`
			Version              string            `json:"version"`
			Link                 bool              `json:"link"`
			Dependencies         map[string]string `json:"dependencies"`
			DevDependencies      map[string]string `json:"devDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
			PeerDependencies     map[string]string `json:"peerDependencies"`
			BundleDependencies   json.RawMessage   `json:"bundleDependencies"`
			BundledDependencies  json.RawMessage   `json:"bundledDependencies"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	if lock.Packages == nil {
		return nil, fmt.Errorf("no installed packages map")
	}
	proved, blocked := map[string]domain.PURL{}, map[string]bool{}
	for path, e := range lock.Packages {
		idx := strings.LastIndex(path, "node_modules/")
		if idx < 0 || e.Link || !domain.ConcreteResolvedVersion(e.Version) {
			continue
		}
		name := path[idx+len("node_modules/"):]
		if e.Name != "" {
			name = e.Name
		}
		if name == "" {
			continue
		}
		p := domain.PURL{Ecosystem: "npm", Name: name, Version: e.Version}
		key := p.String()
		if len(e.Dependencies)+len(e.DevDependencies)+len(e.OptionalDependencies)+len(e.PeerDependencies) != 0 ||
			len(e.BundleDependencies) != 0 || len(e.BundledDependencies) != 0 {
			blocked[key] = true
			continue
		}
		proved[key] = p
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
