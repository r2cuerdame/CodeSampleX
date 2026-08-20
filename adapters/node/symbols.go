package node

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

var sourceExts = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".mjs": true, ".cjs": true,
}

var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
}

const maxSourceFileSize = 1 << 20

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
// (goal.md §13.2); never EXACT.
func (Adapter) ScanSymbols(ctx context.Context, dir string, pkgs []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	if len(pkgs) == 0 {
		return nil, nil
	}
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

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if path != dir && skipDirs[d.Name()] {
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
		if info, err := d.Info(); err != nil || info.Size() > maxSourceFileSize {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
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
