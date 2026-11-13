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
	tomlSectionHeaderRe = regexp.MustCompile(`(?m)^\[([^\[\]\r\n]+)\]\s*(?:#.*)?$`)
	tomlTableHeaderRe   = regexp.MustCompile(`(?m)^\s*\[\[?[^\r\n]+\]\]?\s*(?:#.*)?$`)
	depsKeyRe           = regexp.MustCompile(`(?m)^[ \t]*dependencies[ \t]*=`)
	depNameRe           = regexp.MustCompile(`^\s*([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)`)
	assignmentKeyRe     = regexp.MustCompile(`(?m)^[ \t]*(?:"([^"]+)"|'([^']+)'|([A-Za-z0-9][A-Za-z0-9._-]*))[ \t]*=`)
)

// pyprojectDirectDeps returns the PEP 503-normalized direct dependencies in
// PEP 621 and Poetry sections. ok is false when no supported dependency
// declaration exists, in which case the caller falls back to requirements.
func pyprojectDirectDeps(path string) (deps map[string]bool, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	deps = make(map[string]bool)

	sections := tomlSections(data)
	if section, exists := sections["project"]; exists {
		if loc := depsKeyRe.FindStringIndex(section); loc != nil {
			addRequirementNames(deps, section[loc[1]:])
			ok = true
		}
	}
	if section, exists := sections["project.optional-dependencies"]; exists {
		for pos := 0; pos < len(section); {
			loc := assignmentKeyRe.FindStringSubmatchIndex(section[pos:])
			if loc == nil {
				break
			}
			ok = true
			afterEq := pos + loc[1]
			items, end := arrayItems(section[afterEq:])
			addRequirementItems(deps, items)
			pos = nextLineStart(section, afterEq+end)
		}
	}
	for name, section := range sections {
		if name != "tool.poetry.dependencies" && name != "tool.poetry.dev-dependencies" &&
			!(strings.HasPrefix(name, "tool.poetry.group.") && strings.HasSuffix(name, ".dependencies")) {
			continue
		}
		for _, key := range assignmentKeyRe.FindAllStringSubmatch(section, -1) {
			dep := key[1]
			if dep == "" {
				dep = key[2]
			}
			if dep == "" {
				dep = key[3]
			}
			// Poetry uses this key to describe the supported interpreter, not
			// a distribution to install from PyPI.
			if strings.EqualFold(dep, "python") {
				continue
			}
			deps[normalizeDist(dep)] = true
		}
		ok = true
	}
	return deps, ok
}

// tomlSections returns the contents of each single-bracket TOML table.
// Every table header, including array-of-table headers, ends the preceding
// table even though array-of-table contents are not collected here.
func tomlSections(data []byte) map[string]string {
	locations := tomlSectionHeaderRe.FindAllSubmatchIndex(data, -1)
	boundaries := tomlTableHeaderRe.FindAllStringIndex(string(data), -1)
	sections := make(map[string]string, len(locations))
	for _, loc := range locations {
		name := strings.TrimSpace(string(data[loc[2]:loc[3]]))
		start, end := loc[1], len(data)
		for _, boundary := range boundaries {
			if boundary[0] > loc[0] {
				end = boundary[0]
				break
			}
		}
		sections[name] = string(data[start:end])
	}
	return sections
}

func addRequirementNames(deps map[string]bool, value string) {
	items, _ := arrayItems(value)
	addRequirementItems(deps, items)
}

func addRequirementItems(deps map[string]bool, items []string) {
	for _, item := range items {
		if nm := depNameRe.FindStringSubmatch(item); nm != nil {
			deps[normalizeDist(nm[1])] = true
		}
	}
}

func nextLineStart(s string, pos int) int {
	if pos >= len(s) {
		return len(s)
	}
	if nl := strings.IndexByte(s[pos:], '\n'); nl >= 0 {
		return pos + nl + 1
	}
	return len(s)
}

// arrayItems reads top-level quoted strings from a bracketed TOML array at the
// start of s, ignoring # comments and keeping #, brackets and escaped quotes
// inside strings from acting as comments or array boundaries.
func arrayItems(s string) ([]string, int) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= len(s) || s[i] != '[' {
		return nil, 0
	}
	var items []string
	depth := 0
	for i < len(s) {
		switch s[i] {
		case '[':
			depth++
			i++
		case ']':
			depth--
			if depth == 0 {
				return items, i + 1
			}
			i++
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '"':
			item, next, ok := readBasicString(s, i)
			if ok && depth == 1 {
				items = append(items, item)
			}
			i = next
		case '\'':
			item, next, ok := readLiteralString(s, i)
			if ok && depth == 1 {
				items = append(items, item)
			}
			i = next
		default:
			i++
		}
	}
	return items, len(s)
}

func readBasicString(s string, start int) (string, int, bool) {
	var b strings.Builder
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 >= len(s) {
				return "", len(s), false
			}
			i++
			switch s[i] {
			case '"', '\\', '/':
				b.WriteByte(s[i])
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		case '"':
			return b.String(), i + 1, true
		case '\r', '\n':
			return "", i, false
		default:
			b.WriteByte(s[i])
		}
	}
	return "", len(s), false
}

func readLiteralString(s string, start int) (string, int, bool) {
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\'':
			return s[start+1 : i], i + 1, true
		case '\r', '\n':
			return "", i, false
		}
	}
	return "", len(s), false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
