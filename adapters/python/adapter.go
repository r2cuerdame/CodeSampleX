// Package python implements the scanner.Adapter for the "pypi" ecosystem:
// lockfile-resolved versions from uv.lock / poetry.lock / requirements.txt
// pins, import-based symbol scanning, and command classification.
package python

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// Adapter is the Python/pypi ecosystem adapter.
type Adapter struct{}

var _ scanner.Adapter = (*Adapter)(nil)

// New returns the pypi adapter.
func New() *Adapter { return &Adapter{} }

// Ecosystem returns the purl ecosystem name.
func (*Adapter) Ecosystem() string { return "pypi" }

// Capabilities: usage observation, resolved versions, symbol families.
// No verifier adapter for Python in v1 (§19).
func (*Adapter) Capabilities() []string { return []string{"A0", "A1", "A2"} }

// Detect reports whether dir looks like a Python project.
func (*Adapter) Detect(dir string) bool {
	for _, f := range []string{"pyproject.toml", "requirements.txt", "uv.lock"} {
		if fileExists(filepath.Join(dir, f)) {
			return true
		}
	}
	return false
}

// ScanPackages returns lockfile-resolved dependencies. Lock source priority:
// uv.lock, then poetry.lock, then strict `==` pins in requirements.txt.
// Local/vcs references are PRIVATE; everything else starts UNKNOWN until the
// registry publicness check upgrades it (C12).
func (*Adapter) ScanPackages(ctx context.Context, dir string) ([]scanner.ResolvedPackage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	direct, haveDirect := pyprojectDirectDeps(filepath.Join(dir, "pyproject.toml"))

	var (
		entries   []lockEntry
		source    string
		allDirect bool
	)
	switch {
	case fileExists(filepath.Join(dir, "uv.lock")):
		data, err := os.ReadFile(filepath.Join(dir, "uv.lock"))
		if err != nil {
			return nil, err
		}
		entries, source = parseLockPackages(data, uvPrivateSourceRe), "uv.lock"
	case fileExists(filepath.Join(dir, "poetry.lock")):
		data, err := os.ReadFile(filepath.Join(dir, "poetry.lock"))
		if err != nil {
			return nil, err
		}
		entries, source = parseLockPackages(data, poetryPrivateSourceRe), "poetry.lock"
	case fileExists(filepath.Join(dir, "requirements.txt")):
		data, err := os.ReadFile(filepath.Join(dir, "requirements.txt"))
		if err != nil {
			return nil, err
		}
		entries, source = parseRequirements(data), "requirements.txt"
		allDirect = true // without pyproject deps, requirements lines are the direct set
	default:
		return nil, nil
	}

	seen := make(map[string]bool, len(entries))
	out := make([]scanner.ResolvedPackage, 0, len(entries))
	for _, e := range entries {
		name := normalizeDist(e.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		pub := scanner.PublicnessUnknown
		if e.Private {
			pub = scanner.PublicnessPrivate
		}
		isDirect := allDirect
		if haveDirect {
			isDirect = direct[name]
		}
		out = append(out, scanner.ResolvedPackage{
			PURL:       domain.PURL{Ecosystem: "pypi", Name: name, Version: e.Version},
			Publicness: pub,
			Direct:     isDirect,
			Source:     source,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PURL.Name < out[j].PURL.Name })
	return out, nil
}

// ClassifyCommand maps a Python-ecosystem argv to an observation stage.
func (a *Adapter) ClassifyCommand(argv []string) scanner.CommandProfile {
	if len(argv) == 0 {
		return scanner.CommandProfile{}
	}
	tool := strings.ToLower(filepath.Base(argv[0]))
	tool = strings.TrimSuffix(tool, ".exe")
	rest := argv[1:]

	switch {
	case tool == "pytest":
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "pytest"}
	case tool == "mypy":
		return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: "mypy"}
	case tool == "python" || strings.HasPrefix(tool, "python3"):
		switch moduleArg(rest) {
		case "pytest":
			return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "pytest"}
		case "mypy":
			return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: "mypy"}
		case "pip":
			if hasArg(rest, "install") {
				return scanner.CommandProfile{Stage: domain.StageUsed, Known: true, Tool: "pip"}
			}
			return scanner.CommandProfile{Tool: "pip"}
		}
		return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: tool}
	case tool == "pip" || tool == "pip3":
		if len(rest) > 0 && rest[0] == "install" {
			return scanner.CommandProfile{Stage: domain.StageUsed, Known: true, Tool: "pip"}
		}
		return scanner.CommandProfile{Tool: "pip"}
	case tool == "uv":
		if len(rest) == 0 {
			return scanner.CommandProfile{Tool: "uv"}
		}
		switch rest[0] {
		case "sync", "pip":
			return scanner.CommandProfile{Stage: domain.StageUsed, Known: true, Tool: "uv"}
		case "run":
			if len(rest) > 1 {
				if inner := a.ClassifyCommand(rest[1:]); inner.Known {
					return inner
				}
			}
			return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: "uv"}
		}
		return scanner.CommandProfile{Tool: "uv"}
	}
	return scanner.CommandProfile{Tool: tool}
}

func moduleArg(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-m" {
			return args[i+1]
		}
	}
	return ""
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// EnvironmentHints returns static fingerprint hints keyed by the JSON field
// names of domain.EnvironmentFingerprint; environment.Collect fills in
// versions.
func (*Adapter) EnvironmentHints(ctx context.Context, dir string) map[string]string {
	pm := "pip"
	if fileExists(filepath.Join(dir, "uv.lock")) {
		pm = "uv"
	}
	return map[string]string{
		"ecosystem":      "pypi",
		"runtime":        "python",
		"language":       "python",
		"packageManager": pm,
		"moduleSystem":   "",
	}
}
