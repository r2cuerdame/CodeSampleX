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

// ScanPackages reads the first lockfile found (package-lock.json,
// pnpm-lock.yaml, yarn.lock) and returns resolved versions. No lockfile ⇒
// empty result: manifest ranges are never reported as resolved (goal.md §7.1).
func (Adapter) ScanPackages(ctx context.Context, dir string) ([]scanner.ResolvedPackage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type lockParser struct {
		file  string
		parse func(data []byte, dir string) ([]lockDep, error)
	}
	for _, lp := range []lockParser{
		{"package-lock.json", parsePackageLock},
		{"pnpm-lock.yaml", parsePnpmLock},
		{"yarn.lock", parseYarnLock},
	} {
		data, err := os.ReadFile(filepath.Join(dir, lp.file))
		if err != nil {
			continue
		}
		deps, err := lp.parse(data, dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", lp.file, err)
		}
		return toResolved(deps, lp.file), nil
	}
	return nil, nil
}

func toResolved(deps []lockDep, source string) []scanner.ResolvedPackage {
	deps = mergeLockDeps(deps)
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
	if previous, ok := directSpecs[name]; !ok || (!isPrivateManifestSpec(previous) && isPrivateManifestSpec(spec)) {
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

func parsePackageLock(data []byte, _ string) ([]lockDep, error) {
	var lock npmLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	if lock.Packages == nil {
		return nil, fmt.Errorf("unsupported lockfileVersion %d (no packages map)", lock.LockfileVersion)
	}
	root := lock.Packages[""]
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
			Private: e.Link || isPrivateSpec(e.Resolved) || isPrivateSpec(e.Version) || (topLevel && isPrivateManifestSpec(spec)),
		})
	}
	return mergeLockDeps(deps), nil
}

func mergeLockDeps(deps []lockDep) []lockDep {
	type key struct{ name, version string }
	idxByKey := make(map[key]int, len(deps))
	out := make([]lockDep, 0, len(deps))
	for _, d := range deps {
		k := key{name: d.Name, version: d.Version}
		if idx, ok := idxByKey[k]; ok {
			out[idx].Direct = out[idx].Direct || d.Direct
			out[idx].Private = out[idx].Private || d.Private
			continue
		}
		idxByKey[k] = len(out)
		out = append(out, d)
	}
	return out
}

var semverRangeTokenRe = regexp.MustCompile(`^[0-9]+(?:\.[0-9xX]+)+(?:[-+].*)?$`)

func isValidNpmSegment(seg string) bool {
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	c0 := seg[0]
	if !((c0 >= 'a' && c0 <= 'z') || (c0 >= 'A' && c0 <= 'Z') || (c0 >= '0' && c0 <= '9') || c0 == '_') {
		return false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || (i > 0 && c == '~') {
			continue
		}
		return false
	}
	return true
}

func isValidNpmName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name {
		return false
	}
	if strings.HasPrefix(name, "@") {
		scope, pkg, ok := strings.Cut(name[1:], "/")
		return ok && !strings.Contains(pkg, "/") && isValidNpmSegment(scope) && isValidNpmSegment(pkg)
	}
	if strings.Contains(name, "/") || semverRangeTokenRe.MatchString(name) {
		return false
	}
	return isValidNpmSegment(name)
}

// splitNpmAliasRest splits the "<target>[@<range>]" payload following "npm:"
// or "@npm:", supporting scoped targets ("@scope/pkg", "@npm/pkg"), omitted
// version ranges ("lodash-es", "@scope/pkg"), and ranges that themselves
// contain '@' (such as git+ssh URLs).
func splitNpmAliasRest(rest string) (target, rng string, ok bool) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", false
	}
	if len(rest) > 1 {
		if rel := strings.IndexByte(rest[1:], '@'); rel >= 0 {
			idx := rel + 1
			target, rng = rest[:idx], rest[idx+1:]
		} else {
			target = rest
		}
	} else {
		target = rest
	}
	if !isValidNpmName(target) {
		return "", "", false
	}
	return target, rng, true
}

func parseNpmAliasSpec(spec string) (target, rng string, ok bool) {
	rest, hasPrefix := strings.CutPrefix(strings.TrimSpace(spec), "npm:")
	if !hasPrefix {
		return "", "", false
	}
	return splitNpmAliasRest(rest)
}

func isPrivateManifestSpec(spec string) bool {
	if isPrivateSpec(spec) {
		return true
	}
	if _, rng, ok := parseNpmAliasSpec(spec); ok {
		return isPrivateSpec(rng)
	}
	return false
}

// parsePnpmAliasTarget parses an importer version value (with peer suffix
// already stripped) when it names an aliased target "<pkg>@<version>"
// (with optional "npm:" prefix, including scoped "@scope/pkg@<version>").
// Private path/git/workspace specs and plain versions return ok=false.
func parsePnpmAliasTarget(v string) (name, version string, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" || isPrivateSpec(v) {
		return "", "", false
	}
	s := strings.TrimPrefix(v, "npm:")
	name, version, ok = splitNameVersion(s)
	if !ok || !isValidNpmName(name) {
		return "", "", false
	}
	return name, version, true
}

// --- pnpm-lock.yaml v9 ---

// parsePnpmLock is a purpose-built line parser for the two sections it needs
// (importers "." and packages); a full YAML parser is deliberately avoided to
// keep the dependency set pinned.
func parsePnpmLock(data []byte, _ string) ([]lockDep, error) {
	// rawImpDecl records each importer declaration in section order while
	// rawKeyPrivate retains privateness across sections sharing a manifest key.
	type rawImpDecl struct {
		name       string
		specifier  string
		version    string // peer suffix stripped when not a private spec
		hasVersion bool
		private    bool
	}
	type resolvedImpDep struct {
		versions        []string        // resolved versions (or private specs) for this package
		privateVersions map[string]bool // versions whose importer declaration was private
		nonAliasPrivate bool            // any non-aliased declaration for this package was private
		hasNonAlias     bool            // declared directly under its own package name
	}
	type pkgEntry struct{ name, keyVersion, fieldVersion string }

	var rawDecls []*rawImpDecl
	rawKeyPrivate := map[string]bool{}
	var pkgs []*pkgEntry
	var curPkg *pkgEntry
	var curImpDecl *rawImpDecl

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
			importerActive, depSection, curPkg, curImpDecl = false, "", nil, nil
			continue
		}

		switch section {
		case "importers":
			switch {
			case indent == 2:
				importerActive = strings.HasSuffix(trimmed, ":") && unquoteYAML(strings.TrimSuffix(trimmed, ":")) == "."
				depSection, curImpDecl = "", nil
			case importerActive && indent == 4:
				if strings.HasSuffix(trimmed, ":") {
					depSection = strings.TrimSuffix(trimmed, ":")
				} else {
					depSection = ""
				}
				curImpDecl = nil
			case importerActive && indent == 6:
				curImpDecl = nil
				if strings.HasSuffix(trimmed, ":") &&
					(depSection == "dependencies" || depSection == "devDependencies" ||
						depSection == "optionalDependencies" || depSection == "peerDependencies") {
					name := unquoteYAML(strings.TrimSuffix(trimmed, ":"))
					curImpDecl = &rawImpDecl{name: name}
					rawDecls = append(rawDecls, curImpDecl)
				}
			case curImpDecl != nil && indent == 8:
				if k, v, ok := strings.Cut(trimmed, ":"); ok {
					v = unquoteYAML(strings.TrimSpace(v))
					switch strings.TrimSpace(k) {
					case "specifier":
						curImpDecl.specifier = v
						if isPrivateManifestSpec(v) {
							curImpDecl.private = true
							rawKeyPrivate[curImpDecl.name] = true
						}
					case "version":
						priv := isPrivateSpec(v)
						if !priv {
							v = stripPeerSuffix(v)
						}
						curImpDecl.version = v
						curImpDecl.hasVersion = true
						if priv || isPrivateSpec(v) {
							curImpDecl.private = true
							rawKeyPrivate[curImpDecl.name] = true
						}
					}
				}
			}
		case "packages":
			switch {
			case indent == 2 && strings.HasSuffix(trimmed, ":"):
				key := unquoteYAML(strings.TrimSuffix(trimmed, ":"))
				if idx := strings.IndexByte(key, '('); idx > 0 &&
					!strings.Contains(key[:idx], "file:") && !strings.Contains(key[:idx], "link:") {
					key = key[:idx]
				}
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

	importerDeps := map[string]*resolvedImpDep{}
	var importerOrder []string
	ensureImporter := func(name string) *resolvedImpDep {
		imp := importerDeps[name]
		if imp == nil {
			imp = &resolvedImpDep{privateVersions: map[string]bool{}}
			importerDeps[name] = imp
			importerOrder = append(importerOrder, name)
		}
		return imp
	}

	for _, decl := range rawDecls {
		priv := decl.private || rawKeyPrivate[decl.name]
		if decl.hasVersion {
			if targetName, targetVer, ok := parsePnpmAliasTarget(decl.version); ok {
				imp := ensureImporter(targetName)
				imp.versions = append(imp.versions, targetVer)
				if priv || isPrivateSpec(targetVer) {
					imp.privateVersions[targetVer] = true
				}
				continue
			}
			imp := ensureImporter(decl.name)
			imp.hasNonAlias = true
			imp.nonAliasPrivate = imp.nonAliasPrivate || priv
			imp.versions = append(imp.versions, decl.version)
			if priv || isPrivateSpec(decl.version) {
				imp.privateVersions[decl.version] = true
			}
			continue
		}
		if targetName, _, ok := parseNpmAliasSpec(decl.specifier); ok {
			imp := ensureImporter(targetName)
			if priv {
				imp.nonAliasPrivate = true
			}
			continue
		}
		imp := ensureImporter(decl.name)
		imp.hasNonAlias = true
		imp.nonAliasPrivate = imp.nonAliasPrivate || priv
	}

	seenNames := map[string]bool{}
	var deps []lockDep
	for _, p := range pkgs {
		version := p.keyVersion
		keyPrivate := isPrivateSpec(version)
		if !keyPrivate {
			version = stripPeerSuffix(version)
		}
		imp, ok := importerDeps[p.name]
		direct := ok && (slices.Contains(imp.versions, version) || (keyPrivate && imp.hasNonAlias))
		private := keyPrivate || (direct && (imp.nonAliasPrivate || imp.privateVersions[version]))
		if keyPrivate {
			version = p.fieldVersion
		} else if private && p.fieldVersion != "" {
			version = p.fieldVersion
		}
		deps = append(deps, lockDep{Name: p.name, Version: version, Direct: direct, Private: private})
		seenNames[p.name] = true
	}
	// Importer deps absent from the packages section (e.g. links excluded
	// from the lockfile) still count, so the private set stays complete.
	for _, name := range importerOrder {
		if seenNames[name] {
			continue
		}
		imp := importerDeps[name]
		// The first registry version from any section, so the result does
		// not depend on which section listed a link: path first.
		version := ""
		for _, v := range imp.versions {
			if !isPrivateSpec(v) {
				version = v
				break
			}
		}
		private := imp.nonAliasPrivate || imp.privateVersions[version]
		if version == "" {
			for _, v := range imp.versions {
				if isPrivateSpec(v) || imp.privateVersions[v] {
					private = true
					break
				}
			}
		}
		deps = append(deps, lockDep{Name: name, Version: version, Direct: true, Private: private})
	}
	return mergeLockDeps(deps), nil
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

// splitNameVersion splits "<name>@<version>" at the first '@' after index 0,
// so scoped names ("@types/node@22.5.4") keep their scope and versions or
// git/path specifiers containing '@' stay intact.
func splitNameVersion(key string) (name, version string, ok bool) {
	if len(key) <= 1 {
		return "", "", false
	}
	rel := strings.IndexByte(key[1:], '@')
	if rel < 0 {
		return "", "", false
	}
	idx := rel + 1
	if idx == len(key)-1 {
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

type yarnHeaderSpec struct {
	alias string
	name  string
	rng   string
}

func parseYarnHeaderSpec(spec string) (yarnHeaderSpec, bool) {
	if spec == "" {
		return yarnHeaderSpec{}, false
	}
	// Recognize "<alias>@npm:<target>[@<range>]" where @npm: appears after a
	// non-empty alias. Index 0 "@npm/..." is a scoped package, not an alias.
	if len(spec) > 1 {
		if rel := strings.Index(spec[1:], "@npm:"); rel >= 0 {
			pos := rel + 1
			alias := spec[:pos]
			rest := spec[pos+len("@npm:"):]
			if !isValidNpmName(alias) || rest == "" {
				return yarnHeaderSpec{}, false
			}
			if target, rng, ok := splitNpmAliasRest(rest); ok {
				return yarnHeaderSpec{alias: alias, name: target, rng: rng}, true
			}
			// Yarn Berry writes ordinary ranges as "<pkg>@npm:<range>".
			return yarnHeaderSpec{name: alias, rng: rest}, true
		}
	}
	if strings.HasPrefix(spec, "@npm:") {
		return yarnHeaderSpec{}, false
	}
	name, rng, ok := splitNameVersion(spec)
	if !ok {
		return yarnHeaderSpec{}, false
	}
	return yarnHeaderSpec{name: name, rng: rng}, true
}

func parseYarnLock(data []byte, dir string) ([]lockDep, error) {
	type manifestDecl struct {
		key  string
		spec string
	}
	var decls []manifestDecl
	privateByKey := map[string]bool{}
	if pj, ok := readPackageJSON(dir); ok {
		for _, m := range []map[string]string{pj.Dependencies, pj.DevDependencies, pj.OptionalDependencies, pj.PeerDependencies} {
			for name, spec := range m {
				spec = strings.TrimSpace(spec)
				decls = append(decls, manifestDecl{key: name, spec: spec})
				if isPrivateManifestSpec(spec) {
					privateByKey[name] = true
				}
			}
		}
	}

	type entry struct {
		name              string
		headers           []yarnHeaderSpec
		version, resolved string
		direct, private   bool
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
				h, ok := parseYarnHeaderSpec(spec)
				if !ok {
					continue
				}
				if cur.name == "" {
					cur.name = h.name
				}
				cur.headers = append(cur.headers, h)
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

	for _, e := range entries {
		if isPrivateSpec(e.resolved) {
			e.private = true
		}
		for _, h := range e.headers {
			if isPrivateSpec(h.rng) {
				e.private = true
			}
		}
	}

	anyEntry := func(pred func(h yarnHeaderSpec) bool) bool {
		for _, e := range entries {
			if slices.ContainsFunc(e.headers, pred) {
				return true
			}
		}
		return false
	}
	applyPred := func(pred func(h yarnHeaderSpec) bool, markPrivate bool) {
		for _, e := range entries {
			if slices.ContainsFunc(e.headers, pred) {
				e.direct = true
				if markPrivate {
					e.private = true
				}
			}
		}
	}

	for _, d := range decls {
		if target, declRng, ok := parseNpmAliasSpec(d.spec); ok {
			priv := privateByKey[d.key] || isPrivateSpec(declRng)
			exact := func(h yarnHeaderSpec) bool {
				return h.alias == d.key && h.name == target && h.rng == declRng
			}
			loose := func(h yarnHeaderSpec) bool {
				return h.alias == d.key && h.name == target
			}
			if anyEntry(exact) {
				applyPred(exact, priv)
			} else {
				applyPred(loose, priv)
			}
			continue
		}
		if isPrivateSpec(d.spec) {
			exact := func(h yarnHeaderSpec) bool {
				return ((h.alias == "" && h.name == d.key) || h.alias == d.key) && h.rng == d.spec
			}
			nonAlias := func(h yarnHeaderSpec) bool {
				return h.alias == "" && h.name == d.key
			}
			aliasKey := func(h yarnHeaderSpec) bool {
				return h.alias == d.key
			}
			switch {
			case anyEntry(exact):
				applyPred(exact, true)
			case anyEntry(nonAlias):
				applyPred(nonAlias, true)
			default:
				applyPred(aliasKey, true)
			}
			continue
		}
		exact := func(h yarnHeaderSpec) bool {
			return h.alias == "" && h.name == d.key && h.rng == d.spec
		}
		loose := func(h yarnHeaderSpec) bool {
			return h.alias == "" && h.name == d.key
		}
		if anyEntry(exact) {
			applyPred(exact, privateByKey[d.key])
		} else {
			applyPred(loose, privateByKey[d.key])
		}
	}

	deps := make([]lockDep, 0, len(entries))
	for _, e := range entries {
		deps = append(deps, lockDep{Name: e.name, Version: e.version, Direct: e.direct, Private: e.private})
	}
	return mergeLockDeps(deps), nil
}
