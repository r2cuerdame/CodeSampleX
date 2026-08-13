package python

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// moduleDistTable maps import names whose PyPI distribution name differs
// beyond PEP 503 normalization.
var moduleDistTable = map[string]string{
	"yaml":    "pyyaml",
	"PIL":     "pillow",
	"sklearn": "scikit-learn",
	"cv2":     "opencv-python",
	"bs4":     "beautifulsoup4",
	"dotenv":  "python-dotenv",
}

// moduleToDist maps a top-level import module to a PEP 503-normalized
// distribution name.
func moduleToDist(topModule string) string {
	if d, ok := moduleDistTable[topModule]; ok {
		return d
	}
	return normalizeDist(topModule)
}

var skipDirNames = map[string]bool{
	"venv":          true,
	".venv":         true,
	"site-packages": true,
	"__pycache__":   true,
	"node_modules":  true,
}

var (
	importLineRe = regexp.MustCompile(`^import\s+(.+)$`)
	fromLineRe   = regexp.MustCompile(`^from\s+([A-Za-z_][A-Za-z0-9_.]*)\s+import\s+(.+)$`)
	getattrRe    = regexp.MustCompile(`\bgetattr\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*,`)
	dynImportRe  = regexp.MustCompile(`(?:\bimportlib\.import_module|\b__import__)\(\s*['"]([A-Za-z_][A-Za-z0-9_.]*)['"]`)
	modPathRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	identOnlyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type symKey struct{ dist, family string }

// ScanSymbols regex-scans project *.py files for import statements against
// the scanned package set. `from mod import a` yields family "mod.a"
// PROBABLE; a plain `import mod` yields a package-level UNKNOWN usage.
// Dynamic access (getattr on an imported module, importlib.import_module,
// __import__) degrades every symbol of that package to UNKNOWN (§13.3).
func (*Adapter) ScanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	byDist := make(map[string]scanner.ResolvedPackage, len(pkgs))
	for _, p := range pkgs {
		byDist[normalizeDist(p.PURL.Name)] = p
	}
	found := map[symKey]*scanner.SymbolUsage{}
	dynamic := map[string]bool{}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if path == dir {
				return nil
			}
			name := d.Name()
			if skipDirNames[name] || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".py") {
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		scanPyFile(string(data), byDist, found, dynamic)
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]scanner.SymbolUsage, 0, len(found))
	for k, u := range found {
		if dynamic[k.dist] {
			u.Confidence = domain.SymbolUnknown
		}
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package.Name != out[j].Package.Name {
			return out[i].Package.Name < out[j].Package.Name
		}
		return out[i].Family < out[j].Family
	})
	return out, nil
}

func scanPyFile(src string, byDist map[string]scanner.ResolvedPackage, found map[symKey]*scanner.SymbolUsage, dynamic map[string]bool) {
	alias := map[string]string{} // bound local name -> dist name
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))

		if m := importLineRe.FindStringSubmatch(line); m != nil {
			for _, item := range strings.Split(m[1], ",") {
				parts := strings.Fields(strings.TrimSpace(item))
				if len(parts) == 0 || !modPathRe.MatchString(parts[0]) {
					continue
				}
				mod := parts[0]
				top := strings.SplitN(mod, ".", 2)[0]
				dist := moduleToDist(top)
				bound := top
				if len(parts) == 3 && parts[1] == "as" {
					bound = parts[2]
				}
				alias[bound] = dist
				if pkg, ok := byDist[dist]; ok {
					addUsage(found, dist, pkg, mod, "module", domain.SymbolUnknown)
				}
			}
			continue
		}

		if m := fromLineRe.FindStringSubmatch(line); m != nil {
			mod := m[1] // relative "from . import x" never matches (leading dot)
			top := strings.SplitN(mod, ".", 2)[0]
			dist := moduleToDist(top)
			pkg, ok := byDist[dist]
			if !ok {
				continue
			}
			for _, item := range strings.Split(strings.Trim(m[2], "()"), ",") {
				parts := strings.Fields(strings.TrimSpace(item))
				if len(parts) == 0 {
					continue
				}
				sym := parts[0]
				if sym == "*" {
					addUsage(found, dist, pkg, mod, "module", domain.SymbolUnknown)
					continue
				}
				if !identOnlyRe.MatchString(sym) {
					continue
				}
				addUsage(found, dist, pkg, mod+"."+sym, "property", domain.SymbolProbable)
			}
			continue
		}

		if m := getattrRe.FindStringSubmatch(line); m != nil {
			if dist, ok := alias[m[1]]; ok {
				if _, scanned := byDist[dist]; scanned {
					dynamic[dist] = true
				}
			}
		}
		for _, m := range dynImportRe.FindAllStringSubmatch(line, -1) {
			dist := moduleToDist(strings.SplitN(m[1], ".", 2)[0])
			if _, scanned := byDist[dist]; scanned {
				dynamic[dist] = true
			}
		}
	}
}

func addUsage(found map[symKey]*scanner.SymbolUsage, dist string, pkg scanner.ResolvedPackage, family, kind string, conf domain.SymbolConfidence) {
	k := symKey{dist: dist, family: family}
	if u, ok := found[k]; ok {
		if u.Confidence == domain.SymbolUnknown && conf == domain.SymbolProbable {
			u.Confidence = conf
		}
		return
	}
	found[k] = &scanner.SymbolUsage{Package: pkg.PURL, Family: family, Kind: kind, Confidence: conf}
}
