package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// --- package-lock.json v2/v3 ---

type npmLock struct {
	LockfileVersion int                     `json:"lockfileVersion"`
	Packages        map[string]npmLockEntry `json:"packages"`
}

type npmLockEntry struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Resolved        string            `json:"resolved"`
	Link            bool              `json:"link"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
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
	for name, spec := range root.Dependencies {
		directSpecs[name] = spec
	}
	for name, spec := range root.DevDependencies {
		directSpecs[name] = spec
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
			Private: e.Link || isPrivateSpec(e.Resolved) || isPrivateSpec(e.Version) || (topLevel && isPrivateSpec(spec)),
		})
	}
	return deps, nil
}

// --- pnpm-lock.yaml v9 ---

// parsePnpmLock is a purpose-built line parser for the two sections it needs
// (importers "." and packages); a full YAML parser is deliberately avoided to
// keep the dependency set pinned.
func parsePnpmLock(data []byte, _ string) ([]lockDep, error) {
	type impDep struct{ specifier, version string }
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
				importerActive = unquoteYAML(strings.TrimSuffix(trimmed, ":")) == "."
				depSection, curImpDep = "", nil
			case importerActive && indent == 4 && strings.HasSuffix(trimmed, ":"):
				depSection = strings.TrimSuffix(trimmed, ":")
				curImpDep = nil
			case importerActive && indent == 6 && strings.HasSuffix(trimmed, ":") &&
				(depSection == "dependencies" || depSection == "devDependencies" || depSection == "optionalDependencies"):
				name := unquoteYAML(strings.TrimSuffix(trimmed, ":"))
				curImpDep = &impDep{}
				importerDeps[name] = curImpDep
			case curImpDep != nil && indent == 8:
				if k, v, ok := strings.Cut(trimmed, ":"); ok {
					v = unquoteYAML(strings.TrimSpace(v))
					switch strings.TrimSpace(k) {
					case "specifier":
						curImpDep.specifier = v
					case "version":
						curImpDep.version = v
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

	seenNames := map[string]bool{}
	var deps []lockDep
	for _, p := range pkgs {
		version := stripPeerSuffix(p.keyVersion)
		private := isPrivateSpec(version)
		imp, ok := importerDeps[p.name]
		direct := ok && (stripPeerSuffix(imp.version) == version || private)
		if direct && (isPrivateSpec(imp.specifier) || isPrivateSpec(imp.version)) {
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
		version := stripPeerSuffix(imp.version)
		private := isPrivateSpec(imp.specifier) || isPrivateSpec(imp.version)
		if private && isPrivateSpec(version) {
			version = ""
		}
		deps = append(deps, lockDep{Name: name, Version: version, Direct: true, Private: private})
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

func parseYarnLock(data []byte, dir string) ([]lockDep, error) {
	directSpecs := map[string]string{}
	if pj, ok := readPackageJSON(dir); ok {
		for _, m := range []map[string]string{pj.Dependencies, pj.DevDependencies, pj.OptionalDependencies} {
			for name, spec := range m {
				directSpecs[name] = spec
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

	var deps []lockDep
	for _, e := range entries {
		private := isPrivateSpec(e.resolved)
		for _, r := range e.ranges {
			if isPrivateSpec(r) {
				private = true
			}
		}
		spec, direct := directSpecs[e.name]
		if direct && isPrivateSpec(spec) {
			private = true
		}
		deps = append(deps, lockDep{Name: e.name, Version: e.version, Direct: direct, Private: private})
	}
	return deps, nil
}
