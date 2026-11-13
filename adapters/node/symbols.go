package node

import (
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

var sourceExts = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".mjs": true, ".cjs": true,
}

var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
}

// symbolBudget bounds one symbol scan. The scan runs after every observed
// command, in the MCP server's process too, so an unbounded walk of a large
// project holds a CPU core for as long as it takes (#629, #648).
type symbolBudget struct {
	files    int              // source files read
	fileSize int64            // largest single file read; bigger files are skipped
	bytes    int64            // source bytes read, in total
	elapsed  time.Duration    // wall clock for the whole walk
	now      func() time.Time // nil reads the real clock; tests step a fake one
}

var defaultSymbolBudget = symbolBudget{files: 64, fileSize: 64 << 10, bytes: 512 << 10, elapsed: 250 * time.Millisecond}

// unreadableModes are targets a source read could block on forever, or that
// are not files at all; behind a symlink only a stat of the target shows
// them. ModeIrregular is deliberately absent: on Windows it marks every
// reparse point that is not a link, OneDrive's cloud files among them, and
// those read like any other source file.
const unreadableModes = fs.ModeDir | fs.ModeNamedPipe | fs.ModeSocket | fs.ModeDevice | fs.ModeCharDevice

// Git's own exclude engine handles nested .gitignore files, negations and
// global excludes. The walk below uses this list to prune ignored directories.
func gitSourceSet(ctx context.Context, dir string) (map[string]bool, map[string]bool) {
	if strings.TrimSpace(dir) == "" || (ctx != nil && ctx.Err() != nil) {
		return nil, nil
	}
	dir = filepath.Clean(dir)
	gitCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(gitCtx, "git", "-C", dir, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, nil
	}
	return parseGitSourceSet(ctx, dir, out)
}

func parseGitSourceSet(ctx context.Context, dir string, out []byte) (map[string]bool, map[string]bool) {
	if strings.TrimSpace(dir) == "" || (ctx != nil && ctx.Err() != nil) {
		return nil, nil
	}
	dir = filepath.Clean(dir)
	files, dirs := map[string]bool{}, map[string]bool{}
	for _, rel := range strings.Split(string(out), "\x00") {
		if ctx != nil && ctx.Err() != nil {
			return nil, nil
		}
		if rel == "" {
			continue
		}
		p := filepath.Clean(filepath.Join(dir, filepath.FromSlash(rel)))
		files[p] = true
		addGitParentDirs(ctx, dirs, dir, p)
	}
	if ctx != nil && ctx.Err() != nil {
		return nil, nil
	}
	return files, dirs
}

func addGitParentDirs(ctx context.Context, dirs map[string]bool, dir, p string) {
	dir = filepath.Clean(dir)
	for parent := filepath.Dir(p); parent != dir && parent != "."; {
		if ctx != nil && ctx.Err() != nil {
			return
		}
		dirs[parent] = true
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
}

var (
	reDefNamed  = regexp.MustCompile(`import\s+([A-Za-z_$][\w$]*)\s*,\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"]`)
	reNamespace = regexp.MustCompile(`import\s*\*\s*as\s+([A-Za-z_$][\w$]*)\s+from\s*['"]([^'"]+)['"]`)
	reNamed     = regexp.MustCompile(`import\s+(?:type\s+)?\{([^}]*)\}\s*from\s*['"]([^'"]+)['"]`)
	reDefault   = regexp.MustCompile(`import\s+(?:type\s+)?([A-Za-z_$][\w$]*)\s+from\s*['"]([^'"]+)['"]`)
	reBare      = regexp.MustCompile(`(?m)^[ \t]*import\s*['"]([^'"]+)['"]`)
	reRequire   = regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*require\(\s*['"]([^'"]+)['"]\s*\)`)
	reReqDestr  = regexp.MustCompile(`(?:const|let|var)\s*\{([^}]*)\}\s*=\s*require\(\s*['"]([^'"]+)['"]\s*\)`)
	reDynBind   = regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*await\s+import\(\s*['"]([^'"]+)['"]\s*\)`)
	reDynBare   = regexp.MustCompile(`\bimport\(\s*['"]([^'"]+)['"]\s*\)`)

	reIdent = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
)

// rawUse is one usage found in a single file, keyed by import specifier;
// member == "" means package-level usage only.
type rawUse struct {
	spec   string
	member string
	kind   string
	conf   domain.SymbolConfidence
}

// ScanSymbols statically extracts symbol usages from .ts/.tsx/.js/.mjs/.cjs
// sources, mapping import specifiers to the given resolved packages.
// Static analysis of JS is heuristic, so member usages are at most PROBABLE
// (goal.md §13.2); never EXACT. The walk is bounded by defaultSymbolBudget.
func (Adapter) ScanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	if len(pkgs) == 0 || strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	dir = filepath.Clean(dir)
	cachePath := symbolCachePath(dir, pkgs)
	if cached, ok := readSymbolCache(cachePath); ok {
		return cached, nil
	}
	uses, err := scanSymbols(ctx, dir, pkgs, defaultSymbolBudget)
	if err != nil {
		return nil, err
	}
	writeSymbolCache(cachePath, uses)
	return uses, nil
}

func scanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage, budget symbolBudget) ([]scanner.SymbolUsage, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	dir = filepath.Clean(dir)
	byName := map[string]domain.PURL{}
	for _, p := range pkgs {
		if _, ok := byName[p.PURL.Name]; !ok {
			byName[p.PURL.Name] = p.PURL
		}
	}
	for _, p := range pkgs { // direct deps win name collisions (nested copies)
		if p.Direct {
			byName[p.PURL.Name] = p.PURL
		}
	}

	seen := map[string]bool{}
	var uses []scanner.SymbolUsage
	allowed, allowedDirs := gitSourceSet(ctx, dir)
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
			if path != dir && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if path != dir && allowedDirs != nil && !allowedDirs[path] {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !sourceExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if allowed != nil && !allowed[path] {
			return nil
		}
		// Budgets are charged on the file a read reaches: for a symlink, d
		// describes the link itself, so the target is stat'ed instead.
		info, err := os.Stat(path)
		if err != nil || info.Mode()&unreadableModes != 0 || info.Size() > budget.fileSize || total+info.Size() > budget.bytes {
			return nil // skipped; a smaller file further on may still fit
		}
		data, ok := readAtMost(path, min(budget.fileSize, budget.bytes-total))
		if !ok {
			return nil
		}
		files++
		total += int64(len(data))
		for _, ru := range extractUses(string(data)) {
			pkgName := specToPkgName(ru.spec)
			if pkgName == "" {
				continue
			}
			purl, ok := byName[pkgName]
			if !ok {
				continue
			}
			family := ""
			if ru.member != "" {
				family = pkgName + "." + ru.member
			}
			key := purl.String() + "\x00" + family + "\x00" + ru.kind
			if seen[key] {
				continue
			}
			seen[key] = true
			uses = append(uses, scanner.SymbolUsage{
				Package:    purl,
				Family:     family,
				Kind:       ru.kind,
				Confidence: ru.conf,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(uses, func(i, j int) bool {
		if uses[i].Package.Name != uses[j].Package.Name {
			return uses[i].Package.Name < uses[j].Package.Name
		}
		return uses[i].Family < uses[j].Family
	})
	return uses, nil
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

// extractUses parses one file's import graph and member usages.
func extractUses(content string) []rawUse {
	bindings := map[string]string{} // local ident → specifier
	type namedImport struct{ exported, local, spec string }
	var named []namedImport
	pkgSeen := map[string]bool{}
	pkgHasFamily := map[string]bool{}

	for _, m := range reDefNamed.FindAllStringSubmatch(content, -1) {
		bindings[m[1]] = m[3]
		for _, nl := range parseNamedList(m[2]) {
			named = append(named, namedImport{nl[0], nl[1], m[3]})
		}
		pkgSeen[m[3]] = true
	}
	for _, m := range reNamespace.FindAllStringSubmatch(content, -1) {
		bindings[m[1]] = m[2]
		pkgSeen[m[2]] = true
	}
	for _, m := range reNamed.FindAllStringSubmatch(content, -1) {
		for _, nl := range parseNamedList(m[1]) {
			named = append(named, namedImport{nl[0], nl[1], m[2]})
		}
		pkgSeen[m[2]] = true
	}
	for _, m := range reDefault.FindAllStringSubmatch(content, -1) {
		bindings[m[1]] = m[2]
		pkgSeen[m[2]] = true
	}
	for _, m := range reBare.FindAllStringSubmatch(content, -1) {
		pkgSeen[m[1]] = true
	}
	for _, m := range reRequire.FindAllStringSubmatch(content, -1) {
		bindings[m[1]] = m[2]
		pkgSeen[m[2]] = true
	}
	for _, m := range reReqDestr.FindAllStringSubmatch(content, -1) {
		for _, nl := range parseNamedList(m[1]) {
			named = append(named, namedImport{nl[0], nl[1], m[2]})
		}
		pkgSeen[m[2]] = true
	}
	for _, m := range reDynBind.FindAllStringSubmatch(content, -1) {
		bindings[m[1]] = m[2]
		pkgSeen[m[2]] = true
	}
	for _, m := range reDynBare.FindAllStringSubmatch(content, -1) {
		pkgSeen[m[1]] = true
	}

	// body = content minus the import/require declarations, so usage
	// searches don't match the declarations themselves.
	body := content
	for _, re := range []*regexp.Regexp{
		reDefNamed, reNamespace, reNamed, reDefault, reBare, reRequire, reReqDestr, reDynBind,
	} {
		body = re.ReplaceAllString(body, " ")
	}

	var uses []rawUse
	for local, spec := range bindings {
		// ANY member access, not only an immediate call.
		//
		// This used to require "(" or a template backtick straight after the
		// member, so `axios.defaults.baseURL = x`, `const c = axios.create`,
		// `new pg.Client()` and `if (axios.isCancel)` all recorded nothing.
		// The package-level row already says the package was imported, so a
		// dropped member is the only part that was ever going to be new --
		// and 84% of the most-observed packages carry no symbol evidence.
		//
		// The local name is an import binding, so `local.member` is a member
		// of that package whatever punctuation follows it. Widening the
		// terminator cannot attribute anything to a package nobody imported.
		// The call/reference distinction is real and stays: a member that is
		// invoked is a method, one that is only read is a property.
		reMem := regexp.MustCompile(`\b` + regexp.QuoteMeta(local) + `\.([A-Za-z_$][\w$]*)(\s*[(\x60])?`)
		for _, m := range reMem.FindAllStringSubmatch(body, -1) {
			kind := "property"
			if strings.TrimSpace(m[2]) != "" {
				kind = "method"
			}
			uses = append(uses, rawUse{spec, m[1], kind, domain.SymbolProbable})
			pkgHasFamily[spec] = true
		}
	}
	for _, n := range named {
		reUse := regexp.MustCompile(`\b` + regexp.QuoteMeta(n.local) + `\b`)
		if reUse.MatchString(body) {
			uses = append(uses, rawUse{n.spec, n.exported, "function", domain.SymbolProbable})
			pkgHasFamily[n.spec] = true
		}
	}
	specs := make([]string, 0, len(pkgSeen))
	for spec := range pkgSeen {
		specs = append(specs, spec)
	}
	sort.Strings(specs)
	for _, spec := range specs {
		if !pkgHasFamily[spec] {
			uses = append(uses, rawUse{spec, "", "module", domain.SymbolUnknown})
		}
	}
	return uses
}

// parseNamedList parses "{a, b as c, type T, d: e}" bodies into
// (exported, local) pairs.
func parseNamedList(list string) [][2]string {
	var out [][2]string
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "type ")
		if part == "" {
			continue
		}
		exported, local := part, part
		if a, b, ok := strings.Cut(part, " as "); ok {
			exported, local = strings.TrimSpace(a), strings.TrimSpace(b)
		} else if a, b, ok := strings.Cut(part, ":"); ok {
			exported, local = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		if !reIdent.MatchString(exported) || !reIdent.MatchString(local) {
			continue
		}
		out = append(out, [2]string{exported, local})
	}
	return out
}

// specToPkgName maps an import specifier to its npm package name:
// subpaths drop ("pkg/sub" → "pkg"), scoped keep two segments; relative,
// absolute, and node: builtins map to "".
func specToPkgName(spec string) string {
	if spec == "" || strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") ||
		strings.HasPrefix(spec, "node:") {
		return ""
	}
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") {
		if len(parts) < 2 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}
