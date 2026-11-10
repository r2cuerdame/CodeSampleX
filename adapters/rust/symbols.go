package rust

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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

// symbolBudget bounds one symbol scan. The scan runs after every observed
// command, in the MCP server's process too, so an unbounded walk of a large
// src/ tree holds a CPU core for as long as it takes (#629, #640).
type symbolBudget struct {
	files    int           // source files read
	fileSize int64         // largest single file read; bigger files are skipped
	bytes    int64         // source bytes read, in total
	elapsed  time.Duration // wall clock for both walks together
}

// The node adapter's budget: like it, this is a regex pass over a file's
// imports, not a parse.
var defaultSymbolBudget = symbolBudget{files: 64, fileSize: 64 << 10, bytes: 512 << 10, elapsed: 250 * time.Millisecond}

// unreadableModes are targets a source read could block on forever, or that
// are not files at all; behind a symlink only a stat of the target shows
// them. ModeIrregular is deliberately absent: on Windows it marks every
// reparse point that is not a link, OneDrive's cloud files among them, and
// those read like any other source file.
const unreadableModes = fs.ModeDir | fs.ModeNamedPipe | fs.ModeSocket | fs.ModeDevice | fs.ModeCharDevice

// ScanSymbols regex-scans src/**/*.rs and tests/**/*.rs for `use` and
// `extern crate` statements whose first path segment matches a dependency
// crate ident ('-' normalized to '_'). Leaf items become PROBABLE families
// like "serde::Deserialize"; bare/glob/self/extern-crate uses become
// package-level UNKNOWN. Macro invocations (#[derive(Serialize)]) are never
// attributed. Both walks share defaultSymbolBudget.
func (*Adapter) ScanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	return scanSymbols(ctx, dir, pkgs, defaultSymbolBudget)
}

func scanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage, budget symbolBudget) ([]scanner.SymbolUsage, error) {
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

	start := time.Now()
	files, total := 0, int64(0)
	for _, sub := range []string{"src", "tests"} {
		root := filepath.Join(dir, sub)
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entries are skipped, not fatal
			}
			// A spent budget or a cancelled context ends the walk, not the
			// scan: every usage already recorded was really seen in a file, so
			// it is kept. Whether a cancelled scan is used at all is the
			// caller's decision (scanner.Scan checks ctx itself).
			if ctx.Err() != nil || files >= budget.files || total >= budget.bytes || time.Since(start) >= budget.elapsed {
				return filepath.SkipAll
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".rs") {
				return nil
			}
			// Budgets are charged on the file a read reaches: for a symlink,
			// d describes the link itself, so the target is stat'ed instead.
			info, rerr := os.Stat(path)
			if rerr != nil || info.Mode()&unreadableModes != 0 || info.Size() > budget.fileSize || total+info.Size() > budget.bytes {
				return nil // skipped; a smaller file further on may still fit
			}
			content, ok := readAtMost(path, min(budget.fileSize, budget.bytes-total))
			if !ok {
				return nil
			}
			files++
			total += int64(len(content))
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

// readAtMost reads the file at path, refusing it once it holds more than
// limit bytes. The stat before it is only a pre-check: the file can grow, or a
// link be re-pointed, between that stat and this read, so the read is bounded
// too.
func readAtMost(path string, limit int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(content)) > limit {
		return nil, false
	}
	return content, true
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
