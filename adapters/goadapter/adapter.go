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

// ClassifyCommand maps go, gotestsum and golangci-lint invocations to
// observation stages.
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
	switch tool {
	case "go":
		return a.classifyGo(argv[1:])
	case "gofmt":
		return scanner.CommandProfile{Tool: "gofmt"}
	case "goimports":
		return scanner.CommandProfile{Tool: "goimports"}
	case "staticcheck", "govulncheck":
		return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: tool}
	case "gotestsum":
		// gotestsum runs go test unless its first argument names one of its
		// own subcommands: `tool slowest` reads a finished run's JSON and
		// `help` prints usage, so neither one tested anything.
		if len(argv) > 1 && (argv[1] == "tool" || argv[1] == "help") {
			return scanner.CommandProfile{Tool: "gotestsum"}
		}
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "gotestsum"}
	case "golangci-lint":
		// The root command defaults to run when given paths or run flags.
		// Its administrative subcommands and help only print information or
		// change configuration, so they are not project checks.
		if golangciLintRunsChecks(argv[1:]) {
			return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: "golangci-lint"}
		}
		return scanner.CommandProfile{Tool: "golangci-lint"}
	}
	return scanner.CommandProfile{}
}

func golangciLintRunsChecks(args []string) bool {
	subcommand := golangciSubcommand(args)
	if subcommand == "run" {
		return true
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return false
		}
		if name, value, ok := strings.Cut(arg, "="); ok &&
			(name == "--help" || name == "-h") && value != "false" {
			return false
		}
	}
	switch subcommand {
	case "cache", "completion", "config", "help", "linters", "version", "fmt":
		return false
	case "":
		if len(args) == 0 {
			return true
		}
		// Root run flags use the default run command. Avoid treating a value
		// consumed by an unrelated or malformed option (for example
		// `--color run`) as evidence that a lint run was dispatched.
		for i, arg := range args {
			if arg == "--" {
				return false
			}
			name, value, inline := strings.Cut(arg, "=")
			flagName := strings.TrimLeft(name, "-")
			if inline {
				if flagName == "version" || flagName == "V" {
					return false
				}
				if flagName == "color" {
					return value == "auto" || value == "always" || value == "never"
				}
				if flagName == "help" || flagName == "h" {
					return value == "false"
				}
				return true
			}
			if flagName == "version" || flagName == "V" {
				return false
			}
			if golangciBoolFlags[flagName] && flagName != "help" {
				return true
			}
			if golangciRunValueFlags[flagName] {
				if flagName == "color" {
					if i+1 < len(args) {
						return args[i+1] == "auto" || args[i+1] == "always" || args[i+1] == "never"
					}
					return false
				}
				return true
			}
		}
		return false
	default:
		// A non-command positional argument is a lint target; golangci-lint
		// runs its default `run` command when only targets/options are given.
		return true
	}
}

// classifyGo reads the subcommand of a go invocation.
//
// cmd/go takes its working-directory flag only in first position, as -C dir
// or -C=dir with one dash or two. A build run against a nested module is
// still a build, so the flag and its directory are stepped over rather than
// read as the subcommand.
func (a *Adapter) classifyGo(args []string) scanner.CommandProfile {
	if len(args) > 0 {
		switch flag := args[0]; {
		case flag == "-C" || flag == "--C":
			args = args[min(2, len(args)):]
		case strings.HasPrefix(flag, "-C=") || strings.HasPrefix(flag, "--C="):
			args = args[1:]
		}
	}
	if len(args) == 0 {
		return scanner.CommandProfile{Tool: "go"}
	}
	switch args[0] {
	case "build", "vet", "install":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: "go"}
	case "test":
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"}
	case "run":
		return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: "go"}
	case "tool":
		return a.classifyGoTool(args[1:])
	}
	return scanner.CommandProfile{Tool: "go"}
}

// classifyGoTool reads `go tool NAME`. compile and vet are the toolchain's
// own compiler and checker. Any other NAME may be a module tool declared
// with go.mod's tool directive, run by its package path or that path's last
// element, so `go tool gotestsum` is gotestsum and is classified as one.
func (a *Adapter) classifyGoTool(args []string) scanner.CommandProfile {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "-n" {
			// `go tool -n` prints the command instead of running the tool.
			// A successful dry run is not compile evidence.
			return scanner.CommandProfile{Tool: "go"}
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return scanner.CommandProfile{Tool: "go"}
	}
	switch args[0] {
	case "compile", "vet":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: "go"}
	}
	// A module tool whose path ends in /go is not a second go command.
	if p := a.ClassifyCommand(args); p.Known && p.Tool != "go" {
		return p
	}
	return scanner.CommandProfile{Tool: "go"}
}

// golangciBoolFlags are the boolean flags golangci-lint's root command
// knows: the persistent -h/--help and -v/--verbose, and its own --version.
var golangciBoolFlags = map[string]bool{"h": true, "help": true, "v": true, "verbose": true, "version": true}

var golangciRunValueFlags = map[string]bool{
	"build-tags": true, "color": true, "concurrency": true, "config": true,
	"issues-exit-code": true, "modules-download-mode": true, "new": true,
	"new-from-patch": true, "new-from-rev": true, "out-format": true,
	"path-mode": true, "path-prefix": true, "timeout": true,
}

// golangciSubcommand returns the subcommand golangci-lint dispatches args
// to. It steps over flags the way cobra does when it looks the command up:
// a flag written without = takes the next argument as its value unless the
// root knows it as a boolean. It also scans flags after the subcommand so
// help-only invocations are not recorded as successful lint runs.
func golangciSubcommand(args []string) string {
	command := ""
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--help" || arg == "-h" {
			return ""
		}
		if name, value, ok := strings.Cut(arg, "="); ok &&
			(name == "--help" || name == "-h") && value != "false" {
			return ""
		}
		switch {
		case arg == "--":
			return command
		case arg == "":
		case !strings.HasPrefix(arg, "-"):
			if command == "" {
				command = arg
			}
		case strings.Contains(arg, "="):
		case strings.HasPrefix(arg, "--"):
			if !golangciBoolFlags[arg[2:]] {
				args = args[min(1, len(args)):]
			}
		case len(arg) == 2:
			if !golangciBoolFlags[arg[1:]] {
				args = args[min(1, len(args)):]
			}
		}
	}
	return command
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
		line, comment := cutGoModComment(raw)
		line = strings.TrimSpace(line)
		indirect := strings.HasPrefix(strings.TrimSpace(comment), "indirect")
		if line == "" {
			continue
		}
		fields := goModFields(line)
		switch block {
		case blockRequire:
			if fields[0] == ")" {
				block = blockNone
			} else if len(fields) >= 2 {
				requires = append(requires, requireEntry{
					unquoteGoModToken(fields[0]), unquoteGoModToken(fields[1]), indirect,
				})
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
				requires = append(requires, requireEntry{
					unquoteGoModToken(fields[1]), unquoteGoModToken(fields[2]), indirect,
				})
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
	oldPath := unquoteGoModToken(fields[0])
	// `old vX => new vY` names the version the directive governs; `old =>
	// new vY` governs every version.
	oldVersion := ""
	if arrow == 2 {
		oldVersion = unquoteGoModToken(strings.TrimSpace(fields[1]))
	}
	target := unquoteGoModToken(fields[arrow+1])
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
		if v := unquoteGoModToken(strings.TrimSpace(fields[arrow+2])); v != "" {
			moduleReplaced[oldPath] = append(moduleReplaced[oldPath],
				replacement{path: target, version: v, oldVersion: oldVersion})
		}
	}
}

func unquoteGoModToken(token string) string {
	if unquoted, err := strconv.Unquote(token); err == nil {
		return unquoted
	}
	return token
}

// cutGoModComment splits a line at its `//` comment. A `//` inside a quoted
// token is part of the token, so a "//server/share" replacement path is not
// cut down to a lone quote that unquotes to nothing.
func cutGoModComment(raw string) (line, comment string) {
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"', '`':
			if quoted, err := strconv.QuotedPrefix(raw[i:]); err == nil {
				i += len(quoted) - 1
			}
		case '/':
			if strings.HasPrefix(raw[i:], "//") {
				return raw[:i], raw[i+2:]
			}
		}
	}
	return raw, ""
}

// goModFields splits a directive on whitespace while keeping quoted tokens
// intact so a local replacement path may contain spaces.
func goModFields(line string) []string {
	var fields []string
	for line != "" {
		line = strings.TrimLeft(line, " \t\r\n")
		if line == "" {
			break
		}
		if line[0] == '"' || line[0] == '`' {
			if quoted, err := strconv.QuotedPrefix(line); err == nil {
				fields = append(fields, quoted)
				line = line[len(quoted):]
				continue
			}
		}
		end := strings.IndexAny(line, " \t\r\n")
		if end < 0 {
			fields = append(fields, line)
			break
		}
		fields = append(fields, line[:end])
		line = line[end:]
	}
	return fields
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
