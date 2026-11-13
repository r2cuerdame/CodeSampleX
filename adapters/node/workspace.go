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
				for _, item := range strings.Split(strings.Trim(value, "[]"), ",") {
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

// workspaceDeclares reports whether rel matches an inclusion pattern and no
// "!" exclusion pattern. Patterns are path.Match globs per segment; "**"
// matches any number of segments.
func workspaceDeclares(patterns []string, rel string) bool {
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
		return false
	}
	segs := strings.Split(rel, "/")
	included, excluded := false, false
	for _, p := range patterns {
		negated := strings.HasPrefix(p, "!")
		p = path.Clean(strings.TrimPrefix(strings.TrimPrefix(p, "!"), "./"))
		if p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
			continue
		}
		if matchWorkspaceSegments(strings.Split(p, "/"), segs) {
			if negated {
				excluded = true
			} else {
				included = true
			}
		}
	}
	return included && !excluded
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
