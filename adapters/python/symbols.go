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
	modPathRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	identOnlyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
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

// The node and rust adapters' budget: like theirs, this is one linear pass
// over a file, not a parse.
var defaultSymbolBudget = symbolBudget{files: 64, fileSize: 64 << 10, bytes: 512 << 10, elapsed: 250 * time.Millisecond}

// unreadableModes are targets a source read could block on forever, or that
// are not files at all; behind a symlink only a stat of the target shows
// them. ModeIrregular is deliberately absent: on Windows it marks every
// reparse point that is not a link, OneDrive's cloud files among them, and
// those read like any other source file.
const unreadableModes = fs.ModeDir | fs.ModeNamedPipe | fs.ModeSocket | fs.ModeDevice | fs.ModeCharDevice

// ScanSymbols lexes project *.py files for import statements against
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

// scanPyFile reads one file statement by statement (pyStatements), so a
// comment or a string literal, docstrings included, never reads as an import
// or a dynamic access, and a statement spanning lines in brackets or after a
// backslash is read whole.
func scanPyFile(src string, byDist map[string]scanner.ResolvedPackage, found map[symKey]*scanner.SymbolUsage, dynamic map[string]bool) {
	alias := map[string]string{} // bound local name -> dist name
	pyStatements(src, func(st []pyTok) {
		switch {
		case isPyWord(st, 0, "import"):
			scanPyImport(st[1:], byDist, found, alias)
		case isPyWord(st, 0, "from"):
			scanPyFrom(st[1:], byDist, found)
		default:
			scanPyDynamic(st, byDist, alias, dynamic)
		}
	})
}

// scanPyImport reads the items of `import a.b, c as d`. An item that is not a
// dotted module path with an optional `as name` is skipped whole.
func scanPyImport(toks []pyTok, byDist map[string]scanner.ResolvedPackage, found map[symKey]*scanner.SymbolUsage, alias map[string]string) {
	for _, item := range splitPyOp(toks, ",") {
		mod, k := pyDotted(item, 0)
		if mod == "" || !modPathRe.MatchString(mod) {
			continue
		}
		top := strings.SplitN(mod, ".", 2)[0]
		bound := top
		switch {
		case k == len(item):
		case k+2 == len(item) && isPyWord(item, k, "as") && item[k+1].kind == pyName && identOnlyRe.MatchString(item[k+1].text):
			bound = item[k+1].text
		default:
			continue
		}
		dist := moduleToDist(top)
		alias[bound] = dist
		if pkg, ok := byDist[dist]; ok {
			addUsage(found, dist, pkg, mod, "module", domain.SymbolUnknown)
		}
	}
}

// scanPyFrom reads `from mod import a, b as c` and its parenthesized form.
// A relative import (leading dot) has no module path and is skipped; anything
// after a closing parenthesis is not an imported name.
func scanPyFrom(toks []pyTok, byDist map[string]scanner.ResolvedPackage, found map[symKey]*scanner.SymbolUsage) {
	mod, k := pyDotted(toks, 0)
	if mod == "" || !modPathRe.MatchString(mod) || !isPyWord(toks, k, "import") {
		return
	}
	top := strings.SplitN(mod, ".", 2)[0]
	dist := moduleToDist(top)
	pkg, ok := byDist[dist]
	if !ok {
		return
	}
	names := toks[k+1:]
	if len(names) > 0 && names[0].kind == pyOp && names[0].text == "(" {
		end := 1
		for end < len(names) && !(names[end].kind == pyOp && names[end].text == ")") {
			end++
		}
		names = names[1:end]
	}
	for _, item := range splitPyOp(names, ",") {
		switch {
		case len(item) == 1 && item[0].kind == pyOp && item[0].text == "*":
			addUsage(found, dist, pkg, mod, "module", domain.SymbolUnknown)
		case (len(item) == 1 || len(item) == 3 && isPyWord(item, 1, "as") && item[2].kind == pyName) &&
			item[0].kind == pyName && identOnlyRe.MatchString(item[0].text):
			addUsage(found, dist, pkg, mod+"."+item[0].text, "property", domain.SymbolProbable)
		}
	}
}

// scanPyDynamic marks a scanned dist dynamic on getattr(<bound name>, ...),
// or on importlib.import_module / __import__ called with a plain string
// literal naming its module (§13.3).
func scanPyDynamic(st []pyTok, byDist map[string]scanner.ResolvedPackage, alias map[string]string, dynamic map[string]bool) {
	mark := func(dist string) {
		if _, scanned := byDist[dist]; scanned {
			dynamic[dist] = true
		}
	}
	for i := range st {
		switch {
		case isPyWord(st, i, "getattr") && isPyOp(st, i+1, "(") && i+2 < len(st) && st[i+2].kind == pyName && isPyOp(st, i+3, ","):
			if dist, ok := alias[st[i+2].text]; ok {
				mark(dist)
			}
		case isPyWord(st, i, "__import__") && isPyOp(st, i+1, "("):
			if m, ok := pyModLiteral(st, i+2); ok {
				mark(moduleToDist(strings.SplitN(m, ".", 2)[0]))
			}
		case isPyWord(st, i, "importlib") && isPyOp(st, i+1, ".") && isPyWord(st, i+2, "import_module") && isPyOp(st, i+3, "("):
			if m, ok := pyModLiteral(st, i+4); ok {
				mark(moduleToDist(strings.SplitN(m, ".", 2)[0]))
			}
		}
	}
}

// pyModLiteral returns the module path a non-interpolated string literal at
// toks[i] holds.
func pyModLiteral(toks []pyTok, i int) (string, bool) {
	if i >= len(toks) || toks[i].kind != pyStr || toks[i].interp || !modPathRe.MatchString(toks[i].text) {
		return "", false
	}
	return toks[i].text, true
}

// pyDotted reads a dotted name starting at toks[i] and returns it with the
// index just past it; a trailing dot is left unread. It returns "" when
// toks[i] is not a name.
func pyDotted(toks []pyTok, i int) (string, int) {
	if i >= len(toks) || toks[i].kind != pyName {
		return "", i
	}
	path := toks[i].text
	i++
	for isPyOp(toks, i, ".") && i+1 < len(toks) && toks[i+1].kind == pyName {
		path += "." + toks[i+1].text
		i += 2
	}
	return path, i
}

// splitPyOp splits toks at each op token sep, dropping empty parts.
func splitPyOp(toks []pyTok, sep string) [][]pyTok {
	var parts [][]pyTok
	start := 0
	for i := 0; i <= len(toks); i++ {
		if i == len(toks) || isPyOp(toks, i, sep) {
			if i > start {
				parts = append(parts, toks[start:i])
			}
			start = i + 1
		}
	}
	return parts
}

func isPyWord(toks []pyTok, i int, word string) bool {
	return i < len(toks) && toks[i].kind == pyName && toks[i].text == word
}

func isPyOp(toks []pyTok, i int, op string) bool {
	return i < len(toks) && toks[i].kind == pyOp && toks[i].text == op
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
