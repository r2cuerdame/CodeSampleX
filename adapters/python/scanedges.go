package python

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
func (Adapter) ScanEdges(_ context.Context, dir string) ([]scanner.Edge, error) {
	for _, name := range []string{"uv.lock", "poetry.lock"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		return pythonLockEdges(string(data)), nil
	}
	return nil, errors.New("python: no uv.lock or poetry.lock records a dependency tree here")
}

var (
	// uv.lock: dependencies = [ { name = "x" }, ... ] — inline tables that
	// name the package and leave its version to the package's own block.
	uvDepNameRe = regexp.MustCompile(`\{\s*name\s*=\s*"([^"]+)"`)
	// poetry.lock: a [package.dependencies] sub-table whose keys are names
	// and whose values are CONSTRAINTS, never resolved versions.
	poetryDepsHeaderRe = regexp.MustCompile(`(?m)^\[package\.dependencies\]\s*$`)
	poetryDepKeyRe     = regexp.MustCompile(`(?m)^([A-Za-z0-9._-]+)\s*=`)
	tomlTableRe        = regexp.MustCompile(`(?m)^\[`)
)

// pythonLockEdges reads both lock shapes out of one [[package]]-delimited
// document. A version is only ever taken from the depended-on package's own
// block: a constraint like ">=2.0" is not a version and must never be
// recorded as one.
func pythonLockEdges(content string) []scanner.Edge {
	blocks := strings.Split(content, "[[package]]")
	if len(blocks) < 2 {
		return nil
	}

	type pkg struct {
		name, version string
		deps          []string
	}
	pkgs := make([]pkg, 0, len(blocks)-1)
	versions := map[string]string{}

	for _, b := range blocks[1:] {
		name := normalizeDist(firstGroup(tomlNameRe, b))
		if name == "" {
			continue
		}
		p := pkg{name: name, version: firstGroup(tomlVersionRe, b)}
		if p.version != "" {
			versions[name] = p.version
		}
		for _, m := range uvDepNameRe.FindAllStringSubmatch(b, -1) {
			p.deps = append(p.deps, m[1])
		}
		p.deps = append(p.deps, poetryDependencyNames(b)...)
		pkgs = append(pkgs, p)
	}

	var out []scanner.Edge
	for _, p := range pkgs {
		if p.version == "" {
			continue
		}
		seen := map[string]bool{}
		for _, dep := range p.deps {
			child := normalizeDist(dep)
			// The lockfile is the only place a version can come from. A name
			// it does not resolve is dropped rather than recorded at an
			// invented version.
			version, ok := versions[child]
			if !ok || child == "" || seen[child] {
				continue
			}
			seen[child] = true
			out = append(out, scanner.Edge{
				Parent: domain.PURL{Ecosystem: "pypi", Name: p.name, Version: p.version},
				Child:  domain.PURL{Ecosystem: "pypi", Name: child, Version: version},
			})
		}
	}
	return out
}

// poetryDependencyNames reads the keys of one [package.dependencies] table.
// The table ends at the next table header, whatever it is.
func poetryDependencyNames(block string) []string {
	loc := poetryDepsHeaderRe.FindStringIndex(block)
	if loc == nil {
		return nil
	}
	rest := block[loc[1]:]
	if next := tomlTableRe.FindStringIndex(rest); next != nil {
		rest = rest[:next[0]]
	}
	var out []string
	for _, m := range poetryDepKeyRe.FindAllStringSubmatch(rest, -1) {
		out = append(out, m[1])
	}
	return out
}
