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

var (
	sectionRe    = regexp.MustCompile(`^\s*\[{1,2}\s*([^\]]+?)\s*\]{1,2}`)
	manifestKVRe = regexp.MustCompile(`^\s*(?:"([^"]+)"|([A-Za-z0-9_-]+))\s*=\s*(.+?)\s*$`)
	pathOrGitRe  = regexp.MustCompile(`(?:^|[{,\s])(?:path|git)\s*=`)
)

// depSections are the manifest tables whose keys are direct dependencies.
// Dotted forms ([dependencies.serde], [target.'cfg(..)'.dependencies]) are
// matched by prefix/suffix below.
var depSections = []string{"dependencies", "dev-dependencies", "build-dependencies"}

// parseManifest extracts the root package name and the direct-dependency set
// with a PRIVATE marker for path=/git= entries.
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
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			sec := m[1]
			state, singleDep = stateNone, ""
			switch {
			case sec == "package":
				state = statePackage
			case isDepListSection(sec):
				state = stateDepList
			default:
				if name := singleDepName(sec); name != "" {
					state = stateSingleDep
					singleDep = name
					d := man.deps[name]
					man.deps[name] = d
				}
			}
			continue
		}
		m := manifestKVRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		if key == "" {
			key = m[2]
		}
		value := m[3]
		switch state {
		case statePackage:
			if key == "name" {
				man.rootName = strings.Trim(value, `"`)
			}
		case stateDepList:
			d := man.deps[key]
			if pathOrGitRe.MatchString(value) {
				d.pathOrGit = true
			}
			man.deps[key] = d
		case stateSingleDep:
			if key == "path" || key == "git" {
				d := man.deps[singleDep]
				d.pathOrGit = true
				man.deps[singleDep] = d
			}
		}
	}
	return man
}

func isDepListSection(sec string) bool {
	for _, ds := range depSections {
		if sec == ds || strings.HasSuffix(sec, "."+ds) {
			return true
		}
	}
	return false
}

// singleDepName returns the dependency name for [dependencies.<name>]-style
// sections, or "".
func singleDepName(sec string) string {
	for _, ds := range depSections {
		if name, ok := strings.CutPrefix(sec, ds+"."); ok && name != "" && !strings.Contains(name, ".") {
			return name
		}
	}
	return ""
}
