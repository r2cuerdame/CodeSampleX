package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ScanNoDependencies reads only installed package-lock entries. Missing
// children, unsupported lockfiles, and non-registry/link entries never prove a leaf.
// A workspace member reads the root's lockfile the way ScanPackages does.
func (Adapter) ScanNoDependencies(_ context.Context, dir string) ([]domain.PURL, error) {
	data, member, err := readPackageLock(dir)
	if err != nil {
		return nil, err
	}
	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			npmLockEntry
			BundleDependencies  json.RawMessage `json:"bundleDependencies"`
			BundledDependencies json.RawMessage `json:"bundledDependencies"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	if (lock.LockfileVersion != 2 && lock.LockfileVersion != 3) || lock.Packages == nil {
		return nil, fmt.Errorf("no installed packages map")
	}
	if _, ok := lock.Packages[member]; !ok && member != "" {
		return nil, nil // a stale lockfile that predates this member proves nothing about it
	}
	entries := make(map[string]npmLockEntry, len(lock.Packages))
	for path, e := range lock.Packages {
		entries[path] = e.npmLockEntry
	}
	nonRegistry := nonRegistryInstalledKeys(entries)
	proved, blocked := map[string]domain.PURL{}, map[string]bool{}
	for path, e := range lock.Packages {
		idx := strings.LastIndex(path, "node_modules/")
		if idx < 0 || e.Link || nonRegistry[path] || !domain.ConcreteResolvedVersion(e.Version) {
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
			hasBundledDependencies(e.BundleDependencies) || hasBundledDependencies(e.BundledDependencies) {
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

// hasBundledDependencies reports whether a bundleDependencies or
// bundledDependencies JSON value may introduce bundled dependencies. Explicit
// empty or disabled declarations (false, null, []) carry none; true, non-empty
// arrays, and unknown non-whitespace JSON conservatively block leaf status.
func hasBundledDependencies(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return true
	}
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case []any:
		return len(x) != 0
	default:
		return true
	}
}
