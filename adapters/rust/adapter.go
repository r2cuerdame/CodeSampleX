// Package rust implements the scanner.Adapter for the "cargo" ecosystem.
// Lockfile and manifest are regex-parsed (no TOML dependency); symbols come
// from `use`/`extern crate` statements only — macro invocations are never
// attributed (goal.md §13.5, conservative).
package rust

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// Adapter implements scanner.Adapter for Cargo projects.
type Adapter struct{}

var _ scanner.Adapter = (*Adapter)(nil)

// New returns the cargo ecosystem adapter.
func New() *Adapter { return &Adapter{} }

func (*Adapter) Ecosystem() string { return "cargo" }

func (*Adapter) Capabilities() []string { return []string{"A0", "A1", "A2"} }

func (*Adapter) Detect(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "Cargo.toml"))
	return err == nil && !info.IsDir()
}

// crates.io source strings as they appear in Cargo.lock. Only these mark a
// candidate public package (still reported UNKNOWN; a registry check may
// upgrade to PUBLIC). Any other source — git, private registry — is PRIVATE,
// as is a missing source (path dependency).
const cratesIOGitIndex = "registry+https://github.com/rust-lang/crates.io-index"
const cratesIOSparsePrefix = "sparse+https://index.crates.io"

func (*Adapter) ScanPackages(ctx context.Context, dir string) ([]scanner.ResolvedPackage, error) {
	lock, err := os.ReadFile(filepath.Join(dir, "Cargo.lock"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	manifestBytes, _ := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	man := parseManifest(string(manifestBytes))

	var out []scanner.ResolvedPackage
	for _, e := range parseLockPackages(string(lock)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.name == "" || e.version == "" {
			continue
		}
		if e.source == "" && e.name == man.rootName {
			continue // the project itself, never a dependency
		}
		publicness := scanner.PublicnessPrivate
		if e.source == cratesIOGitIndex || strings.HasPrefix(e.source, cratesIOSparsePrefix) {
			publicness = scanner.PublicnessUnknown
		}
		dep, direct := man.deps[e.name]
		if direct && dep.pathOrGit {
			publicness = scanner.PublicnessPrivate
		}
		out = append(out, scanner.ResolvedPackage{
			PURL:       domain.PURL{Ecosystem: "cargo", Name: e.name, Version: e.version},
			Publicness: publicness,
			Direct:     direct,
			Source:     "Cargo.lock",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PURL.Name < out[j].PURL.Name })
	return out, nil
}

func (*Adapter) ClassifyCommand(argv []string) scanner.CommandProfile {
	if len(argv) == 0 {
		return scanner.CommandProfile{}
	}
	switch baseName(argv[0]) {
	case "rustc":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: "rustc"}
	case "cargo":
		sub := ""
		for _, a := range argv[1:] {
			// skip toolchain override (+nightly) and flags before the subcommand
			if strings.HasPrefix(a, "+") || strings.HasPrefix(a, "-") {
				continue
			}
			sub = a
			break
		}
		switch sub {
		case "build", "check", "clippy":
			return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: "cargo"}
		case "test":
			return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "cargo"}
		case "run":
			return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: "cargo"}
		}
		return scanner.CommandProfile{Tool: "cargo"}
	}
	return scanner.CommandProfile{}
}

func (*Adapter) EnvironmentHints(ctx context.Context, dir string) map[string]string {
	return map[string]string{
		"ecosystem":      "cargo",
		"runtime":        "",
		"language":       "rust",
		"packageManager": "cargo",
		"moduleSystem":   "",
	}
}

// baseName strips directory (either separator, any OS) and a .exe suffix.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	p = strings.ToLower(p)
	return strings.TrimSuffix(p, ".exe")
}

// --- Cargo.lock ---

type lockEntry struct {
	name, version, source string
}

var lockKVRe = regexp.MustCompile(`^(name|version|source) = "(.*)"$`)

// parseLockPackages scans [[package]] sections for name/version/source.
func parseLockPackages(content string) []lockEntry {
	var (
		out []lockEntry
		cur lockEntry
		in  bool
	)
	flush := func() {
		if in && cur.name != "" {
			out = append(out, cur)
		}
		cur = lockEntry{}
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "[") {
			flush()
			in = strings.TrimSpace(line) == "[[package]]"
			continue
		}
		if !in {
			continue
		}
		if m := lockKVRe.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "name":
				cur.name = m[2]
			case "version":
				cur.version = m[2]
			case "source":
				cur.source = m[2]
			}
		}
	}
	flush()
	return out
}

// --- Cargo.toml ---

type manifestDep struct {
	pathOrGit bool
}

type manifest struct {
	rootName string
	deps     map[string]manifestDep
}

const tomlKeyPart = `(?:"[^"]*"|'[^']*'|[A-Za-z0-9_-]+)`

var (
	sectionRe = regexp.MustCompile(`^\s*\[{1,2}\s*([^\]]+?)\s*\]{1,2}`)
	// The key may be dotted (alias.package = "real"); splitDottedKey reads it.
	manifestKVRe = regexp.MustCompile(`^\s*(` + tomlKeyPart + `(?:\s*\.\s*` + tomlKeyPart + `)*)\s*=\s*(.+?)\s*$`)
	pathOrGitRe  = regexp.MustCompile(`(?:^|[{,\s])(?:path|git)\s*=`)
	packageRe    = regexp.MustCompile(`(?:^|[{,\s])package\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	tomlStringRe = regexp.MustCompile(`^(?:"([^"]*)"|'([^']*)')`)
)

// depSections are the manifest tables whose keys are direct dependencies.
// They are matched on the last part of a table name ([dependencies],
// [target.'cfg(..)'.dependencies]) or, for a table describing one dependency,
// the part before it ([dependencies.serde], [target.'cfg(..)'.dependencies.serde]).
var depSections = []string{"dependencies", "dev-dependencies", "build-dependencies"}

// depDecl is one dependency as declared, before it is resolved to the crate
// it names.
type depDecl struct {
	pkg       string // package = "...": the real crate name of a renamed dependency
	pathOrGit bool
}

// set records one field of a dependency declaration.
func (d *depDecl) set(field, value string) {
	switch field {
	case "package":
		d.pkg = tomlString(value)
	case "path", "git":
		d.pathOrGit = true
	}
}

// parseManifest extracts the root package name and the direct-dependency set
// with a PRIVATE marker for path=/git= entries.
//
// A dependency is keyed by the crate it names, not the key it is declared
// under: `json = { package = "serde_json" }` is serde_json, the only name
// Cargo.lock knows it by, and a crate called "json" may be somebody else's
// dependency. A table's keys come in any order, so each section's
// declarations are collected and resolved when the section ends.
func parseManifest(content string) manifest {
	man := manifest{deps: map[string]manifestDep{}}
	const (
		stateNone = iota
		statePackage
		stateDepList   // [dependencies] etc: each key is a dep
		stateSingleDep // [dependencies.<name>]: keys describe one dep
	)
	state := stateNone
	singleDep := ""
	decls := map[string]*depDecl{} // the current section's, by declared name
	decl := func(name string) *depDecl {
		d := decls[name]
		if d == nil {
			d = &depDecl{}
			decls[name] = d
		}
		return d
	}
	flush := func() {
		for name, d := range decls {
			if d.pkg != "" {
				name = d.pkg
			}
			if name == "" {
				continue
			}
			md := man.deps[name]
			md.pathOrGit = md.pathOrGit || d.pathOrGit
			man.deps[name] = md
		}
		clear(decls)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			flush()
			sec := m[1]
			state, singleDep = stateNone, ""
			list, single := depSection(sec)
			switch {
			case sec == "package":
				state = statePackage
			case list:
				state = stateDepList
			case single != "":
				state, singleDep = stateSingleDep, single
				decl(single)
			}
			continue
		}
		m := manifestKVRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := splitDottedKey(m[1])
		value := m[2]
		switch state {
		case statePackage:
			if len(key) == 1 && key[0] == "name" {
				man.rootName = tomlString(value)
			}
		case stateDepList:
			d := decl(key[0])
			if len(key) == 1 { // name = "1.0" or name = { ... }
				d.pathOrGit = d.pathOrGit || pathOrGitRe.MatchString(value)
				if pm := packageRe.FindStringSubmatch(value); pm != nil {
					d.pkg = pm[1] + pm[2]
				}
			} else { // name.package = "...", name.path = "..."
				d.set(key[1], value)
			}
		case stateSingleDep:
			if len(key) == 1 {
				decl(singleDep).set(key[0], value)
			}
		}
	}
	flush()
	return man
}

// depSection classifies a manifest table name: list reports a table whose
// keys are dependencies, single names the one dependency a
// [<...>.dependencies.<name>] table describes. Target-specific tables differ
// only in their prefix, so only the last two parts are read.
func depSection(sec string) (list bool, single string) {
	parts := splitDottedKey(sec)
	n := len(parts)
	if isDepTable(parts[n-1]) {
		return true, ""
	}
	if n >= 2 && isDepTable(parts[n-2]) && parts[n-1] != "" {
		return false, parts[n-1]
	}
	return false, ""
}

func isDepTable(part string) bool {
	for _, ds := range depSections {
		if part == ds {
			return true
		}
	}
	return false
}

// splitDottedKey splits a TOML dotted key on the dots outside quotes and
// unquotes each part, so [target.'cfg(target_os = "linux")'.dependencies] and
// [target."cfg(target_os = \"linux\")".dependencies] read the same.
func splitDottedKey(key string) []string {
	var (
		parts []string
		cur   strings.Builder
		quote byte
	)
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case quote == '"' && c == '\\' && i+1 < len(key):
			i++
			cur.WriteByte(key[i])
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(parts, strings.TrimSpace(cur.String()))
}

// tomlString returns the content of the TOML string a value starts with, or
// "" when it does not start with one.
func tomlString(value string) string {
	m := tomlStringRe.FindStringSubmatch(value)
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}
