package python

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

// symbolBudget bounds one symbol scan. The scan runs after every observed
// command, in the MCP server's process too, so an unbounded walk of a large
// project holds a CPU core for as long as it takes (#629, #645).
type symbolBudget struct {
	files    int              // source files read
	fileSize int64            // largest single file read; bigger files are skipped
	bytes    int64            // source bytes read, in total
	elapsed  time.Duration    // wall clock for the whole walk
	now      func() time.Time // nil reads the real clock; tests step a fake one
}

// The node and rust adapters' budget: like theirs, this is a regex pass over
// a file's imports, not a parse.
var defaultSymbolBudget = symbolBudget{files: 64, fileSize: 64 << 10, bytes: 512 << 10, elapsed: 250 * time.Millisecond}

// unreadableModes are targets a source read could block on forever, or that
// are not files at all; behind a symlink only a stat of the target shows
// them. ModeIrregular is deliberately absent: on Windows it marks every
// reparse point that is not a link, OneDrive's cloud files among them, and
// those read like any other source file.
const unreadableModes = fs.ModeDir | fs.ModeNamedPipe | fs.ModeSocket | fs.ModeDevice | fs.ModeCharDevice

// ScanSymbols regex-scans project *.py files for import statements against
// the scanned package set. `from mod import a` yields family "mod.a"
// PROBABLE; a plain `import mod` yields a package-level UNKNOWN usage.
// Dynamic access (getattr on an imported module, importlib.import_module,
// __import__) degrades every symbol of that package to UNKNOWN (§13.3).
// The walk is bounded by defaultSymbolBudget.
func (*Adapter) ScanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	return scanSymbols(ctx, dir, pkgs, defaultSymbolBudget)
}

func scanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage, budget symbolBudget) ([]scanner.SymbolUsage, error) {
	byDist := make(map[string]scanner.ResolvedPackage, len(pkgs))
	for _, p := range pkgs {
		byDist[normalizeDist(p.PURL.Name)] = p
	}
	found := map[symKey]*scanner.SymbolUsage{}
	dynamic := map[string]bool{}

	now := budget.now
	if now == nil {
		now = time.Now
	}
	start := now()
	files, total := 0, int64(0)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		// A spent budget ends the walk, not the scan: every usage already
		// recorded was really seen in a file, so it is kept.
		if files >= budget.files || total >= budget.bytes || now().Sub(start) >= budget.elapsed {
			return filepath.SkipAll
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
		// Budgets are charged on the file a read reaches: for a symlink, d
		// describes the link itself, so the target is stat'ed instead.
		info, rerr := os.Stat(path)
		if rerr != nil || info.Mode()&unreadableModes != 0 || info.Size() > budget.fileSize || total+info.Size() > budget.bytes {
			return nil // skipped; a smaller file further on may still fit
		}
		data, ok := readAtMost(path, min(budget.fileSize, budget.bytes-total))
		if !ok {
			return nil
		}
		files++
		total += int64(len(data))
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
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, false
	}
	return data, true
}

func scanPyFile(src string, byDist map[string]scanner.ResolvedPackage, found map[symKey]*scanner.SymbolUsage, dynamic map[string]bool) {
	alias := map[string]string{} // bound local name -> dist name
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(strings.TrimSuffix(lines[i], "\r"))
		// Python permits parenthesized from-import lists to span several lines.
		// Join only that statement so import parsing still sees a single line.
		if m := fromLineRe.FindStringSubmatch(line); m != nil && strings.Contains(m[2], "(") && !strings.Contains(m[2], ")") {
			for i++; i < len(lines); i++ {
				line += " " + strings.TrimSpace(strings.TrimSuffix(lines[i], "\r"))
				if strings.Contains(lines[i], ")") {
					break
				}
			}
		}

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
