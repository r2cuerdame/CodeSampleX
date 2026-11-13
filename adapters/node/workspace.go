package node

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxWorkspaceClimb bounds how many directories above dir are searched for
// the workspace root that owns dir's lockfile.
const maxWorkspaceClimb = 8

// maxWorkspaceBraceExpansions caps brace-alternative expansion so crafted
// patterns with many "{a,b}" groups cannot exhaust memory or CPU.
const maxWorkspaceBraceExpansions = 1024

// firstLockfile names the first supported lockfile present in dir, in the
// same order ScanPackages reads them, or "" when there is none.
func firstLockfile(dir string) string {
	for _, lp := range lockParsers {
		if fileExists(filepath.Join(dir, lp.file)) {
			return lp.file
		}
	}
	return ""
}

// workspaceRoot finds the closest ancestor of dir that holds a supported
// lockfile and returns it only when that ancestor declares dir as one of its
// workspaces. Monorepos keep a single lockfile at the root while tests and
// builds run inside each member, so a member directory has no lockfile of its
// own. member is dir relative to root with forward slashes ("apps/web").
//
// An unrelated ancestor lockfile (a stray one in a home directory, say) is
// never borrowed: the search stops at the closest lockfile either way, and
// that root must list dir in its workspace patterns.
func workspaceRoot(dir string) (root, member, lockfile string, ok bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", "", false
	}
	cur := abs
	for range maxWorkspaceClimb {
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
		file := firstLockfile(cur)
		if file == "" {
			continue
		}
		rel, err := filepath.Rel(cur, abs)
		if err != nil {
			return "", "", "", false
		}
		rel = filepath.ToSlash(rel)
		patterns := packageJSONWorkspaces(cur)
		if file == "pnpm-lock.yaml" {
			patterns = pnpmWorkspacePatterns(cur)
		}
		if !workspaceDeclares(patterns, rel) {
			return "", "", "", false
		}
		return cur, rel, file, true
	}
	return "", "", "", false
}

// packageJSONWorkspaces reads the npm/yarn/bun "workspaces" field, either
// an array of patterns or an object with a "packages" array.
func packageJSONWorkspaces(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pj struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if json.Unmarshal(data, &pj) != nil || len(pj.Workspaces) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(pj.Workspaces, &list) == nil {
		return list
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(pj.Workspaces, &obj) == nil {
		return obj.Packages
	}
	return nil
}

// pnpmWorkspacePatterns reads the "packages" list of pnpm-workspace.yaml in
// block ("- 'apps/*'") or flow ("['apps/*']") form. Like parsePnpmLock it is a
// purpose-built line reader rather than a YAML dependency.
func pnpmWorkspacePatterns(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "pnpm-workspace.yaml"))
	if err != nil {
		return nil
	}
	var out []string
	inPackages := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if i := strings.Index(trimmed, " #"); i >= 0 {
			trimmed = strings.TrimSpace(trimmed[:i])
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "-") {
			key, value, _ := strings.Cut(trimmed, ":")
			inPackages = strings.TrimSpace(key) == "packages"
			if value = strings.TrimSpace(value); inPackages && strings.HasPrefix(value, "[") {
				inner := strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
				for _, item := range splitYAMLFlowList(inner) {
					if item = unquoteYAML(item); item != "" {
						out = append(out, item)
					}
				}
				inPackages = false
			}
			continue
		}
		if item, ok := strings.CutPrefix(trimmed, "-"); inPackages && ok {
			if item = unquoteYAML(item); item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

// splitYAMLFlowList splits a YAML flow-sequence body on top-level commas,
// ignoring commas inside quotes or "{a,b}" brace groups.
func splitYAMLFlowList(s string) []string {
	var items []string
	var quote byte
	braceDepth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if quote == '"' && c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if quote == '\'' && c == '\'' && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '{':
			braceDepth++
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case ',':
			if braceDepth == 0 {
				items = append(items, s[start:i])
				start = i + 1
			}
		}
	}
	items = append(items, s[start:])
	return items
}

// workspaceDeclares reports whether rel matches an inclusion pattern and no
// "!" exclusion pattern. Patterns expand "{a,b}" brace alternatives and match
// path.Match globs per segment; "**" matches any number of segments.
func workspaceDeclares(patterns []string, rel string) bool {
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
		return false
	}
	segs := strings.Split(rel, "/")
	included, excluded := false, false
	for _, p := range patterns {
		negated := strings.HasPrefix(p, "!")
		raw := strings.TrimPrefix(p, "!")
		for _, expanded := range expandBraces(raw) {
			cleaned := path.Clean(strings.TrimPrefix(expanded, "./"))
			if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
				continue
			}
			if matchWorkspaceSegments(strings.Split(cleaned, "/"), segs) {
				if negated {
					excluded = true
				} else {
					included = true
				}
				break
			}
		}
	}
	return included && !excluded
}

// expandBraces expands "{a,b}" brace alternatives recursively (including nested
// and sequential brace groups) up to maxWorkspaceBraceExpansions variants.
// Literal braces without a comma, unclosed braces, escaped "\{", and braces
// inside "[...]" character classes stay intact for path.Match.
func expandBraces(pattern string) []string {
	out := make([]string, 0, 1)
	expandBracesInto(pattern, &out, maxWorkspaceBraceExpansions)
	return out
}

func expandBracesInto(s string, out *[]string, limit int) {
	if len(*out) >= limit {
		return
	}
	i, commas, end := findBraceGroup(s)
	if end < 0 {
		*out = append(*out, s)
		return
	}
	prefix := s[:i]
	suffix := s[end+1:]
	prev := i + 1
	for _, comma := range append(commas, end) {
		if len(*out) >= limit {
			return
		}
		expandBracesInto(prefix+s[prev:comma]+suffix, out, limit)
		prev = comma + 1
	}
}

func findBraceGroup(s string) (start int, commas []int, end int) {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if s[i] == '[' {
			if closeIdx := findCharClassEnd(s, i+1); closeIdx >= 0 {
				i = closeIdx
			}
			continue
		}
		if s[i] != '{' {
			continue
		}
		depth := 1
		var topCommas []int
		matchedEnd := -1
		for j := i + 1; j < len(s) && matchedEnd < 0; j++ {
			if s[j] == '\\' && j+1 < len(s) {
				j++
				continue
			}
			switch s[j] {
			case '[':
				if closeIdx := findCharClassEnd(s, j+1); closeIdx >= 0 {
					j = closeIdx
				}
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					matchedEnd = j
				}
			case ',':
				if depth == 1 {
					topCommas = append(topCommas, j)
				}
			}
		}
		if matchedEnd >= 0 && len(topCommas) > 0 {
			return i, topCommas, matchedEnd
		}
	}
	return 0, nil, -1
}

func findCharClassEnd(s string, start int) int {
	for i := start; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if s[i] == ']' {
			return i
		}
	}
	return -1
}

func matchWorkspaceSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchWorkspaceSegments(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	return err == nil && ok && matchWorkspaceSegments(pat[1:], segs[1:])
}
