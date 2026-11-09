package goadapter

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

var majorSuffixRE = regexp.MustCompile(`^v[0-9]+$`)

// skipDirs hold other people's modules, never this module's source.
var skipDirs = map[string]bool{"vendor": true, "node_modules": true}

// skipRootDirs are build-output and packaging directories by convention, but
// only at the project root: below it build and dist are ordinary Go package
// names (goreleaser's internal/build, the toolchain's cmd/dist), so unlike
// the node adapter's list they are not pruned everywhere.
var skipRootDirs = map[string]bool{"build": true, "dist": true}

// symbolBudget bounds one symbol scan. The scan runs after every observed
// command, in the MCP server's process too, where an unbounded full parse of
// a large tree is the leading suspect for a held CPU core (#629, #632).
type symbolBudget struct {
	files    int           // source files parsed
	bytes    int64         // source bytes parsed, in total
	fileSize int64         // largest single file parsed; bigger files are skipped
	elapsed  time.Duration // wall clock for the whole walk
}

// The node adapter's budget (64 files, 512 KiB) is a regex pass over a few
// imports. Go packages are split across many files and symbol evidence is
// what search grades on, so more source fits here; the time limit is what
// caps the CPU a single scan can take.
var defaultSymbolBudget = symbolBudget{
	files:    256,
	bytes:    2 << 20,
	fileSize: 256 << 10,
	elapsed:  250 * time.Millisecond,
}

// ScanSymbols parses non-test .go files under dir (skipping vendor/,
// node_modules/, hidden/underscore dirs and a root-level build/ or dist/;
// the project's own testdata is included) and collects selector usages of
// imports that belong to the given packages. Shadowing of an import name by
// a local identifier cannot be ruled out without full type checking, hence
// PROBABLE. The walk stops at defaultSymbolBudget.
func (a *Adapter) ScanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	return scanSymbols(ctx, dir, pkgs, defaultSymbolBudget)
}

func scanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage, b symbolBudget) ([]scanner.SymbolUsage, error) {
	modules := make([]string, 0, len(pkgs))
	byModule := make(map[string]domain.PURL, len(pkgs))
	for _, p := range pkgs {
		modules = append(modules, p.PURL.Name)
		byModule[p.PURL.Name] = p.PURL
	}
	// Longest module path first so nested modules win prefix matching.
	sort.Slice(modules, func(i, j int) bool { return len(modules[i]) > len(modules[j]) })

	seen := map[string]scanner.SymbolUsage{}
	root := filepath.Clean(dir)
	start := time.Now()
	files, total := 0, int64(0)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skipped, not fatal -- the same rule the node and python
			// adapters follow, and the one scanFile below states: partial
			// local code must never abort a scan.
			//
			// Returning the error aborted the WHOLE module scan on the
			// first directory the process could not list, and the caller
			// discards that error, so a Go repo with one root-owned
			// bind-mount directory (./data, ./pgdata -- routine in service
			// repos) recorded zero symbol observations for every package,
			// exited 0, and said nothing. Symbol evidence is what search
			// grades on, so the project silently stopped contributing and
			// stopped matching.
			return nil
		}
		// A spent budget or a cancelled context ends the walk, not the scan.
		// Every usage already recorded was really seen in a file, so it is
		// returned instead of being thrown away with the files not reached;
		// whether a cancelled scan is used at all is the caller's decision
		// (scanner.Scan checks ctx itself).
		if ctx.Err() != nil || files >= b.files || total >= b.bytes || time.Since(start) >= b.elapsed {
			return filepath.SkipAll
		}
		name := d.Name()
		if d.IsDir() {
			if p == dir {
				return nil
			}
			if skipDirs[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				(skipRootDirs[name] && filepath.Dir(p) == root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > b.fileSize || total+info.Size() > b.bytes {
			return nil // skipped; a smaller file further on may still fit
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		files++
		total += int64(len(src))
		scanFile(p, src, modules, byModule, seen)
		return nil
	})
	if err != nil {
		return nil, err
	}

	usages := make([]scanner.SymbolUsage, 0, len(seen))
	for _, u := range seen {
		usages = append(usages, u)
	}
	sort.Slice(usages, func(i, j int) bool { return usages[i].Family < usages[j].Family })
	return usages, nil
}

// scanFile records symbol usages from one file's source. Files that fail to
// parse are skipped: partial local code must never abort a scan.
func scanFile(path string, src []byte, modules []string, byModule map[string]domain.PURL, seen map[string]scanner.SymbolUsage) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil || file == nil {
		return
	}

	// ident name → import path, for imports belonging to required modules.
	identToImport := map[string]string{}
	importToPURL := map[string]domain.PURL{}
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		purl, ok := matchModule(importPath, modules, byModule)
		if !ok {
			continue
		}
		importToPURL[importPath] = purl
		var local string
		if imp.Name != nil {
			local = imp.Name.Name
		} else {
			local = defaultImportName(importPath)
		}
		switch local {
		case ".", "_":
			// Dot and blank imports hide the selector: only a
			// package-level usage with UNKNOWN confidence is honest.
			addUsage(seen, scanner.SymbolUsage{
				Package:    purl,
				Family:     importPath,
				Kind:       "module",
				Confidence: domain.SymbolUnknown,
			})
		default:
			identToImport[local] = importPath
		}
	}
	if len(identToImport) == 0 {
		return
	}

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		importPath, ok := identToImport[ident.Name]
		if !ok {
			return true
		}
		addUsage(seen, scanner.SymbolUsage{
			Package:    importToPURL[importPath],
			Family:     importPath + "." + sel.Sel.Name,
			Kind:       "function",
			Confidence: domain.SymbolProbable,
		})
		return true
	})
}

func addUsage(seen map[string]scanner.SymbolUsage, u scanner.SymbolUsage) {
	seen[u.Package.String()+"|"+u.Family] = u
}

// matchModule finds the longest required module path that is a prefix of
// importPath (whole path segments only).
func matchModule(importPath string, modules []string, byModule map[string]domain.PURL) (domain.PURL, bool) {
	for _, m := range modules {
		if importPath == m || strings.HasPrefix(importPath, m+"/") {
			return byModule[m], true
		}
	}
	return domain.PURL{}, false
}

// defaultImportName approximates the package identifier for an import
// without alias: the last path segment, skipping a /vN major suffix.
func defaultImportName(importPath string) string {
	base := path.Base(importPath)
	if majorSuffixRE.MatchString(base) {
		base = path.Base(path.Dir(importPath))
	}
	return base
}
