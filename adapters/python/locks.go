package python

import (
	"net/url"
	"os"
	"regexp"
	"strings"
)

// lockEntry is one dependency read from a lock source before it becomes a
// scanner.ResolvedPackage.
type lockEntry struct {
	Name    string // as written in the file; normalized later
	Version string // empty for private path/vcs references
	Private bool
}

var (
	tomlNameRe    = regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)
	tomlVersionRe = regexp.MustCompile(`(?m)^version\s*=\s*"([^"]+)"`)

	// uv.lock: [[package]] with an inline source table; anything that is not
	// a registry source is local or vcs and therefore private (§25.E).
	uvPrivateSourceRe = regexp.MustCompile(`(?m)^source\s*=\s*\{[^}]*\b(?:editable|virtual|directory|path|git)\b`)
	// poetry.lock: private sources appear as [package.source] type = "...".
	poetryPrivateSourceRe = regexp.MustCompile(`(?m)^type\s*=\s*"(?:directory|file|git|url)"`)

	// Strict pin: name, optional extras, "==", version. Anything else is a
	// range and yields no resolved version (§7.1).
	// The version class deliberately excludes "*". PEP 440 makes `==4.2.*`
	// a PREFIX MATCH — a range, not a pin — and accepting it recorded
	// observations under the literal version string "4.2.*", which no
	// release has ever had, while grading treated it as exact. `django==4.2.*`
	// and `flask==3.*` are among the most common lines in a requirements.txt.
	reqPinRe = regexp.MustCompile(`^([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)(?:\[[^\]]*\])?\s*==\s*([A-Za-z0-9.!+]+)$`)

	winPathRe = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

	pep503Re = regexp.MustCompile(`[-_.]+`)
)

// normalizeDist applies PEP 503 name normalization: lowercase, runs of
// '-', '_' and '.' collapse to a single '-'.
func normalizeDist(name string) string {
	return pep503Re.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
}

// parseLockPackages extracts name/version pairs from [[package]] blocks of
// uv.lock or poetry.lock. Regex-based on purpose: both files are generated
// with a stable line layout and a full TOML parser would add a dependency.
// publicIndexHosts are the canonical public Python indexes. A package
// resolved from anywhere else came from an index this machine was
// configured for, which is private information about where the
// contributor works.
var publicIndexHosts = map[string]bool{
	"pypi.org":               true,
	"pypi.python.org":        true,
	"files.pythonhosted.org": true,
}

// lockSourceURLRe pulls the index URL out of a lock entry's source table:
// uv writes source = { registry = "https://…" }, poetry writes url = "https://…"
// under [package.source].
var lockSourceURLRe = regexp.MustCompile(`(?m)(?:registry|url)[ 	]*=[ 	]*"(https?://[^"]+)"`)

// fromPrivateIndex reports whether a lock entry resolved from an index that
// is not a canonical public one.
//
// The source patterns only caught editable, path, git and directory
// entries. A package pulled from a company Artifactory looks like an
// ordinary registry entry, so it was reported UNKNOWN, the registry
// checker then found the NAME on public PyPI and upgraded it to PUBLIC,
// and an observation went out for pkg:pypi/requests@2.31.0-corp1 — an
// internal build string leaving the machine, and false public evidence for
// a version PyPI never published.
//
// An unrecognised host is treated as private. Over-caution costs some
// evidence; under-caution sends where someone works to a public network.
func fromPrivateIndex(block string) bool {
	m := lockSourceURLRe.FindStringSubmatch(block)
	if m == nil {
		return false
	}
	u, err := url.Parse(m[1])
	if err != nil {
		return true
	}
	return !publicIndexHosts[strings.ToLower(u.Hostname())]
}

func parseLockPackages(data []byte, privateRe *regexp.Regexp) []lockEntry {
	var out []lockEntry
	blocks := strings.Split(string(data), "[[package]]")
	for _, b := range blocks[1:] {
		name := firstGroup(tomlNameRe, b)
		if name == "" {
			continue
		}
		out = append(out, lockEntry{
			Name:    name,
			Version: firstGroup(tomlVersionRe, b),
			Private: privateRe.MatchString(b) || fromPrivateIndex(b),
		})
	}
	return out
}

func firstGroup(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// parseRequirements reads a requirements.txt. Only strict `==` pins produce
// resolved versions; editable/path/vcs/file references are PRIVATE and never
// leave the machine; every other line (options, ranges) is ignored.
func parseRequirements(data []byte) []lockEntry {
	var out []lockEntry
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if i := strings.Index(line, ";"); i >= 0 { // environment marker
			line = strings.TrimSpace(line[:i])
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case fields[0] == "-e" || fields[0] == "--editable":
			ref := ""
			if len(fields) > 1 {
				ref = fields[len(fields)-1]
			}
			out = append(out, lockEntry{Name: refName(ref), Private: true})
			continue
		case strings.HasPrefix(fields[0], "-"): // -r, --index-url, ...
			continue
		case strings.Contains(lower, "git+") || strings.Contains(lower, "file:") ||
			strings.HasPrefix(line, "./") || strings.HasPrefix(line, "../") ||
			strings.HasPrefix(line, "/") || strings.HasPrefix(line, "~") ||
			winPathRe.MatchString(line):
			out = append(out, lockEntry{Name: refName(line), Private: true})
			continue
		}
		if m := reqPinRe.FindStringSubmatch(line); m != nil {
			out = append(out, lockEntry{Name: m[1], Version: m[2]})
		}
	}
	return out
}

// refName derives a best-effort name for a private path/vcs reference. The
// result stays local (PRIVATE entries are never uploaded), it only keys the
// local exclusion row.
func refName(ref string) string {
	if i := strings.Index(ref, "#egg="); i >= 0 {
		return strings.TrimSpace(ref[i+len("#egg="):])
	}
	if i := strings.Index(ref, "@"); i > 0 && !strings.ContainsAny(ref[:i], "/\\") {
		return strings.TrimSpace(ref[:i]) // "name @ file://..." direct reference
	}
	ref = strings.TrimRight(ref, "/\\")
	if i := strings.LastIndexAny(ref, "/\\"); i >= 0 {
		ref = ref[i+1:]
	}
	ref = strings.TrimSuffix(ref, ".git")
	if ref == "" || ref == "." || ref == ".." {
		return "local"
	}
	return ref
}

var (
	projectSectionRe = regexp.MustCompile(`(?ms)^\[project\]\s*$(.*?)(?:^\[|\z)`)
	depsKeyRe        = regexp.MustCompile(`(?m)^dependencies\s*=`)
	quotedRe         = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)
	depNameRe        = regexp.MustCompile(`^\s*([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)`)
)

// pyprojectDirectDeps returns the PEP 503-normalized names listed in
// pyproject [project] dependencies. ok is false when the file or the key is
// absent, in which case the caller falls back to requirements lines.
func pyprojectDirectDeps(path string) (deps map[string]bool, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	m := projectSectionRe.FindSubmatch(data)
	if m == nil {
		return nil, false
	}
	section := string(m[1])
	loc := depsKeyRe.FindStringIndex(section)
	if loc == nil {
		return nil, false
	}
	deps = make(map[string]bool)
	for _, q := range quotedRe.FindAllStringSubmatch(arrayBody(section[loc[0]:]), -1) {
		item := q[1]
		if item == "" {
			item = q[2]
		}
		if nm := depNameRe.FindStringSubmatch(item); nm != nil {
			deps[normalizeDist(nm[1])] = true
		}
	}
	return deps, true
}

// arrayBody returns the contents of the first bracket-balanced [...] in s.
func arrayBody(s string) string {
	start := strings.Index(s, "[")
	if start < 0 {
		return ""
	}
	depth := 0
	for j := start; j < len(s); j++ {
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return s[start+1 : j]
			}
		}
	}
	return s[start+1:]
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
