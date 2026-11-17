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

// The scan runs after every observed command, in the MCP server's process
// too, so an unbounded walk would hold a CPU core (#629, #648).
type symbolBudget struct {
	files    int
	fileSize int64
	bytes    int64
	elapsed  time.Duration
	now      func() time.Time
}

var defaultSymbolBudget = symbolBudget{files: 64, fileSize: 64 << 10, bytes: 512 << 10, elapsed: 250 * time.Millisecond}

// unreadableModes are targets a source read could block on forever, or that
// are not files at all; behind a symlink only a stat of the target shows
// them. ModeIrregular is deliberately absent: on Windows it marks every
// reparse point that is not a link, OneDrive's cloud files among them, and
// those read like any other source file.
const unreadableModes = fs.ModeDir | fs.ModeNamedPipe | fs.ModeSocket | fs.ModeDevice | fs.ModeCharDevice

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

type rawUse struct {
	spec   string
	member string
	kind   string
	conf   domain.SymbolConfidence
}

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
	for _, p := range pkgs {
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
			return nil
		}
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
		// For a symlink d describes the link itself; budgets charge the target.
		info, err := os.Stat(path)
		if err != nil || info.Mode()&unreadableModes != 0 || info.Size() > budget.fileSize || total+info.Size() > budget.bytes {
			return nil
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

// The file can grow, or a link be re-pointed, after the stat pre-check.
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

func extractUses(content string) []rawUse {
	content = jsCode(content, false)
	bindings := map[string]string{}
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

	body := content
	for _, re := range []*regexp.Regexp{
		reDefNamed, reNamespace, reNamed, reDefault, reBare, reRequire, reReqDestr, reDynBind,
	} {
		body = re.ReplaceAllString(body, " ")
	}
	body = jsCode(body, true)

	var uses []rawUse
	for local, spec := range bindings {
		reMem := regexp.MustCompile(regexp.QuoteMeta(local) + `\.([A-Za-z_$][\w$]*)(\s*[(\x60])?`)
		for _, m := range findMemberUses(body, reMem) {
			kind := "property"
			if strings.TrimSpace(m[2]) != "" {
				kind = "method"
			}
			uses = append(uses, rawUse{spec, m[1], kind, domain.SymbolProbable})
			pkgHasFamily[spec] = true
		}
	}
	for _, n := range named {
		if containsJSIdent(body, n.local) {
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

func findMemberUses(body string, re *regexp.Regexp) [][]string {
	var out [][]string
	for off := 0; off < len(body); {
		loc := re.FindStringSubmatchIndex(body[off:])
		if loc == nil {
			break
		}
		start, end := off+loc[0], off+loc[1]
		if start > 0 && isJSIdentByte(body[start-1]) {
			off = start + 1
			continue
		}
		m := make([]string, len(loc)/2)
		for i := range m {
			if loc[2*i] >= 0 {
				m[i] = body[off+loc[2*i] : off+loc[2*i+1]]
			}
		}
		out = append(out, m)
		off = end
	}
	return out
}

func containsJSIdent(body, name string) bool {
	if name == "" {
		return false
	}
	for off := 0; ; {
		i := strings.Index(body[off:], name)
		if i < 0 {
			return false
		}
		start, end := off+i, off+i+len(name)
		if (start == 0 || !isJSIdentByte(body[start-1])) && (end == len(body) || !isJSIdentByte(body[end])) {
			return true
		}
		off = start + 1
	}
}

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
