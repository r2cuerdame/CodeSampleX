package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// lockDep is one lockfile-resolved dependency before conversion to
// scanner.ResolvedPackage.
type lockDep struct {
	Name    string
	Version string
	Direct  bool
	Private bool
}

// lockParser parses one lockfile kind. dir is the scanned directory; member
// is dir relative to the workspace root that owns the lockfile ("apps/web"),
// or "" when the lockfile is dir's own. Direct dependencies are the ones the
// scanned directory declares, so a member's are read from its own entry.
type lockParser struct {
	file  string
	parse func(data []byte, dir, member string) ([]lockDep, error)
}

var lockParsers = []lockParser{
	{"package-lock.json", parsePackageLock},
	{"pnpm-lock.yaml", parsePnpmLock},
	{"yarn.lock", parseYarnLock},
	{"bun.lock", parseBunLock},
}

// ScanPackages reads the first lockfile found (package-lock.json,
// pnpm-lock.yaml, yarn.lock, bun.lock) and returns resolved versions. When
// dir has none, the lockfile of a workspace root that declares dir as a
// member is used instead (see workspaceRoot). No lockfile ⇒ empty result:
// manifest ranges are never reported as resolved (goal.md §7.1).
func (Adapter) ScanPackages(ctx context.Context, dir string) ([]scanner.ResolvedPackage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, lp := range lockParsers {
		data, err := os.ReadFile(filepath.Join(dir, lp.file))
		if err != nil {
			continue
		}
		deps, err := lp.parse(data, dir, "")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", lp.file, err)
		}
		return toResolved(deps, lp.file), nil
	}
	root, member, file, ok := workspaceRoot(dir)
	if !ok {
		return nil, nil
	}
	for _, lp := range lockParsers {
		if lp.file != file {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, lp.file))
		if err != nil {
			return nil, nil
		}
		deps, err := lp.parse(data, dir, member)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", lp.file, err)
		}
		return toResolved(deps, lp.file), nil
	}
	return nil, nil
}

// parseBunLock reads Bun's text lockfile, never the legacy binary bun.lockb.
// Bun hoists to top-level keys and records a workspace's conflicting version
// under "<workspace name>/<dependency>", which a member resolves first.
func parseBunLock(data []byte, _, member string) ([]lockDep, error) {
	clean, err := cleanBunJSON(data)
	if err != nil {
		return nil, err
	}
	var lock struct {
		Workspaces map[string]npmLockEntry      `json:"workspaces"`
		Packages   map[string][]json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(clean, &lock); err != nil {
		return nil, err
	}
	if lock.Packages == nil {
		return nil, fmt.Errorf("missing packages map")
	}
	root, ok := lock.Workspaces[member]
	if !ok && member != "" {
		return nil, nil // a stale lockfile that predates this member proves nothing about it
	}
	nested := ""
	if member != "" && root.Name != "" {
		nested = root.Name + "/"
	}
	directSpecs := map[string]string{}
	for _, specs := range []map[string]string{root.Dependencies, root.DevDependencies, root.OptionalDependencies, root.PeerDependencies} {
		for name, spec := range specs {
			addDirectSpec(directSpecs, name, spec)
		}
	}
	var deps []lockDep
	seen := map[string]int{}
	for key, entry := range lock.Packages {
		var coordinate string
		if len(entry) == 0 || json.Unmarshal(entry[0], &coordinate) != nil || coordinate == "" {
			return nil, fmt.Errorf("package %q has no package coordinate", key)
		}
		name, version := coordinate, ""
		// Skip the leading @ in scoped names; later @ characters may belong
		// to a private git URL and must remain part of the specifier.
		if sep := strings.Index(coordinate[1:], "@"); sep >= 0 {
			name, version = coordinate[:sep+1], coordinate[sep+2:]
		}
		importName, visible := key, true
		if nested != "" {
			if rest, own := strings.CutPrefix(key, nested); own {
				importName = rest
			} else if _, shadowed := lock.Packages[nested+key]; shadowed {
				visible = false // the member resolves its own copy instead
			}
		}
		spec, declared := directSpecs[importName]
		topLevel := !strings.Contains(strings.TrimPrefix(importName, "@"), "/") ||
			(strings.HasPrefix(importName, "@") && strings.Count(importName, "/") == 1)
		direct := declared && topLevel && visible
		dep := lockDep{Name: name, Version: version, Direct: direct,
			Private: isPrivateSpec(version) || (direct && isPrivateSpec(spec))}
		identity := name + "\x00" + version
		if i, ok := seen[identity]; ok {
			deps[i].Direct = deps[i].Direct || dep.Direct
			deps[i].Private = deps[i].Private || dep.Private
		} else {
			seen[identity] = len(deps)
			deps = append(deps, dep)
		}
	}
	return deps, nil
}

// cleanBunJSON removes comments and trailing commas only outside JSON strings.
// Mask comments instead of joining tokens so malformed JSON stays malformed.
func cleanBunJSON(data []byte) ([]byte, error) {
	out := append([]byte(nil), data...)
	inString, escaped := false, false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c != '/' || i+1 >= len(out) {
			continue
		}
		switch out[i+1] {
		case '/':
			for i < len(out) && out[i] != '\n' && out[i] != '\r' {
				out[i] = ' '
				i++
			}
			i--
		case '*':
			out[i], out[i+1] = ' ', ' '
			i += 2
			for ; i+1 < len(out) && !(out[i] == '*' && out[i+1] == '/'); i++ {
				if out[i] != '\n' && out[i] != '\r' {
					out[i] = ' '
				}
			}
			if i+1 >= len(out) {
				return nil, fmt.Errorf("unterminated block comment")
			}
			out[i], out[i+1] = ' ', ' '
			i++
		}
	}
	inString, escaped = false, false
	for i, c := range out {
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\r' || out[j] == '\n') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				k := i - 1
				for k >= 0 && (out[k] == ' ' || out[k] == '\t' || out[k] == '\r' || out[k] == '\n') {
					k--
				}
				if k >= 0 && out[k] != '{' && out[k] != '[' && out[k] != ',' && out[k] != ':' {
					out[i] = ' '
				}
			}
		}
	}
	return out, nil
}

func toResolved(deps []lockDep, source string) []scanner.ResolvedPackage {
	out := make([]scanner.ResolvedPackage, 0, len(deps))
	for _, d := range deps {
		pub := scanner.PublicnessUnknown // never PUBLIC here; registry check upgrades
		if d.Private {
			pub = scanner.PublicnessPrivate
		}
		// A version the lockfile did not give us is not 0.0.0.
		// Substituting one fabricated a release that has never existed and
		// then uploaded evidence under it: every dependency of a Yarn Berry
		// project (yarn.lock beginning with __metadata:, which this parser
		// does not read) was recorded as name@0.0.0, so another machine's
		// search could be answered with evidence attributed to a version
		// nobody can install.
		//
		// The entry is kept — a linked local package has no version and is
		// still worth having in the local inventory — with the version left
		// EMPTY rather than invented, and marked private so it can never be
		// uploaded. Nothing may leave this machine describing a release
		// that does not exist.
		version := d.Version
		if version == "" {
			pub = scanner.PublicnessPrivate
		}
		out = append(out, scanner.ResolvedPackage{
			PURL:       domain.PURL{Ecosystem: "npm", Name: d.Name, Version: version},
			Publicness: pub,
			Direct:     d.Direct,
			Source:     source,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PURL.Name != out[j].PURL.Name {
			return out[i].PURL.Name < out[j].PURL.Name
		}
		return out[i].PURL.Version < out[j].PURL.Version
	})
	return out
}

// isPrivateSpec reports whether a specifier/version/resolution marks a
// non-registry dependency. Such packages are PRIVATE and never leave the
// machine (goal.md §25.E).
func isPrivateSpec(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, p := range []string{
		"file:", "link:", "portal:", "workspace:",
		"git+ssh:", "git+https:", "git+http:", "git+file:", "git:", "github:", "ssh:",
	} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") {
		return true
	}
	return windowsPathRe.MatchString(s)
}

var windowsPathRe = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// addDirectSpec records spec as the manifest specifier of a direct
// dependency. If a name appears in more than one manifest section, a private
// path specifier is retained so the installed package cannot be mistaken for
// public based on whichever section was visited last.
func addDirectSpec(directSpecs map[string]string, name, spec string) {
	if previous, ok := directSpecs[name]; !ok || (!isPrivateSpec(previous) && isPrivateSpec(spec)) {
		directSpecs[name] = spec
	}
}

// --- package-lock.json v2/v3 ---

type npmLock struct {
	LockfileVersion int                     `json:"lockfileVersion"`
	Packages        map[string]npmLockEntry `json:"packages"`
}

type npmLockEntry struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Resolved             string            `json:"resolved"`
	Link                 bool              `json:"link"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

// A workspace member's own entry is keyed by its path ("apps/web"); it
// resolves "apps/web/node_modules/<name>" first and the hoisted
// "node_modules/<name>" otherwise, so either is its top-level installation.
func parsePackageLock(data []byte, _, member string) ([]lockDep, error) {
	var lock npmLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	if lock.Packages == nil {
		return nil, fmt.Errorf("unsupported lockfileVersion %d (no packages map)", lock.LockfileVersion)
	}
	root, ok := lock.Packages[member]
	if !ok && member != "" {
		return nil, nil // a stale lockfile that predates this member proves nothing about it
	}
	memberModules := ""
	if member != "" {
		memberModules = member + "/node_modules/"
	}
	directSpecs := map[string]string{}
	for _, specs := range []map[string]string{
		root.Dependencies,
		root.DevDependencies,
		root.OptionalDependencies,
		root.PeerDependencies,
	} {
		for name, spec := range specs {
			addDirectSpec(directSpecs, name, spec)
		}
	}

	urlInstalled := urlInstalledKeys(lock.Packages)
	var deps []lockDep
	for key, e := range lock.Packages {
		if key == "" || !strings.Contains(key, "node_modules/") {
			continue // root or a filesystem-path entry (link target)
		}
		idx := strings.LastIndex(key, "node_modules/")
		name := key[idx+len("node_modules/"):]
		if name == "" {
			continue
		}
		topLevel := key == "node_modules/"+name
		if memberModules != "" {
			_, shadowed := lock.Packages[memberModules+name]
			topLevel = key == memberModules+name || (topLevel && !shadowed)
		}
		spec, isDirect := directSpecs[name]

		// An ALIAS installs one package under another name:
		//
		//	"dependencies": {"lodash": "npm:lodash-es@4.17.21"}
		//	"node_modules/lodash": {"name":"lodash-es","version":"4.17.21"}
		//
		// The key is the import name; the entry's own "name" is the package
		// that is actually installed. Reading the key alone reported
		// lodash@4.17.21 -- a package this project never installs, and a
		// real one on npm, so the publicness check confirmed it and
		// observations were uploaded about a build that never used it,
		// while the package that WAS built went unreported. The alias
		// string-width-cjs -> npm:string-width appears transitively in a
		// large share of real lockfiles through @isaacs/cliui.
		//
		// Symbols still resolve by import name, so an aliased import loses
		// its symbol row rather than attributing lodash.debounce to the
		// wrong package. Losing a row is the acceptable half of that trade.
		if e.Name != "" && e.Name != name {
			name = e.Name
		}

		version := e.Version
		if version == "" && e.Link {
			version = lock.Packages[e.Resolved].Version
		}
		deps = append(deps, lockDep{
			Name:    name,
			Version: version,
			Direct:  topLevel && isDirect,
			Private: e.Link || isPrivateSpec(e.Resolved) || isPrivateSpec(e.Version) ||
				(topLevel && (isPrivateSpec(spec) || isURLSpec(spec))) || urlInstalled[key],
		})
	}
	return deps, nil
}

// --- pnpm-lock.yaml v9 ---

// parsePnpmLock is a purpose-built line parser for the two sections it needs
// (importers "." and packages); a full YAML parser is deliberately avoided to
// keep the dependency set pinned. A workspace member's importer is keyed by
// its path ("apps/web") instead of ".".
func parsePnpmLock(data []byte, _, member string) ([]lockDep, error) {
	importer := "."
	if member != "" {
		importer = member
	}
	importerSeen := false
	// impDep accumulates one importer dependency across every section that
	// declares it (a peer that is also a devDependency, say), so the last
	// section read cannot drop a version match or a private specifier.
	type impDep struct {
		versions []string // peer suffixes stripped
		private  bool     // any specifier or version is a private path
	}
	type pkgEntry struct{ name, keyVersion, fieldVersion string }

	importerDeps := map[string]*impDep{}
	var pkgs []*pkgEntry
	var curPkg *pkgEntry
	var curImpDep *impDep

	section := ""
	importerActive := false
	depSection := ""

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))

		if indent == 0 {
			section = strings.TrimSuffix(trimmed, ":")
			importerActive, depSection, curPkg, curImpDep = false, "", nil, nil
			continue
		}

		switch section {
		case "importers":
			switch {
			case indent == 2 && strings.HasSuffix(trimmed, ":"):
				importerActive = unquoteYAML(strings.TrimSuffix(trimmed, ":")) == importer
				importerSeen = importerSeen || importerActive
				depSection, curImpDep = "", nil
			case importerActive && indent == 4 && strings.HasSuffix(trimmed, ":"):
				depSection = strings.TrimSuffix(trimmed, ":")
				curImpDep = nil
			case importerActive && indent == 6 && strings.HasSuffix(trimmed, ":") &&
				(depSection == "dependencies" || depSection == "devDependencies" ||
					depSection == "optionalDependencies" || depSection == "peerDependencies"):
				name := unquoteYAML(strings.TrimSuffix(trimmed, ":"))
				curImpDep = importerDeps[name]
				if curImpDep == nil {
					curImpDep = &impDep{}
					importerDeps[name] = curImpDep
				}
			case curImpDep != nil && indent == 8:
				if k, v, ok := strings.Cut(trimmed, ":"); ok {
					v = unquoteYAML(strings.TrimSpace(v))
					switch strings.TrimSpace(k) {
					case "specifier":
						curImpDep.private = curImpDep.private || isPrivateSpec(v)
					case "version":
						curImpDep.versions = append(curImpDep.versions, stripPeerSuffix(v))
						curImpDep.private = curImpDep.private || isPrivateSpec(v)
					}
				}
			}
		case "packages":
			switch {
			case indent == 2 && strings.HasSuffix(trimmed, ":"):
				key := unquoteYAML(strings.TrimSuffix(trimmed, ":"))
				name, ver, ok := splitNameVersion(key)
				curPkg = nil
				if ok {
					curPkg = &pkgEntry{name: name, keyVersion: ver}
					pkgs = append(pkgs, curPkg)
				}
			case curPkg != nil && indent >= 4:
				if k, v, ok := strings.Cut(trimmed, ":"); ok && strings.TrimSpace(k) == "version" {
					curPkg.fieldVersion = unquoteYAML(strings.TrimSpace(v))
				}
			}
		}
	}

	if member != "" && !importerSeen {
		return nil, nil // a stale lockfile that predates this member proves nothing about it
	}

	seenNames := map[string]bool{}
	var deps []lockDep
	for _, p := range pkgs {
		version := stripPeerSuffix(p.keyVersion)
		private := isPrivateSpec(version)
		imp, ok := importerDeps[p.name]
		direct := ok && (slices.Contains(imp.versions, version) || private)
		if direct && imp.private {
			private = true
		}
		if private {
			version = p.fieldVersion
		}
		deps = append(deps, lockDep{Name: p.name, Version: version, Direct: direct, Private: private})
		seenNames[p.name] = true
	}
	// Importer deps absent from the packages section (e.g. links excluded
	// from the lockfile) still count, so the private set stays complete.
	for name, imp := range importerDeps {
		if seenNames[name] {
			continue
		}
		// The first registry version from any section, so the result does
		// not depend on which section listed a link: path first.
		version := ""
		for _, v := range imp.versions {
			if !isPrivateSpec(v) {
				version = v
				break
			}
		}
		deps = append(deps, lockDep{Name: name, Version: version, Direct: true, Private: imp.private})
	}
	return deps, nil
}

func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// splitNameVersion splits "<name>@<version>" at the last '@', so scoped
// names ("@types/node@22.5.4") keep their scope.
func splitNameVersion(key string) (name, version string, ok bool) {
	idx := strings.LastIndex(key, "@")
	if idx <= 0 || idx == len(key)-1 {
		return "", "", false
	}
	return key[:idx], key[idx+1:], true
}

// stripPeerSuffix drops pnpm peer-dependency suffixes: "1.12.0(x@1)" → "1.12.0".
func stripPeerSuffix(v string) string {
	if i := strings.IndexByte(v, '('); i >= 0 {
		return v[:i]
	}
	return v
}

// --- yarn.lock classic ---

// The classic lockfile records no locations, so direct dependencies are the
// ones dir's own package.json declares — a workspace member's when dir is one.
// A root lockfile also holds every other member's ranges, so for a member an
// entry is direct only when it carries the declared range, if any entry does.
func parseYarnLock(data []byte, dir, member string) ([]lockDep, error) {
	directSpecs := map[string]string{}
	if pj, ok := readPackageJSON(dir); ok {
		for _, m := range []map[string]string{pj.Dependencies, pj.DevDependencies, pj.OptionalDependencies, pj.PeerDependencies} {
			for name, spec := range m {
				addDirectSpec(directSpecs, name, spec)
			}
		}
	}

	type entry struct {
		name              string
		ranges            []string
		version, resolved string
	}
	var entries []*entry
	var cur *entry

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			if !strings.HasSuffix(line, ":") {
				cur = nil
				continue
			}
			cur = &entry{}
			for _, spec := range strings.Split(strings.TrimSuffix(line, ":"), ",") {
				spec = strings.Trim(strings.TrimSpace(spec), `"`)
				name, rng, ok := splitNameVersion(spec)
				if !ok {
					continue
				}
				cur.name = name
				cur.ranges = append(cur.ranges, rng)
			}
			if cur.name == "" {
				cur = nil
				continue
			}
			entries = append(entries, cur)
			continue
		}
		if cur == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(trimmed, "version "); ok {
			cur.version = strings.Trim(v, `"`)
		} else if v, ok := strings.CutPrefix(trimmed, "resolved "); ok {
			cur.resolved = strings.Trim(v, `"`)
		}
	}

	rangeMatched := map[string]bool{}
	if member != "" {
		for _, e := range entries {
			if spec, ok := directSpecs[e.name]; ok && slices.Contains(e.ranges, spec) {
				rangeMatched[e.name] = true
			}
		}
	}

	var deps []lockDep
	for _, e := range entries {
		private := isPrivateSpec(e.resolved)
		for _, r := range e.ranges {
			if isPrivateSpec(r) || isURLSpec(r) {
				private = true
			}
		}
		spec, direct := directSpecs[e.name]
		if direct && rangeMatched[e.name] && !slices.Contains(e.ranges, spec) {
			direct = false // another member's copy of the same package
		}
		if direct && (isPrivateSpec(spec) || isURLSpec(spec)) {
			private = true
		}
		deps = append(deps, lockDep{Name: e.name, Version: e.version, Direct: direct, Private: private})
	}
	return deps, nil
}
