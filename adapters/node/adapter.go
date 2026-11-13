// Package node implements the npm-ecosystem scanner adapter: lockfile-resolved
// dependency scanning (package-lock.json v2/v3, pnpm-lock.yaml v9, classic
// yarn.lock, and Bun text bun.lock), static import/symbol extraction for TS/JS
// sources, command classification, and environment hints. Legacy binary
// bun.lockb scanning is not supported.
package node

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

	name, ver := packageManagerHint(dir, pj)
	if name == "" {
		// A workspace member carries no clue of its own; its root does.
		if root, _, _, ok := workspaceRoot(dir); ok {
			rootPJ, _ := readPackageJSON(root)
			name, ver = packageManagerHint(root, rootPJ)
		}
	}
	if name != "" {
		h["packageManager"] = name
		if ver != "" {
			h["packageManagerVersion"] = ver
		}
	}
	return h
}

// packageManagerHint reads the package.json "packageManager" field, falling
// back to which lockfile dir holds.
func packageManagerHint(dir string, pj packageJSON) (name, version string) {
	if pj.PackageManager != "" {
		name, ver, _ := strings.Cut(pj.PackageManager, "@")
		if name != "" {
			ver, _, _ = strings.Cut(ver, "+") // drop integrity suffix
			return name, ver
		}
	}
	switch {
	case fileExists(filepath.Join(dir, "bun.lock")), fileExists(filepath.Join(dir, "bun.lockb")):
		return "bun", ""
	case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
		return "pnpm", ""
	case fileExists(filepath.Join(dir, "yarn.lock")):
		return "yarn", ""
	case fileExists(filepath.Join(dir, "package-lock.json")):
		return "npm", ""
	}
	return "", ""
}

func (Adapter) ClassifyCommand(argv []string) scanner.CommandProfile {
	if len(argv) == 0 {
		return scanner.CommandProfile{}
	}
	tool := baseTool(argv[0])
	rest := argv[1:]

	if tool == "npx" || tool == "bunx" {
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return scanner.CommandProfile{}
		}
		return Adapter{}.ClassifyCommand(rest)
	}
	if tool == "bun" && len(rest) > 0 && rest[0] == "x" {
		rest = skipBunOptions(rest[1:])
		if len(rest) == 0 {
			return scanner.CommandProfile{Tool: tool}
		}
		return Adapter{}.ClassifyCommand(rest)
	}

	switch tool {
	case "tsc":
		return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: tool}
	case "eslint":
		return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: tool}
	case "biome":
		if len(rest) > 0 && (rest[0] == "check" || rest[0] == "lint" || rest[0] == "ci") {
			return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: tool}
		}
		return scanner.CommandProfile{Tool: tool}
	case "playwright":
		if len(rest) > 0 && rest[0] == "test" {
			return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
		}
		return scanner.CommandProfile{Tool: tool}
	case "cypress":
		if len(rest) > 0 && rest[0] == "run" {
			return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
		}
		return scanner.CommandProfile{Tool: tool}
	case "bun":
		return classifyBunCommand(rest)
	case "turbo":
		return classifyTurboCommand(rest)
	case "prettier":
		for _, arg := range rest {
			if arg == "--check" || arg == "-c" || arg == "--list-different" || strings.HasPrefix(arg, "--check=") {
				return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: tool}
			}
		}
		return scanner.CommandProfile{Tool: tool}
	case "oxlint":
		return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: tool}
	case "node":
		if nodeTestMode(rest) {
			return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
		}
		return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: tool}
	case "tsup", "webpack", "esbuild", "rollup", "swc":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: tool}
	case "jest", "vitest", "mocha", "ava":
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

func classifyBunCommand(args []string) scanner.CommandProfile {
	if len(args) == 0 {
		return scanner.CommandProfile{Tool: "bun"}
	}
	switch args[0] {
	case "test":
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "bun"}
	case "build":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: "bun"}
	case "run":
		args = skipBunOptions(args[1:])
		if len(args) == 0 {
			return scanner.CommandProfile{Tool: "bun"}
		}
		switch args[0] {
		case "test":
			return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "bun"}
		case "build":
			return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: "bun"}
		default:
			return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: "bun"}
		}
	case "install", "add", "remove", "update", "link", "unlink", "pm", "upgrade", "init", "create", "repl", "completions", "help", "--help", "--version":
		return scanner.CommandProfile{Tool: "bun"}
	default:
		if strings.HasPrefix(args[0], "-") {
			return scanner.CommandProfile{Tool: "bun"}
		}
		// Bun executes a source file when it is invoked as `bun <file>`.
		return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: "bun"}
	}
}

// skipBunOptions removes Bun executor flags before a script or package name.
// Options that take a separate value consume it as well; --name=value is
// already a single argument and needs no special handling.
func skipBunOptions(args []string) []string {
	valueOptions := map[string]bool{
		"--cwd": true, "--env-file": true, "--filter": true, "--loader": true,
		"--preload": true, "--define": true, "--conditions": true,
		"--tsconfig-override": true, "--package": true, "-r": true,
	}
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		option := args[0]
		args = args[1:]
		if valueOptions[option] && len(args) > 0 {
			args = args[1:]
		}
	}
	return args
}

func classifyTurboCommand(args []string) scanner.CommandProfile {
	if len(args) == 0 {
		return scanner.CommandProfile{Tool: "turbo"}
	}
	task := args[0]
	if task == "run" {
		if len(args) < 2 {
			return scanner.CommandProfile{Tool: "turbo"}
		}
		task = args[1]
	}
	switch task {
	case "test":
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "turbo"}
	case "build":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: "turbo"}
	case "lint", "check", "typecheck":
		return scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: "turbo"}
	default:
		return scanner.CommandProfile{Tool: "turbo"}
	}
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
	runningScript := args[0] == "run"
	if runningScript {
		args = args[1:]
	}
	if len(args) == 0 {
		return scanner.CommandProfile{Tool: tool}
	}
	switch args[0] {
	case "build":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: tool}
	case "test":
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
	case "t", "tst":
		if tool == "npm" && !runningScript {
			return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
		}
		return scanner.CommandProfile{Tool: tool}
	default:
		return scanner.CommandProfile{Tool: tool}
	}
}

var nodeValueOptions = map[string]bool{
	"-C": true, "-r": true, "-e": true, "-p": true,
	"--conditions": true, "--cpu-prof-dir": true, "--cpu-prof-interval": true, "--cpu-prof-name": true,
	"--diagnostic-dir": true, "--disable-proto": true, "--disable-warning": true,
	"--dns-result-order": true, "--env-file": true, "--env-file-if-exists": true,
	"--experimental-config-file": true, "--experimental-loader": true, "--experimental-sea-config": true,
	"--heap-prof-dir": true, "--heap-prof-interval": true, "--heap-prof-name": true,
	"--heapsnapshot-near-heap-limit": true, "--heapsnapshot-signal": true, "--icu-data-dir": true,
	"--import": true, "--input-type": true, "--loader": true, "--localstorage-file": true,
	"--max-http-header-size": true, "--network-family-autoselection-attempt-timeout": true,
	"--openssl-config": true, "--redirect-warnings": true, "--report-filename": true,
	"--report-signal": true, "--require": true, "--run": true, "--secure-heap": true,
	"--secure-heap-min": true, "--snapshot-blob": true, "--test-concurrency": true,
	"--test-coverage-branches": true, "--test-coverage-exclude": true, "--test-coverage-functions": true,
	"--test-coverage-include": true, "--test-coverage-lines": true, "--test-global-setup": true,
	"--test-name-pattern": true, "--test-reporter": true, "--test-reporter-destination": true,
	"--test-rerun-failures": true, "--test-shard": true, "--test-skip-pattern": true,
	"--test-timeout": true, "--title": true, "--tls-cipher-list": true, "--tls-keylog": true,
	"--trace-event-categories": true, "--trace-event-file-pattern": true, "--trace-require-module": true,
	"--unhandled-rejections": true, "--use-largepages": true, "--v8-pool-size": true,
	"--watch-kill-signal": true, "--watch-path": true,
}

var nodeOptionalValueOptions = map[string]bool{
	"--inspect": true, "--inspect-brk": true, "--inspect-port": true, "--inspect-wait": true,
}

// nodeTestMode reads only Node's option section. The first script, stdin,
// eval/print source, or explicit -- boundary ends option parsing, so an
// application argument named --test cannot be mistaken for the test runner.
func nodeTestMode(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if arg == "--test" {
			return true
		}
		if arg == "-e" || arg == "-p" || arg == "--eval" || arg == "--print" ||
			strings.HasPrefix(arg, "--eval=") || strings.HasPrefix(arg, "--print=") {
			return false
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
		if strings.Contains(arg, "=") {
			continue
		}
		if nodeValueOptions[arg] {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if nodeOptionalValueOptions[arg] && i+1 < len(args) && isNodeInspectorTarget(args[i+1]) {
			i++
		}
	}
	return false
}

func isNodeInspectorTarget(value string) bool {
	if _, err := strconv.ParseUint(value, 10, 16); err == nil {
		return true
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	_, err = strconv.ParseUint(port, 10, 16)
	return err == nil
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
