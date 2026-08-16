// Package goadapter implements the scanner.Adapter contract for the
// "golang" ecosystem: go.mod-resolved dependencies, go/parser-based
// symbol usage extraction, and go command classification.
package goadapter

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// Adapter is the golang ecosystem adapter.
type Adapter struct{}

// New returns the golang adapter.
func New() *Adapter { return &Adapter{} }

var _ scanner.Adapter = (*Adapter)(nil)

// Ecosystem returns "golang".
func (a *Adapter) Ecosystem() string { return "golang" }

// Capabilities per goal.md §13.1: dependency scan, symbol scan, command
// classification. No verifier adapter in Public v1.
func (a *Adapter) Capabilities() []string { return []string{"A0", "A1", "A2"} }

// Detect reports whether dir contains a go.mod.
func (a *Adapter) Detect(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && !fi.IsDir()
}

// ScanPackages parses go.mod require directives (single and block form).
// Modules replaced to a filesystem path are PRIVATE; everything else is
// UNKNOWN until the registry publicness check upgrades it.
func (a *Adapter) ScanPackages(ctx context.Context, dir string) ([]scanner.ResolvedPackage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, err
	}
	requires, localReplaced, moduleReplaced := parseGoMod(string(data))

	source := "go.mod"
	if fi, err := os.Stat(filepath.Join(dir, "go.sum")); err == nil && !fi.IsDir() {
		source = "go.mod+go.sum"
	}

	pkgs := make([]scanner.ResolvedPackage, 0, len(requires))
	for _, r := range requires {
		publicness := scanner.PublicnessUnknown
		if governs(localReplaced[r.path], r.version) {
			publicness = scanner.PublicnessPrivate
		}
		// A module replace decides what actually compiled. Reporting the
		// require line meant evidence produced by golang.org/x/net v0.38.0
		// was filed under v0.17.0, and another agent asking about v0.17.0
		// got a HIT backed by a build that never used it.
		name, version := r.path, r.version
		if rep, ok := pickReplacement(moduleReplaced[r.path], r.version); ok {
			name, version = rep.path, rep.version
		}
		pkgs = append(pkgs, scanner.ResolvedPackage{
			PURL:       domain.PURL{Ecosystem: "golang", Name: name, Version: version},
			Publicness: publicness,
			Direct:     !r.indirect,
			Source:     source,
		})
	}
	return pkgs, nil
}

// ClassifyCommand maps go tool invocations to observation stages.
func (a *Adapter) ClassifyCommand(argv []string) scanner.CommandProfile {
	if len(argv) == 0 {
		return scanner.CommandProfile{}
	}
	// Split on BOTH separators regardless of the host OS. filepath.ToSlash
	// is a no-op on Linux, so `C:\Go\bin\go.exe` survived intact and
	// filepath.Base returned the whole string — the command went
	// unclassified everywhere except Windows. Evidence is recorded on one
	// machine and aggregated on another, so a command line must parse the
	// same way on every platform.
	tool := strings.ToLower(path.Base(strings.ReplaceAll(argv[0], `\`, "/")))
	tool = strings.TrimSuffix(tool, ".exe")
	if tool != "go" {
		if tool == "gofmt" {
			return scanner.CommandProfile{Tool: "gofmt"}
		}
		return scanner.CommandProfile{}
	}
	if len(argv) < 2 {
		return scanner.CommandProfile{Tool: "go"}
	}
	switch argv[1] {
	case "build", "vet", "install":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: "go"}
	case "test":
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"}
	case "run":
		return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: "go"}
	}
	return scanner.CommandProfile{Tool: "go"}
}

// EnvironmentHints returns static golang hints; version probes are the
// caller's job (environment.Collect runs `go version` etc.).
func (a *Adapter) EnvironmentHints(ctx context.Context, dir string) map[string]string {
	return map[string]string{
		"ecosystem":      "golang",
		"runtime":        "go",
		"language":       "go",
		"packageManager": "go",
		"moduleSystem":   "",
	}
}

type requireEntry struct {
	path     string
	version  string
	indirect bool
}

// replacement is what a module replace directive redirects to.
type replacement struct{ path, version, oldVersion string }

// appliesTo reports whether this replace directive governs the version the
// build actually selected.
//
// `replace old vX => new vY` applies ONLY when the selected version is
// exactly vX; go ignores it otherwise. The left-hand version was parsed and
// thrown away, so a stale directive -- the ordinary residue of a `go get
// -u` -- was applied to whatever version the require line now names. A
// go.mod requiring golang.org/x/crypto v0.21.0 alongside a leftover
// `replace golang.org/x/crypto v0.0.0-2022... => golang.org/x/crypto
// v0.17.0` compiles v0.21.0, and evidence from that build was filed under
// v0.17.0: the next agent asking about v0.17.0 got a HIT backed by a build
// that never used it. That is the exact failure the comment at the call
// site says this code prevents.
//
// A version-less left side (`replace old => new vY`) applies to every
// version, which is why it stays unconditional.
func (r replacement) appliesTo(selected string) bool {
	return r.oldVersion == "" || r.oldVersion == selected
}

// pickReplacement chooses the directive go would actually apply, out of
// every replace directive written for one module path.
//
// A go.mod may legally carry several for the same path as long as the
// left-hand versions differ, and this is not an exotic shape: a CVE bump
// pinned for one version sitting above a catch-all, or the ordinary
// residue of `go get -u`. Keeping only the last one parsed made file order
// decide which version got recorded — and when the survivor was the
// non-matching one, NO replacement applied at all and evidence from a
// v0.31.0 build was filed under v0.21.0. That is the exact poisoning the
// comment at the call site says this code prevents; it was prevented for
// one directive and not for two.
//
// go's precedence is that an exact `path vX => …` outranks a version-less
// `path => …`, regardless of the order they appear in, so the exact match
// is looked for first and the wildcard is only the fallback.
func pickReplacement(reps []replacement, selected string) (replacement, bool) {
	var wildcard replacement
	var haveWildcard bool
	for _, r := range reps {
		if r.oldVersion == selected {
			return r, true
		}
		if r.oldVersion == "" && !haveWildcard {
			wildcard, haveWildcard = r, true
		}
	}
	return wildcard, haveWildcard
}

// governs reports whether any of these left-hand versions covers the
// version the build selected. An empty string is the version-less form and
// covers everything.
func governs(oldVersions []string, selected string) bool {
	for _, v := range oldVersions {
		if v == "" || v == selected {
			return true
		}
	}
	return false
}

// parseGoMod extracts require entries, the set of module paths whose
// replace target is a filesystem path (⇒ private, never uploadable), and
// the module replacements that decide which version actually compiles.
func parseGoMod(src string) ([]requireEntry, map[string][]string, map[string][]replacement) {
	var requires []requireEntry
	localReplaced := map[string][]string{}
	moduleReplaced := map[string][]replacement{}
	const (
		blockNone = iota
		blockRequire
		blockReplace
	)
	block := blockNone
	for _, raw := range strings.Split(src, "\n") {
		line, comment, _ := strings.Cut(raw, "//")
		line = strings.TrimSpace(line)
		indirect := strings.HasPrefix(strings.TrimSpace(comment), "indirect")
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		switch block {
		case blockRequire:
			if fields[0] == ")" {
				block = blockNone
			} else if len(fields) >= 2 {
				requires = append(requires, requireEntry{fields[0], fields[1], indirect})
			}
			continue
		case blockReplace:
			if fields[0] == ")" {
				block = blockNone
			} else {
				recordReplace(fields, localReplaced, moduleReplaced)
			}
			continue
		}
		switch fields[0] {
		case "require":
			if len(fields) == 2 && fields[1] == "(" {
				block = blockRequire
			} else if len(fields) >= 3 {
				requires = append(requires, requireEntry{fields[1], fields[2], indirect})
			}
		case "replace":
			if len(fields) == 2 && fields[1] == "(" {
				block = blockReplace
			} else {
				recordReplace(fields[1:], localReplaced, moduleReplaced)
			}
		}
	}
	return requires, localReplaced, moduleReplaced
}

// recordReplace handles `old [ver] => new [ver]` and marks old as locally
// replaced when new is a filesystem path.
func recordReplace(fields []string, localReplaced map[string][]string, moduleReplaced map[string][]replacement) {
	arrow := -1
	for i, f := range fields {
		if f == "=>" {
			arrow = i
			break
		}
	}
	if arrow < 1 || arrow+1 >= len(fields) {
		return
	}
	oldPath := fields[0]
	// `old vX => new vY` names the version the directive governs; `old =>
	// new vY` governs every version.
	oldVersion := ""
	if arrow == 2 {
		oldVersion = strings.TrimSpace(fields[1])
	}
	target := fields[arrow+1]
	if unquoted, err := strconv.Unquote(target); err == nil {
		target = unquoted
	}
	if isFilesystemPath(target) {
		// The left-hand version governs a local replace exactly as it
		// governs a module one. Recording only the path marked a module
		// PRIVATE on the strength of a directive that does not apply to
		// the version being built, which suppresses evidence that was
		// perfectly publishable.
		localReplaced[oldPath] = append(localReplaced[oldPath], oldVersion)
		return
	}
	// A module replace always carries a version — go.mod requires one
	// unless the target is a directory, which the branch above handled.
	if arrow+2 < len(fields) {
		if v := strings.TrimSpace(fields[arrow+2]); v != "" {
			moduleReplaced[oldPath] = append(moduleReplaced[oldPath],
				replacement{path: target, version: v, oldVersion: oldVersion})
		}
	}
}

// isFilesystemPath mirrors the go.mod rule: a replacement is a directory
// when it starts with ./ ../ (either slash), is absolute, or names a drive.
func isFilesystemPath(p string) bool {
	for _, prefix := range []string{"./", "../", ".\\", "..\\", "/", "\\"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
	}
	return false
}
