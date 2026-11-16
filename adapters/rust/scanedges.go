package rust

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// ScanEdges reports the dependency tree between crates.io packages from
// Cargo.lock.
//
// The tree was already in the file and nothing read it. The network held
// 1,351 dependency edges and every one of them was npm, because adapters/node
// was the only adapter implementing EdgeScanner — so "which package pulled
// this version of that library" had no answer for cargo, pypi or golang.
//
// Only crates.io entries (both ends) are emitted: root crates, path
// dependencies, git dependencies, and private-registry crates cannot be named
// as public registry PURLs without colliding with crates.io coordinates of the
// same name and version. Registry existence/publicness is still decided by
// the caller.
func (Adapter) ScanEdges(ctx context.Context, dir string) ([]scanner.Edge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "Cargo.lock"))
	if err != nil {
		return nil, err
	}
	pkgs := parseLockDependencies(string(data))

	// Cargo writes a dependency as "name", "name version", or "name version
	// (source)". Indexing all [[package]] entries by name lets every form
	// resolve to the actual lockfile entry (and drop ambiguous entries when
	// multiple sources share a name and version without a source suffix).
	byName := map[string][]lockEntry{}
	for _, p := range pkgs {
		if p.entry.name == "" {
			continue
		}
		byName[p.entry.name] = append(byName[p.entry.name], p.entry)
	}

	var out []scanner.Edge
	for _, p := range pkgs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if p.entry.version == "" || !isCratesIOSource(p.entry.source) {
			continue
		}
		for _, dep := range p.deps {
			name, version, source := splitLockDependency(dep)
			resolved, ok := resolveLockDependency(byName, name, version, source)
			if !ok || !isCratesIOSource(resolved.source) {
				continue
			}
			out = append(out, scanner.Edge{
				Parent: domain.PURL{Ecosystem: "cargo", Name: p.entry.name, Version: p.entry.version},
				Child:  domain.PURL{Ecosystem: "cargo", Name: resolved.name, Version: resolved.version},
			})
		}
	}
	return out, nil
}

func isCratesIOSource(source string) bool {
	return source == cratesIOGitIndex ||
		source == cratesIOSparsePrefix ||
		strings.HasPrefix(source, cratesIOSparsePrefix+"/")
}

func resolveLockDependency(byName map[string][]lockEntry, name, version, source string) (lockEntry, bool) {
	if name == "" {
		return lockEntry{}, false
	}
	var match lockEntry
	matches := 0
	for _, candidate := range byName[name] {
		if version != "" && candidate.version != version {
			continue
		}
		if source != "" && candidate.source != source {
			continue
		}
		match = candidate
		matches++
	}
	if matches != 1 || match.version == "" {
		return lockEntry{}, false
	}
	return match, true
}

// splitLockDependency reads one entry of a Cargo.lock dependencies list.
func splitLockDependency(dep string) (name, version, source string) {
	dep = strings.TrimSpace(dep)
	if i := strings.Index(dep, " ("); i >= 0 {
		if !strings.HasSuffix(dep, ")") {
			return "", "", ""
		}
		source = strings.TrimSpace(dep[i+2 : len(dep)-1])
		if source == "" {
			return "", "", ""
		}
		dep = strings.TrimSpace(dep[:i])
	}
	fields := strings.Fields(dep)
	switch len(fields) {
	case 0:
		return "", "", ""
	case 1:
		return fields[0], "", source
	case 2:
		return fields[0], fields[1], source
	default:
		return "", "", ""
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
