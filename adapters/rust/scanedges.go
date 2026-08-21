package rust

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// ScanEdges reports the dependency tree from Cargo.lock.
//
// The tree was already in the file and nothing read it. The network held
// 1,351 dependency edges and every one of them was npm, because adapters/node
// was the only adapter implementing EdgeScanner — so "which package pulled
// this version of that library" had no answer for cargo, pypi or golang.
//
// Publicness is not decided here. Every edge is returned and the caller drops
// the ones whose ends are not public, the same way it does for packages:
// deciding it twice in two places is how the two answers drift apart.
func (Adapter) ScanEdges(_ context.Context, dir string) ([]scanner.Edge, error) {
	data, err := os.ReadFile(filepath.Join(dir, "Cargo.lock"))
	if err != nil {
		return nil, err
	}
	pkgs := parseLockDependencies(string(data))

	// Cargo writes a dependency as "name", "name version", or "name version
	// (source)". Only the first form needs resolving, and it is unambiguous
	// precisely because cargo omits the version when one package of that name
	// is in the graph.
	byName := map[string]lockEntry{}
	for _, p := range pkgs {
		byName[p.entry.name] = p.entry
	}

	var out []scanner.Edge
	for _, p := range pkgs {
		if p.entry.version == "" {
			continue
		}
		for _, dep := range p.deps {
			name, version := splitLockDependency(dep)
			if version == "" {
				resolved, ok := byName[name]
				if !ok || resolved.version == "" {
					// A name the lockfile does not resolve is a name we
					// cannot place at a version. Recording it at an invented
					// one would be worse than not recording it.
					continue
				}
				version = resolved.version
			}
			out = append(out, scanner.Edge{
				Parent: domain.PURL{Ecosystem: "cargo", Name: p.entry.name, Version: p.entry.version},
				Child:  domain.PURL{Ecosystem: "cargo", Name: name, Version: version},
			})
		}
	}
	return out, nil
}

// splitLockDependency reads one entry of a Cargo.lock dependencies list.
func splitLockDependency(dep string) (name, version string) {
	dep = strings.TrimSpace(dep)
	if i := strings.Index(dep, " ("); i >= 0 {
		dep = strings.TrimSpace(dep[:i]) // drop the source suffix
	}
	fields := strings.Fields(dep)
	switch len(fields) {
	case 0:
		return "", ""
	case 1:
		return fields[0], ""
	default:
		return fields[0], fields[1]
	}
}

type lockPackage struct {
	entry lockEntry
	deps  []string
}

// parseLockDependencies scans [[package]] sections for the fields
// parseLockPackages already reads, plus the dependencies list beside them.
//
// It is a second pass rather than a widening of parseLockPackages because the
// package scan feeds publicness and version reporting, where a dependency
// list has no business, and because a list spans lines while every field that
// scan reads does not.
func parseLockDependencies(content string) []lockPackage {
	var (
		out    []lockPackage
		cur    lockPackage
		in     bool
		inDeps bool
	)
	flush := func() {
		if in && cur.entry.name != "" {
			out = append(out, cur)
		}
		cur = lockPackage{}
		inDeps = false
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			flush()
			in = trimmed == "[[package]]"
			continue
		}
		if !in {
			continue
		}
		if inDeps {
			if trimmed == "]" {
				inDeps = false
				continue
			}
			if dep := strings.Trim(strings.TrimSuffix(trimmed, ","), `"`); dep != "" {
				cur.deps = append(cur.deps, dep)
			}
			continue
		}
		if trimmed == "dependencies = [" {
			inDeps = true
			continue
		}
		if m := lockKVRe.FindStringSubmatch(trimmed); m != nil {
			switch m[1] {
			case "name":
				cur.entry.name = m[2]
			case "version":
				cur.entry.version = m[2]
			case "source":
				cur.entry.source = m[2]
			}
		}
	}
	flush()
	return out
}
