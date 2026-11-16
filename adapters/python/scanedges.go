package python

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// ScanEdges reports the dependency tree from uv.lock or poetry.lock.
//
// The tree was already in those files and nothing read it. The network held
// 1,351 dependency edges and every one of them was npm, because adapters/node
// was the only adapter implementing EdgeScanner — so "which package pulled
// this version of that library" had no answer for pypi at all.
//
// requirements.txt is deliberately absent. A flat list of pins records no
// tree, and reporting none is the honest answer rather than guessing one.
//
// Publicness is not decided here. Every edge is returned and the caller drops
// the ones whose ends are not public, the same way it does for packages.
func (Adapter) ScanEdges(ctx context.Context, dir string) ([]scanner.Edge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, name := range []string{"uv.lock", "poetry.lock"} {
		path := filepath.Join(dir, name)
		if !fileExists(path) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return pythonLockEdges(ctx, data, name == "uv.lock")
	}
	return nil, errors.New("python: no uv.lock or poetry.lock records a dependency tree here")
}

type lockDependencyRef struct {
	name    string
	version string // non-empty only when uv.lock pins a specific resolved version
}

type lockEdgeKey struct {
	parentName, parentVersion string
	childName, childVersion   string
}

// pythonLockEdges decodes [[package]] entries from uv.lock or poetry.lock.
// A child version comes only from resolved [[package]] blocks: Poetry
// constraints (such as ">=2.0") are never resolved versions, and a name that
// resolved to multiple distinct versions cannot be linked without an explicit
// uv dependency version.
func pythonLockEdges(ctx context.Context, data []byte, uv bool) ([]scanner.Edge, error) {
	var lock struct {
		Packages []struct {
			Name         string `toml:"name"`
			Version      string `toml:"version"`
			Dependencies any    `toml:"dependencies"`
		} `toml:"package"`
	}
	if err := toml.Unmarshal(data, &lock); err != nil {
		return nil, err
	}

	resolved := make(map[string]map[string]bool, len(lock.Packages))
	for _, p := range lock.Packages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := normalizeDist(p.Name)
		if name == "" || !domain.ConcreteResolvedVersion(p.Version) {
			continue
		}
		if resolved[name] == nil {
			resolved[name] = make(map[string]bool)
		}
		resolved[name][p.Version] = true
	}

	seen := make(map[lockEdgeKey]bool)
	var out []scanner.Edge
	for _, p := range lock.Packages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		deps, err := extractLockDeps(p.Dependencies, uv)
		if err != nil {
			return nil, err
		}
		parentName := normalizeDist(p.Name)
		if parentName == "" || !domain.ConcreteResolvedVersion(p.Version) {
			continue
		}
		parent := domain.PURL{Ecosystem: "pypi", Name: parentName, Version: p.Version}
		for _, dep := range deps {
			childVersion, ok := resolveLockDependencyVersion(resolved, dep.name, dep.version)
			if !ok {
				continue
			}
			key := lockEdgeKey{
				parentName:    parent.Name,
				parentVersion: parent.Version,
				childName:     dep.name,
				childVersion:  childVersion,
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, scanner.Edge{
				Parent: parent,
				Child:  domain.PURL{Ecosystem: "pypi", Name: dep.name, Version: childVersion},
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Parent.Name != out[j].Parent.Name {
			return out[i].Parent.Name < out[j].Parent.Name
		}
		if out[i].Parent.Version != out[j].Parent.Version {
			return out[i].Parent.Version < out[j].Parent.Version
		}
		if out[i].Child.Name != out[j].Child.Name {
			return out[i].Child.Name < out[j].Child.Name
		}
		return out[i].Child.Version < out[j].Child.Version
	})
	return out, nil
}

func extractLockDeps(raw any, uv bool) ([]lockDependencyRef, error) {
	if raw == nil {
		return nil, nil
	}
	if uv {
		return extractUVDeps(raw)
	}
	return extractPoetryDeps(raw)
}

func extractUVDeps(raw any) ([]lockDependencyRef, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid uv dependency declaration")
	}
	deps := make([]lockDependencyRef, 0, len(items))
	for _, item := range items {
		tbl, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid uv dependency entry")
		}
		rawName, hasName := tbl["name"]
		if !hasName {
			continue
		}
		nameStr, ok := rawName.(string)
		if !ok {
			return nil, fmt.Errorf("invalid uv dependency name")
		}
		name := normalizeDist(nameStr)
		if name == "" {
			continue
		}
		var ver string
		if rawVer, hasVer := tbl["version"]; hasVer {
			v, ok := rawVer.(string)
			if !ok {
				return nil, fmt.Errorf("invalid uv dependency version")
			}
			ver = v
		}
		deps = append(deps, lockDependencyRef{name: name, version: ver})
	}
	return deps, nil
}

func extractPoetryDeps(raw any) ([]lockDependencyRef, error) {
	tbl, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid Poetry dependency declaration")
	}
	keys := make([]string, 0, len(tbl))
	for k := range tbl {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	deps := make([]lockDependencyRef, 0, len(keys))
	for _, rawName := range keys {
		name := normalizeDist(rawName)
		if name == "" {
			continue
		}
		opt, err := poetryDependencyOptional(rawName, tbl[rawName])
		if err != nil {
			return nil, err
		}
		if opt {
			continue
		}
		deps = append(deps, lockDependencyRef{name: name})
	}
	return deps, nil
}

func poetryDependencyOptional(rawName string, val any) (bool, error) {
	switch v := val.(type) {
	case string:
		return false, nil
	case map[string]any:
		opt, _ := v["optional"].(bool)
		return opt, nil
	case []any:
		if len(v) == 0 {
			return false, nil
		}
		allOptional := true
		for _, item := range v {
			tbl, ok := item.(map[string]any)
			if !ok {
				return false, fmt.Errorf("invalid Poetry dependency entry for %q", rawName)
			}
			if opt, _ := tbl["optional"].(bool); !opt {
				allOptional = false
			}
		}
		return allOptional, nil
	default:
		return false, fmt.Errorf("invalid Poetry dependency declaration for %q", rawName)
	}
}

func resolveLockDependencyVersion(resolved map[string]map[string]bool, name, explicitVersion string) (string, bool) {
	vers := resolved[name]
	if len(vers) == 0 {
		return "", false
	}
	if explicitVersion != "" {
		if domain.ConcreteResolvedVersion(explicitVersion) && vers[explicitVersion] {
			return explicitVersion, true
		}
		return "", false
	}
	if len(vers) != 1 {
		return "", false
	}
	for v := range vers {
		return v, true
	}
	return "", false
}
