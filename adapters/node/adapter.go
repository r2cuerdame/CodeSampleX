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
		return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: tool}
	case "tsup", "webpack":
		return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: tool}
	case "jest", "vitest", "mocha":
		return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
	case "vite", "next":
		if len(rest) > 0 && rest[0] == "build" {
			return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: tool}
		}
		return scanner.CommandProfile{Tool: tool}
	case "npm", "pnpm", "yarn":
		args := rest
		if len(args) > 0 && args[0] == "run" {
			args = args[1:]
		}
		if len(args) > 0 {
			switch args[0] {
			case "build":
				return scanner.CommandProfile{Stage: domain.StageProjectCompile, Known: true, Tool: tool}
			case "test":
				return scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tool}
			}
		}
		return scanner.CommandProfile{Tool: tool}
	}
	return scanner.CommandProfile{}
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
