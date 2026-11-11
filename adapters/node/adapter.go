// Package node implements the npm-ecosystem scanner adapter: lockfile-resolved
// dependency scanning (package-lock.json v2/v3, pnpm-lock.yaml v9, classic
// yarn.lock), static import/symbol extraction for TS/JS sources, command
// classification, and environment hints.
package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// Adapter implements scanner.Adapter for the "npm" ecosystem.
type Adapter struct{}

var _ scanner.Adapter = Adapter{}

func (Adapter) Ecosystem() string { return "npm" }

func (Adapter) Capabilities() []string { return []string{"A0", "A1", "A2", "A4"} }

func (Adapter) Detect(dir string) bool {
	return fileExists(filepath.Join(dir, "package.json"))
}

// packageJSON is the subset of package.json the adapter needs.
type packageJSON struct {
	Name                 string            `json:"name"`
	Type                 string            `json:"type"`
	PackageManager       string            `json:"packageManager"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

func readPackageJSON(dir string) (packageJSON, bool) {
	var pj packageJSON
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return pj, false
	}
	if err := json.Unmarshal(data, &pj); err != nil {
		return pj, false
	}
	return pj, true
}

func (Adapter) EnvironmentHints(ctx context.Context, dir string) map[string]string {
	h := map[string]string{
		"ecosystem": "npm",
		"runtime":   "node", // runtimeVersion is probed by the caller
	}
	pj, _ := readPackageJSON(dir)

	if pj.Type == "module" {
		h["moduleSystem"] = "esm"
	} else {
		h["moduleSystem"] = "cjs"
	}

	if fileExists(filepath.Join(dir, "tsconfig.json")) {
		h["language"] = "typescript"
	} else {
		h["language"] = "javascript"
	}

	if pj.PackageManager != "" {
		name, ver, _ := strings.Cut(pj.PackageManager, "@")
		if name != "" {
			h["packageManager"] = name
			if ver != "" {
				ver, _, _ = strings.Cut(ver, "+") // drop integrity suffix
				h["packageManagerVersion"] = ver
			}
		}
	}
	if h["packageManager"] == "" {
		switch {
		case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
			h["packageManager"] = "pnpm"
		case fileExists(filepath.Join(dir, "yarn.lock")):
			h["packageManager"] = "yarn"
		case fileExists(filepath.Join(dir, "package-lock.json")):
			h["packageManager"] = "npm"
		}
	}
	return h
}

func (Adapter) ClassifyCommand(argv []string) scanner.CommandProfile {
	if len(argv) == 0 {
		return scanner.CommandProfile{}
	}
	tool := baseTool(argv[0])
	rest := argv[1:]

	if tool == "npx" {
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return scanner.CommandProfile{}
		}
		return Adapter{}.ClassifyCommand(rest)
	}

	switch tool {
	case "tsc":
		return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: tool}
	case "node":
		for _, arg := range rest {
			if arg == "--test" {
				return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
			}
		}
		return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: tool}
	case "tsup", "webpack", "esbuild", "rollup":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: tool}
	case "jest", "vitest", "mocha":
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
	case "vite", "next":
		if len(rest) > 0 && rest[0] == "build" {
			return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: tool}
		}
		return scanner.CommandProfile{Tool: tool}
	case "npm", "pnpm", "yarn":
		args := skipPackageManagerOptions(tool, rest)
		if tool == "yarn" && len(args) >= 2 && args[0] == "workspace" {
			// Yarn's workspace command takes the workspace name before the
			// script command (which may itself use `run`).
			args = args[2:]
		}
		return classifyPackageManagerCommand(tool, args)
	}
	return scanner.CommandProfile{}
}

func classifyPackageManagerCommand(tool string, args []string) scanner.CommandProfile {
	if len(args) == 0 {
		return scanner.CommandProfile{Tool: tool}
	}
	if args[0] != "run" && isPackageExecutor(tool, args[0]) {
		command := skipExecutorOptions(tool, args[1:])
		if len(command) > 0 {
			return Adapter{}.ClassifyCommand(command)
		}
		return scanner.CommandProfile{Tool: tool}
	}
	if args[0] == "run" {
		args = args[1:]
	}
	if len(args) == 0 {
		return scanner.CommandProfile{Tool: tool}
	}
	switch args[0] {
	case "build":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: tool}
	case "test", "t", "tst":
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
	default:
		return scanner.CommandProfile{Tool: tool}
	}
}

var packageManagerValueOptions = map[string]map[string]bool{
	"npm": {
		"--prefix": true, "--workspace": true, "-w": true, "-C": true,
	},
	"pnpm": {
		"--filter": true, "-F": true, "--filter-prod": true,
		"--dir": true, "-C": true,
		"--workspace-concurrency": true, "--reporter": true,
	},
	"yarn": {
		"--cwd": true, "--use-yarnrc": true, "--cache-folder": true,
		"--modules-folder": true, "--mutex": true, "--network-concurrency": true,
	},
}

// skipPackageManagerOptions advances to the actual package command while
// consuming options that take a separate value. Unknown options are treated
// as flags, allowing new boolean flags without mistaking their names for a
// command; known value options consume exactly one following argument.
func skipPackageManagerOptions(tool string, args []string) []string {
	for len(args) > 0 {
		if args[0] == "--" {
			return args[1:]
		}
		if !strings.HasPrefix(args[0], "-") {
			return args
		}
		option := args[0]
		args = args[1:]
		if !strings.Contains(option, "=") && packageManagerValueOptions[tool][option] && len(args) > 0 {
			args = args[1:]
		}
	}
	return nil
}

func isPackageExecutor(tool, command string) bool {
	switch tool {
	case "npm":
		return command == "exec"
	case "pnpm", "yarn":
		return command == "exec" || command == "dlx"
	default:
		return false
	}
}

// executorValueOptions lists, per package manager, the exec/dlx options whose
// value is a separate token. A package, workspace or filter name can equal a
// tool name ("--workspace jest"), so the value must not be classified as the
// command. Forms using an equals sign are already a single option token.
// Short options differ between managers: npm "-w" is --workspace and "-p" is
// the boolean --parseable, while pnpm "-w" is the boolean --workspace-root.
var executorValueOptions = map[string]map[string]bool{
	"npm": {
		"--package": true, "--call": true, "-c": true,
		"--workspace": true, "-w": true, "--prefix": true, "-C": true,
	},
	"pnpm": {
		"--package": true, "--allow-build": true,
		"--filter": true, "-F": true, "--filter-prod": true,
		"--dir": true, "-C": true, "--resume-from": true,
		"--workspace-concurrency": true, "--reporter": true,
		"--test-pattern": true, "--changed-files-ignore-pattern": true,
	},
	"yarn": {"--package": true, "-p": true},
}

func skipExecutorOptions(tool string, args []string) []string {
	for len(args) > 0 {
		if args[0] == "--" {
			return args[1:]
		}
		if !strings.HasPrefix(args[0], "-") {
			return args
		}
		option := args[0]
		args = args[1:]
		if executorValueOptions[tool][option] && len(args) > 0 {
			args = args[1:]
		}
	}
	return nil
}

// baseTool normalizes argv[0]: strips directories and Windows launcher
// extensions, lowercases.
func baseTool(s string) string {
	s = strings.ToLower(filepath.Base(strings.ReplaceAll(s, `\`, "/")))
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		s = strings.TrimSuffix(s, ext)
	}
	return s
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
