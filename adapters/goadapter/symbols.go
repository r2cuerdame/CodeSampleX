package goadapter

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

var majorSuffixRE = regexp.MustCompile(`^v[0-9]+$`)

// ScanSymbols parses every non-test .go file under dir (skipping vendor/
// and hidden/underscore dirs; the project's own testdata is included) and
// collects selector usages of imports that belong to the given packages.
// Shadowing of an import name by a local identifier cannot be ruled out
// without full type checking, hence PROBABLE.
func (a *Adapter) ScanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	modules := make([]string, 0, len(pkgs))
	byModule := make(map[string]domain.PURL, len(pkgs))
	for _, p := range pkgs {
		modules = append(modules, p.PURL.Name)
		byModule[p.PURL.Name] = p.PURL
	}
	// Longest module path first so nested modules win prefix matching.
	sort.Slice(modules, func(i, j int) bool { return len(modules[i]) > len(modules[j]) })

	seen := map[string]scanner.SymbolUsage{}
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		name := d.Name()
		if d.IsDir() {
			if p != dir && (name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		scanFile(p, modules, byModule, seen)
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

// scanFile records symbol usages from one file. Files that fail to parse
// are skipped: partial local code must never abort a scan.
func scanFile(path string, modules []string, byModule map[string]domain.PURL, seen map[string]scanner.SymbolUsage) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
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
