package verifier

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// This file answers one question a receipt could not answer before: which
// version actually ran.
//
// A receipt said "this sample passed in this environment" and took the
// version from the manifest — a claim the verification never checked. The
// manifest is written by the sample's author, and §3.8 is that a wrong HIT
// is worse than a MISS, so a version nobody confirmed is exactly the shape
// of claim this project exists to refuse. It was also why there was
// nowhere to put version-axis data: the same contract run against three
// releases produced three receipts that all said the same thing.
//
// After resolve, the workspace holds the answer. Every ecosystem writes
// what it actually installed somewhere inside /work — a lockfile it
// generated, or the installed distribution metadata — and /work is the
// sample directory on this machine. Reading it costs nothing and turns a
// claim into evidence.
//
// Silence is honest here. A lockfile that cannot be read yields nothing
// rather than a guess, and an empty list means "not established", never
// "matches the manifest".

// resolvedPackages reports the purls the resolve stage actually produced,
// for the packages this sample declares.
func resolvedPackages(dir string, m domain.SampleManifest) []string {
	resolverEcosystem := strings.ToLower(strings.TrimSpace(m.Environment.Ecosystem))
	if resolverEcosystem == "" {
		return nil
	}
	want := map[string]domain.PURL{}
	for _, raw := range m.Packages {
		if p, err := domain.ParsePURL(raw); err == nil {
			// One verification runs one resolver. A lockfile for another
			// ecosystem may merely be shipped beside the sample; reading it
			// would turn an npm PASS into a false Cargo (or PyPI, Gem, ...)
			// claim. Only the resolver that actually ran can contribute
			// resolved-package evidence.
			if p.Ecosystem != resolverEcosystem {
				continue
			}
			want[p.Ecosystem+"/"+p.Name] = p
		}
	}
	if len(want) == 0 {
		return nil
	}

	found := map[string]string{} // eco/name → version
	for key, p := range want {
		if v := resolvedVersion(dir, m, p); domain.ConcreteResolvedVersion(v) {
			found[key] = v
		}
	}
	out := make([]string, 0, len(found))
	for key, v := range found {
		p := want[key]
		p.Version = v
		out = append(out, p.String())
	}
	sort.Strings(out)
	return out
}

// resolvedVersion reads one package's installed version out of whatever
// the ecosystem's resolve step left behind.
func resolvedVersion(dir string, m domain.SampleManifest, p domain.PURL) string {
	switch p.Ecosystem {
	case "npm":
		switch sandbox.NPMResolver(m.Environment.Runtime) {
		case "bun":
			return bunResolved(dir, p.Name)
		case "deno":
			return denoResolved(dir, p.Name)
		}
		return npmResolved(dir, p.Name)
	case "cargo":
		return cargoLockResolved(dir, p.Name)
	case "gem":
		return gemfileLockResolved(dir, p.Name)
	case "composer":
		// composer.lock is author-controlled, and Packagist's notification
		// URL can coexist with an arbitrary dist/source URL. Composer may
		// also fall back from dist to source. Until resolve persists an
		// independently checked Packagist record, silence is the only honest
		// public-source claim.
		return ""
	case "hex":
		return mixLockResolved(dir, p.Name)
	case "pub":
		return pubspecLockResolved(dir, p.Name)
	case "golang":
		return goListResolved(dir, p.Name)
	case "pypi":
		return distInfoResolved(dir, p.Name)
	case "maven":
		return mavenResolved(dir, p)
	}
	return ""
}

// mavenResolved proves one manifest-locked JAR was fetched by the generated
// Central-only resolver. The manifest supplies the coordinate, but it becomes
// receipt evidence only when the fresh local repository contains the exact JAR
// and Maven's remote marker attributes it to the forced Central mirror.
func mavenResolved(dir string, p domain.PURL) string {
	group, artifact, ok := strings.Cut(p.Name, "/")
	if !ok || strings.Contains(artifact, "/") || !domain.ConcreteResolvedVersion(p.Version) {
		return ""
	}
	base := filepath.Join(dir, ".csx-vendor", "m2", filepath.FromSlash(strings.ReplaceAll(group, ".", "/")), artifact, p.Version)
	jarName := artifact + "-" + p.Version + ".jar"
	jar, err := os.Stat(filepath.Join(base, jarName))
	if err != nil || !jar.Mode().IsRegular() || jar.Size() == 0 {
		return ""
	}
	marker := readFile(base, "_remote.repositories")
	if !strings.Contains(marker, jarName+">csx-central=") {
		return ""
	}
	return p.Version
}

// bunResolved reads Bun's text lockfile. bun.lock is JSONC rather than
// strict JSON (trailing commas are normal), so decoding it as JSON would
// reject the file Bun itself just accepted. The usual registry entry leaves
// its source string empty; that proves an identity and version, but not which
// registry supplied them. Such entries deliberately stay silent. A public
// npm tarball URL is the minimum evidence accepted here.
func bunResolved(dir, name string) string {
	body := readFile(dir, "bun.lock")
	if body == "" {
		return ""
	}
	q := regexp.QuoteMeta(name)
	// Do not anchor this to the beginning of a line: Bun legitimately emits
	// compact lockfiles where the package key follows `"packages": {`.
	re := regexp.MustCompile(`"` + q + `"\s*:\s*\[\s*"([^"]+)"\s*,\s*"([^"]*)"`)
	matches := re.FindAllStringSubmatch(body, -1)
	if len(matches) != 1 {
		return ""
	}
	v, ok := npmSpecVersion(matches[0][1], name)
	if !ok || !npmRegistryArtifactURL(matches[0][2], name) || installedNPMVersion(dir, name) != v {
		return ""
	}
	return v
}

// denoResolved supports the v3 packages.npm layout and the v4 top-level
// npm layout. Deno lockfiles can contain several copies of one transitive
// dependency; without a unique direct specifier, silence is safer than
// choosing whichever map key happened to be visited first.
func denoResolved(dir, name string) string {
	body := readFile(dir, "deno.lock")
	if body == "" {
		return ""
	}
	var lock struct {
		Specifiers map[string]string          `json:"specifiers"`
		NPM        map[string]json.RawMessage `json:"npm"`
		Packages   struct {
			Specifiers map[string]string          `json:"specifiers"`
			NPM        map[string]json.RawMessage `json:"npm"`
		} `json:"packages"`
	}
	if json.Unmarshal([]byte(body), &lock) != nil {
		return ""
	}
	versions := map[string]bool{}
	collectDenoSpecifiers(versions, lock.Specifiers, name)
	collectDenoSpecifiers(versions, lock.Packages.Specifiers, name)
	v := onlyVersion(versions)
	if v == "" {
		return ""
	}
	// Deno normally records integrity but not the registry URL. Integrity
	// proves bytes, not public provenance: a private npm registry produces it
	// too. Accept only lock revisions/entries that carry an explicit public
	// registry or tarball URL for every matching package entry.
	if !denoPackageHasPublicSource(name, v, lock.NPM, lock.Packages.NPM) {
		return ""
	}
	return v
}

func collectDenoSpecifiers(out map[string]bool, specs map[string]string, name string) {
	for requested, resolved := range specs {
		if _, ok := npmSpecifierForName(requested, name); !ok {
			continue
		}
		if v, ok := npmSpecVersion(resolved, name); ok {
			out[v] = true
			continue
		}
		// Some lockfile revisions store only the resolved version as the value.
		if v := trimPeerSuffix(resolved); concreteNPMVersion(v) {
			out[v] = true
		}
	}
}

func denoPackageHasPublicSource(name, version string, sets ...map[string]json.RawMessage) bool {
	matches := 0
	for _, packages := range sets {
		for spec, raw := range packages {
			v, ok := npmSpecVersion(spec, name)
			if !ok || v != version {
				continue
			}
			matches++
			if !denoNPMEntryIsPublic(raw, name) {
				return false
			}
		}
	}
	return matches > 0
}

func denoNPMEntryIsPublic(raw json.RawMessage, name string) bool {
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil {
		return false
	}
	for _, field := range []string{"resolved", "tarball"} {
		var source string
		if json.Unmarshal(entry[field], &source) == nil && npmRegistryArtifactURL(source, name) {
			return true
		}
	}
	var registry string
	return json.Unmarshal(entry["registry"], &registry) == nil && publicNPMRegistryBase(registry)
}

func npmSpecVersion(spec, name string) (string, bool) {
	rest, ok := npmSpecifierForName(spec, name)
	if !ok {
		return "", false
	}
	v := trimPeerSuffix(rest)
	return v, concreteNPMVersion(v)
}

func npmSpecifierForName(spec, name string) (string, bool) {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "npm:") {
		spec = strings.TrimPrefix(spec, "npm:")
	}
	prefix := name + "@"
	if !strings.HasPrefix(spec, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(spec, prefix)
	if rest == "" || nonRegistryNPMReference(rest) || strings.HasPrefix(rest, "npm:") {
		return "", false
	}
	return rest, true
}

func trimPeerSuffix(v string) string {
	if i := strings.IndexAny(v, "_("); i >= 0 {
		v = v[:i]
	}
	return v
}

func concreteNPMVersion(v string) bool {
	if v == "" || v[0] < '0' || v[0] > '9' {
		return false
	}
	return !strings.ContainsAny(v, " \t\r\n/\\:#?^~*<>=|")
}

var npmWindowsPathRe = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func nonRegistryNPMReference(spec string) bool {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		return false
	}
	for _, prefix := range []string{
		"file:", "link:", "portal:", "workspace:", "npm:",
		"git:", "git+", "github:", "ssh:", "http:", "https:",
	} {
		if strings.HasPrefix(spec, prefix) {
			return true
		}
	}
	return strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") ||
		strings.HasPrefix(spec, "~") || npmWindowsPathRe.MatchString(spec)
}

func publicNPMRegistryBase(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.User == nil &&
		strings.EqualFold(u.Hostname(), "registry.npmjs.org")
}

func npmRegistryArtifactURL(raw, name string) bool {
	if !publicNPMRegistryBase(raw) {
		return false
	}
	u, _ := url.Parse(strings.TrimSpace(raw))
	path, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return false
	}
	return strings.HasPrefix(path, "/"+name+"/-/")
}

func onlyVersion(versions map[string]bool) string {
	if len(versions) != 1 {
		return ""
	}
	for v := range versions {
		return v
	}
	return ""
}

func readFile(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}

// installedNPMVersion checks the installation tree, not only the lockfile.
// npm ci can retain lock entries for platform-skipped optional packages, so
// package-lock.json alone proves selection but not that the package existed
// in the container where the later stages ran. Registry installs are real
// directories; links/workspaces are deliberately not credited as public npm.
func installedNPMVersion(dir, name string) string {
	base := filepath.Join(dir, "node_modules")
	pkgDir := filepath.Join(base, filepath.FromSlash(name))
	rel, err := filepath.Rel(base, pkgDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	info, err := os.Lstat(pkgDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	metadataPath := filepath.Join(pkgDir, "package.json")
	metadataInfo, err := os.Lstat(metadataPath)
	if err != nil || !metadataInfo.Mode().IsRegular() || metadataInfo.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	var metadata struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	b, err := os.ReadFile(metadataPath)
	if err != nil || json.Unmarshal(b, &metadata) != nil || metadata.Name != name || !concreteNPMVersion(metadata.Version) {
		return ""
	}
	return metadata.Version
}

// npmResolved reads package-lock.json, preferring the top-level install of
// the package over a nested copy some dependency pulled in.
func npmResolved(dir, name string) string {
	body := readFile(dir, "package-lock.json")
	if body == "" {
		return ""
	}
	var lock struct {
		Packages map[string]struct {
			Name                 string            `json:"name"`
			Version              string            `json:"version"`
			Resolved             string            `json:"resolved"`
			Link                 bool              `json:"link"`
			Dependencies         map[string]string `json:"dependencies"`
			DevDependencies      map[string]string `json:"devDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
		} `json:"packages"`
	}
	if json.Unmarshal([]byte(body), &lock) != nil {
		return ""
	}
	if e, ok := lock.Packages["node_modules/"+name]; ok {
		if e.Link || (e.Name != "" && e.Name != name) ||
			!concreteNPMVersion(e.Version) || !npmRegistryArtifactURL(e.Resolved, name) {
			return ""
		}
		root := lock.Packages[""]
		for _, specs := range []map[string]string{
			root.Dependencies, root.DevDependencies, root.OptionalDependencies,
		} {
			if spec, declared := specs[name]; declared && nonRegistryNPMReference(spec) {
				return ""
			}
		}
		if installedNPMVersion(dir, name) != e.Version {
			return ""
		}
		return e.Version
	}
	return ""
}

func cargoLockResolved(dir, name string) string {
	if cargoOverridesCratesIO(dir) {
		return ""
	}
	body := readFile(dir, "Cargo.lock")
	if body == "" {
		return ""
	}
	matches := 0
	version := ""
	for _, block := range strings.Split(body, "[[package]]") {
		n := cargoLockValue(block, "name")
		if n != name {
			continue
		}
		matches++
		v := cargoLockValue(block, "version")
		source := cargoLockValue(block, "source")
		if v == "" || !publicCratesIOSource(source) {
			return ""
		}
		version = v
	}
	// Multiple matching blocks are ambiguous even when their version strings
	// happen to agree: Cargo.lock does not identify the direct dependency
	// edge without walking the root package's graph.
	if matches != 1 {
		return ""
	}
	if !cargoCrateFetched(dir, name, version) {
		return ""
	}
	return version
}

func cargoCrateFetched(dir, name, version string) bool {
	want := name + "-" + version + ".crate"
	root := filepath.Join(dir, ".csx-vendor", "cargo", "registry", "cache")
	found := 0
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() != want {
			return nil
		}
		if info, statErr := entry.Info(); statErr == nil && info.Mode().IsRegular() {
			found++
		}
		return nil
	})
	return found == 1
}

func cargoOverridesCratesIO(dir string) bool {
	sectionRe := regexp.MustCompile(`(?mi)^\s*\[\s*source\.crates-io\s*\]\s*(?:#.*)?$`)
	replaceRe := regexp.MustCompile(`(?mi)^\s*replace-with\s*=`)
	registryRe := regexp.MustCompile(`(?mi)^\s*\[\s*registries\.crates-io\s*\]\s*(?:#.*)?$`)
	for _, name := range []string{".cargo/config.toml", ".cargo/config"} {
		body := readFile(dir, name)
		if body != "" && (sectionRe.MatchString(body) || replaceRe.MatchString(body) || registryRe.MatchString(body)) {
			return true
		}
	}
	return false
}

func cargoLockValue(block, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*=\s*"([^"]+)"\s*\r?$`)
	if match := re.FindStringSubmatch(block); match != nil {
		return match[1]
	}
	return ""
}

func publicCratesIOSource(source string) bool {
	return source == "registry+https://github.com/rust-lang/crates.io-index" ||
		strings.HasPrefix(source, "sparse+https://index.crates.io/")
}

func gemfileLockResolved(dir, name string) string {
	body := readFile(dir, "Gemfile.lock")
	if body == "" {
		return ""
	}
	type lockSection struct {
		kind  string
		lines []string
	}
	var sections []lockSection
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			sections = append(sections, lockSection{kind: strings.TrimSpace(line)})
			continue
		}
		if len(sections) > 0 {
			sections[len(sections)-1].lines = append(sections[len(sections)-1].lines, line)
		}
	}

	matches := 0
	version := ""
	specRe := regexp.MustCompile(`^ {4}` + regexp.QuoteMeta(name) + ` \(([^)\s]+)\)\s*$`)
	for _, section := range sections {
		sectionMatches := 0
		publicRemotes := true
		remoteCount := 0
		for _, line := range section.lines {
			if strings.HasPrefix(line, "  remote:") {
				remoteCount++
				remote := strings.TrimSpace(strings.TrimPrefix(line, "  remote:"))
				if !publicRubyGemsURL(remote) {
					publicRemotes = false
				}
			}
			if match := specRe.FindStringSubmatch(line); match != nil {
				sectionMatches++
				version = match[1]
			}
		}
		if sectionMatches == 0 {
			continue
		}
		matches += sectionMatches
		// A matching GIT/PATH entry is an identity-changing or local source.
		// A GEM block without one unambiguous public remote is no better.
		if section.kind != "GEM" || remoteCount != 1 || !publicRemotes {
			return ""
		}
	}
	if matches != 1 {
		return ""
	}
	if installedGemVersion(dir, name) != version {
		return ""
	}
	return version
}

func installedGemVersion(dir, name string) string {
	root := filepath.Join(dir, ".csx-vendor", "gems")
	versions := map[string]bool{}
	nameRe := regexp.MustCompile(`(?m)^\s*s\.name\s*=\s*"` + regexp.QuoteMeta(name) + `"(?:\.freeze)?\s*$`)
	versionRe := regexp.MustCompile(`(?m)^\s*s\.version\s*=\s*"([^"\s]+)"(?:\.freeze)?\s*$`)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			filepath.Base(filepath.Dir(path)) != "specifications" || filepath.Ext(path) != ".gemspec" {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || !nameRe.Match(data) {
			return nil
		}
		if match := versionRe.FindSubmatch(data); match != nil {
			versions[string(match[1])] = true
		}
		return nil
	})
	return onlyVersion(versions)
}

func publicRubyGemsURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.User == nil &&
		strings.EqualFold(u.Hostname(), "rubygems.org")
}

func mixLockResolved(dir, name string) string {
	body := readFile(dir, "mix.lock")
	if body == "" {
		return ""
	}
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*:\s*\{`)
	matches := re.FindAllStringIndex(body, -1)
	if len(matches) != 1 {
		return ""
	}
	start := matches[0][1] - 1 // the opening `{` included by the match
	end := matchingDelimiter(body, start, '{', '}')
	if end < 0 {
		return ""
	}
	fields := splitTopLevel(body[start+1:end], ',')
	if len(fields) < 7 || strings.TrimSpace(fields[0]) != ":hex" {
		return ""
	}
	identity, ok := elixirAtom(fields[1])
	if !ok || identity != name {
		return ""
	}
	version, ok := quotedString(fields[2])
	if !ok || version == "" {
		return ""
	}
	repository, ok := quotedString(fields[6])
	if !ok || repository != "hexpm" {
		return ""
	}
	info, err := os.Lstat(filepath.Join(dir, "deps", filepath.FromSlash(name), "mix.exs"))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	return version
}

func matchingDelimiter(s string, start int, open, close byte) int {
	depth := 0
	quoted := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			continue
		}
		switch c {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitTopLevel(s string, separator byte) []string {
	var fields []string
	start := 0
	depth := 0
	quoted := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		default:
			if c == separator && depth == 0 {
				fields = append(fields, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	fields = append(fields, strings.TrimSpace(s[start:]))
	return fields
}

func quotedString(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", false
	}
	v, err := strconv.Unquote(s)
	return v, err == nil
}

func elixirAtom(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, ":") {
		return "", false
	}
	s = strings.TrimPrefix(s, ":")
	if strings.HasPrefix(s, `"`) {
		return quotedString(s)
	}
	if s == "" || strings.ContainsAny(s, " \t\r\n,{}[]()") {
		return "", false
	}
	return s, true
}

func pubspecLockResolved(dir, name string) string {
	body := readFile(dir, "pubspec.lock")
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	inPackages := false
	matches := 0
	version := ""
	for i := 0; i < len(lines); {
		line := lines[i]
		indent := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)
		if indent == 0 {
			inPackages = trimmed == "packages:"
			i++
			continue
		}
		if !inPackages || indent != 2 || !strings.HasSuffix(trimmed, ":") {
			i++
			continue
		}
		packageName := unquoteYAMLScalar(strings.TrimSuffix(trimmed, ":"))
		j := i + 1
		for j < len(lines) {
			if strings.TrimSpace(lines[j]) != "" && leadingSpaces(lines[j]) <= 2 {
				break
			}
			j++
		}
		if packageName == name {
			matches++
			v, public := publicPubPackageBlock(lines[i+1:j], name)
			if !public {
				return ""
			}
			version = v
		}
		i = j
	}
	if matches != 1 {
		return ""
	}
	if !pubPackageInstalled(dir, name, version) {
		return ""
	}
	return version
}

func pubPackageInstalled(dir, name, version string) bool {
	body := readFile(dir, ".dart_tool/package_config.json")
	if body == "" {
		return false
	}
	var config struct {
		Packages []struct {
			Name    string `json:"name"`
			RootURI string `json:"rootUri"`
		} `json:"packages"`
	}
	if json.Unmarshal([]byte(body), &config) != nil {
		return false
	}
	wantSuffix := "/.csx-vendor/pub/hosted/pub.dev/" + name + "-" + version
	matches := 0
	for _, p := range config.Packages {
		if p.Name == name && strings.HasPrefix(p.RootURI, "file://") &&
			strings.HasSuffix(strings.TrimSuffix(p.RootURI, "/"), wantSuffix) {
			matches++
		}
	}
	return matches == 1
}

func publicPubPackageBlock(lines []string, name string) (string, bool) {
	version := ""
	source := ""
	descriptionName := ""
	descriptionURL := ""
	inDescription := false
	for _, line := range lines {
		indent := leadingSpaces(line)
		trimmed := strings.TrimSpace(line)
		if indent == 4 {
			inDescription = trimmed == "description:"
			if value, ok := yamlValue(trimmed, "source"); ok {
				source = value
			}
			if value, ok := yamlValue(trimmed, "version"); ok {
				version = value
			}
			continue
		}
		if inDescription && indent >= 6 {
			if value, ok := yamlValue(trimmed, "name"); ok {
				descriptionName = value
			}
			if value, ok := yamlValue(trimmed, "url"); ok {
				descriptionURL = value
			}
		}
	}
	return version, version != "" && source == "hosted" &&
		descriptionName == name && publicPubDevURL(descriptionURL)
}

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

func yamlValue(line, key string) (string, bool) {
	prefix := key + ":"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	return unquoteYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line, prefix))), true
}

func unquoteYAMLScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') ||
		(s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func publicPubDevURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.User == nil &&
		strings.EqualFold(u.Hostname(), "pub.dev") && (u.EscapedPath() == "" || u.EscapedPath() == "/")
}

// goListResolved reads the build list emitted by `go list -m -json all`
// during resolve. go.mod alone is not enough: MVS can upgrade a direct
// require and a replace can change both version and module identity.
func goListResolved(dir, name string) string {
	body := readFile(dir, ".csx-vendor/go-modules.json")
	if body == "" {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(body))
	for {
		var mod struct {
			Path    string `json:"Path"`
			Version string `json:"Version"`
			Replace *struct {
				Path    string `json:"Path"`
				Version string `json:"Version"`
			} `json:"Replace"`
		}
		if err := dec.Decode(&mod); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return ""
		}
		if mod.Path != name {
			continue
		}
		if mod.Replace != nil {
			// A local or identity-changing replacement cannot be represented as
			// a new version of the declared purl. Omit it rather than lie.
			if mod.Replace.Path != name || mod.Replace.Version == "" {
				return ""
			}
			return mod.Replace.Version
		}
		return mod.Version
	}
	return ""
}

var pep503NameRe = regexp.MustCompile(`[-_.]+`)

// distInfoResolved reads what pip actually installed. Python has no
// lockfile by default, and requirements.txt is a request rather than a
// result — the METADATA inside the .dist-info directory is the result. The
// directory name is not evidence: both distribution names and versions may
// contain dashes, so splitting it can silently attribute the wrong identity.
func distInfoResolved(dir, name string) string {
	base := filepath.Join(dir, ".csx-vendor", "py")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	target := normalizePythonName(name)
	matches := 0
	version := ""
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".dist-info") {
			continue
		}
		infoDir := filepath.Join(base, e.Name())
		metadataName, metadataVersion, ok := pythonMetadataIdentity(filepath.Join(infoDir, "METADATA"))
		if !ok || normalizePythonName(metadataName) != target {
			continue
		}
		matches++
		// PEP 610 direct_url.json is written for local directories, VCS and
		// direct archive URLs. None establishes the declared PyPI identity.
		if _, err := os.Lstat(filepath.Join(infoDir, "direct_url.json")); err == nil {
			return ""
		}
		version = metadataVersion
	}
	if matches != 1 {
		return ""
	}
	reportedVersion, ok := pipReportResolved(dir, name)
	if !ok || reportedVersion != version {
		return ""
	}
	return version
}

func normalizePythonName(name string) string {
	return pep503NameRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
}

func pythonMetadataIdentity(path string) (name, version string, ok bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	nameCount := 0
	versionCount := 0
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if line == "" { // RFC-style headers end at the first empty line.
			break
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			nameCount++
			name = strings.TrimSpace(value)
		case "version":
			versionCount++
			version = strings.TrimSpace(value)
		}
	}
	return name, version, nameCount == 1 && versionCount == 1 && name != "" && version != ""
}

// pipReportResolved supplies the provenance METADATA deliberately does not:
// --report records the download URL and whether the requirement was direct.
// Both records must agree before a public PyPI purl is signed.
func pipReportResolved(dir, name string) (string, bool) {
	body := readFile(dir, ".csx-vendor/pip-report.json")
	if body == "" {
		return "", false
	}
	type reportInstall struct {
		DownloadInfo struct {
			URL         string          `json:"url"`
			VCSInfo     json.RawMessage `json:"vcs_info"`
			DirInfo     json.RawMessage `json:"dir_info"`
			ArchiveInfo *struct {
				Hash   string            `json:"hash"`
				Hashes map[string]string `json:"hashes"`
			} `json:"archive_info"`
		} `json:"download_info"`
		IsDirect *bool `json:"is_direct"`
		Metadata struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"metadata"`
	}
	var report struct {
		Install []reportInstall `json:"install"`
	}
	if json.Unmarshal([]byte(body), &report) != nil {
		return "", false
	}
	target := normalizePythonName(name)
	matches := 0
	version := ""
	for _, install := range report.Install {
		if normalizePythonName(install.Metadata.Name) != target {
			continue
		}
		matches++
		archive := install.DownloadInfo.ArchiveInfo
		if install.IsDirect == nil || *install.IsDirect || install.Metadata.Version == "" ||
			hasJSONValue(install.DownloadInfo.VCSInfo) || hasJSONValue(install.DownloadInfo.DirInfo) ||
			archive == nil || (archive.Hash == "" && len(archive.Hashes) == 0) ||
			!publicPyPIArtifactURL(install.DownloadInfo.URL) {
			return "", false
		}
		version = install.Metadata.Version
	}
	return version, matches == 1
}

func hasJSONValue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func publicPyPIArtifactURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.User == nil &&
		strings.EqualFold(u.Hostname(), "files.pythonhosted.org") &&
		strings.HasPrefix(u.EscapedPath(), "/packages/") && u.RawQuery == "" && u.Fragment == ""
}
