package rust

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

var (
	// [^;]+ spans newlines, so multi-line use groups are captured whole.
	useStmtRe     = regexp.MustCompile(`(?m)^\s*(?:pub(?:\([^)]*\))?\s+)?use\s+([^;]+);`)
	externCrateRe = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?extern\s+crate\s+([A-Za-z_][A-Za-z0-9_]*)`)
	wsRe          = regexp.MustCompile(`\s+`)
)

// ScanSymbols regex-scans src/**/*.rs and tests/**/*.rs for `use` and
// `extern crate` statements whose first path segment matches a dependency
// crate ident ('-' normalized to '_'). Leaf items become PROBABLE families
// like "serde::Deserialize"; bare/glob/self/extern-crate uses become
// package-level UNKNOWN. Macro invocations (#[derive(Serialize)]) are never
// attributed.
func (*Adapter) ScanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	identToPkg := map[string]domain.PURL{}
	for _, p := range pkgs {
		identToPkg[strings.ReplaceAll(p.PURL.Name, "-", "_")] = p.PURL
	}
	seen := map[string]bool{}
	var out []scanner.SymbolUsage
	add := func(u scanner.SymbolUsage) {
		key := u.Package.String() + "|" + u.Family
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, u)
	}

	for _, sub := range []string{"src", "tests"} {
		root := filepath.Join(dir, sub)
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entries are skipped, not fatal
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".rs") {
				return nil
			}
			content, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			scanRustSource(string(content), identToPkg, add)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package.Name != out[j].Package.Name {
			return out[i].Package.Name < out[j].Package.Name
		}
		return out[i].Family < out[j].Family
	})
	return out, nil
}

func scanRustSource(content string, identToPkg map[string]domain.PURL, add func(scanner.SymbolUsage)) {
	for _, m := range useStmtRe.FindAllStringSubmatch(content, -1) {
		tree := normalizeUseTree(m[1])
		tree = strings.TrimPrefix(tree, "::") // 2015-edition global paths
		for _, leaf := range expandUseTree(tree) {
			emitLeaf(leaf, identToPkg, add)
		}
	}
	for _, m := range externCrateRe.FindAllStringSubmatch(content, -1) {
		if pkg, ok := identToPkg[m[1]]; ok {
			add(packageLevelUsage(pkg, m[1]))
		}
	}
}

// emitLeaf attributes one expanded use path to a dependency, if it matches.
func emitLeaf(leaf string, identToPkg map[string]domain.PURL, add func(scanner.SymbolUsage)) {
	segs := strings.Split(leaf, "::")
	first := segs[0]
	pkg, ok := identToPkg[first]
	if !ok {
		return
	}
	last := segs[len(segs)-1]
	if len(segs) == 1 || last == "*" || last == "self" {
		add(packageLevelUsage(pkg, first))
		return
	}
	kind := "function"
	if r := []rune(last); len(r) > 0 && unicode.IsUpper(r[0]) {
		kind = "class"
	}
	add(scanner.SymbolUsage{
		Package:    pkg,
		Family:     leaf,
		Kind:       kind,
		Confidence: domain.SymbolProbable,
	})
}

// packageLevelUsage is the UNKNOWN-confidence record for a bare
// `use serde;`, glob import, or `extern crate serde;`.
func packageLevelUsage(pkg domain.PURL, ident string) scanner.SymbolUsage {
	return scanner.SymbolUsage{
		Package:    pkg,
		Family:     ident,
		Kind:       "module",
		Confidence: domain.SymbolUnknown,
	}
}

// normalizeUseTree collapses whitespace so multi-line trees parse like
// single-line ones.
func normalizeUseTree(s string) string {
	s = wsRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, " ::", "::")
	s = strings.ReplaceAll(s, ":: ", "::")
	return strings.TrimSpace(s)
}

// expandUseTree flattens a use tree into full leaf paths:
// "serde::{de::{DeserializeOwned}, Serialize}" →
// ["serde::de::DeserializeOwned", "serde::Serialize"]. `as` aliases keep the
// original item name.
func expandUseTree(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if s[0] == '{' {
		end := strings.LastIndexByte(s, '}')
		if end < 0 {
			return nil
		}
		var out []string
		for _, part := range splitTopLevel(s[1:end]) {
			out = append(out, expandUseTree(part)...)
		}
		return out
	}
	if i := strings.IndexByte(s, '{'); i >= 0 {
		prefix := strings.TrimSuffix(strings.TrimSpace(s[:i]), "::")
		var out []string
		for _, leaf := range expandUseTree(s[i:]) {
			out = append(out, prefix+"::"+leaf)
		}
		return out
	}
	if k := strings.Index(s, " as "); k >= 0 {
		s = s[:k]
	}
	return []string{strings.TrimSpace(s)}
}

// splitTopLevel splits on commas outside braces.
func splitTopLevel(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}
