package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

var _ scanner.Adapter = Adapter{}

func TestEcosystem(t *testing.T) {
	if got := (Adapter{}).Ecosystem(); got != "npm" {
		t.Errorf("Ecosystem() = %q, want npm", got)
	}
}

func TestDetect(t *testing.T) {
	a := Adapter{}
	if !a.Detect("testdata/npmproj") {
		t.Error("Detect should be true for a dir with package.json")
	}
	if a.Detect(t.TempDir()) {
		t.Error("Detect should be false for an empty dir")
	}
}

func TestClassifyCommand(t *testing.T) {
	a := Adapter{}
	tests := []struct {
		argv  []string
		stage domain.Stage
		known bool
	}{
		{[]string{"tsc", "--noEmit"}, domain.StageProjectTypecheck, true},
		{[]string{"npx", "tsc"}, domain.StageProjectTypecheck, true},
		{[]string{"eslint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"npx", "eslint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"pnpm", "dlx", "eslint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"pnpm", "exec", "eslint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"yarn", "dlx", "eslint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"yarn", "exec", "eslint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"biome", "check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"biome", "lint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"biome", "ci", "."}, domain.StageProjectTypecheck, true},
		{[]string{"biome", "format", "."}, "", false},
		{[]string{"bun", "test"}, domain.StageProjectTest, true},
		{[]string{"bun", "run", "test"}, domain.StageProjectTest, true},
		{[]string{"bun", "run", "--silent", "test"}, domain.StageProjectTest, true},
		{[]string{"bun", "run", "--bun", "build"}, domain.StageProjectCompile, true},
		{[]string{"bun", "build", "src/index.ts"}, domain.StageProjectCompile, true},
		{[]string{"bun", "run", "build"}, domain.StageProjectCompile, true},
		{[]string{"bun", "x", "eslint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"bun", "x", "--bun", "eslint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"bunx", "eslint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"bun", "run", "dev"}, domain.StageProjectProcess, true},
		{[]string{"bun", "src/index.ts"}, domain.StageProjectProcess, true},
		{[]string{"turbo", "test"}, domain.StageProjectTest, true},
		{[]string{"turbo", "run", "test"}, domain.StageProjectTest, true},
		{[]string{"turbo", "build"}, domain.StageProjectCompile, true},
		{[]string{"turbo", "run", "build"}, domain.StageProjectCompile, true},
		{[]string{"turbo", "lint"}, domain.StageProjectTypecheck, true},
		{[]string{"turbo", "check"}, domain.StageProjectTypecheck, true},
		{[]string{"turbo", "run", "lint"}, domain.StageProjectTypecheck, true},
		{[]string{"turbo", "run", "check"}, domain.StageProjectTypecheck, true},
		{[]string{"turbo", "run", "typecheck"}, domain.StageProjectTypecheck, true},
		{[]string{"prettier", "--check", "."}, domain.StageProjectTypecheck, true},
		{[]string{"prettier", "-c", "."}, domain.StageProjectTypecheck, true},
		{[]string{"prettier", "--list-different", "."}, domain.StageProjectTypecheck, true},
		{[]string{"oxlint", "."}, domain.StageProjectTypecheck, true},
		{[]string{"playwright", "test"}, domain.StageProjectTest, true},
		{[]string{"npx", "playwright", "test"}, domain.StageProjectTest, true},
		{[]string{"pnpm", "exec", "playwright", "test"}, domain.StageProjectTest, true},
		{[]string{"playwright", "codegen"}, "", false},
		{[]string{"ava"}, domain.StageProjectTest, true},
		{[]string{"cypress", "run"}, domain.StageProjectTest, true},
		{[]string{"cypress", "open"}, "", false},
		{[]string{"swc", "src", "-d", "dist"}, domain.StageProjectCompile, true},
		{[]string{"npm", "run", "build"}, domain.StageProjectCompile, true},
		{[]string{"pnpm", "--filter", "web", "test"}, domain.StageProjectTest, true},
		{[]string{"pnpm", "-F", "web", "test"}, domain.StageProjectTest, true},
		{[]string{"pnpm", "-C", "packages/web", "test"}, domain.StageProjectTest, true},
		{[]string{"pnpm", "--filter", "@my/app", "run", "build"}, domain.StageProjectCompile, true},
		{[]string{"npm", "--prefix", "frontend", "test"}, domain.StageProjectTest, true},
		{[]string{"npm", "-w", "app", "test"}, domain.StageProjectTest, true},
		{[]string{"npm", "--workspace", "@my/app", "run", "build"}, domain.StageProjectCompile, true},
		{[]string{"yarn", "--cwd", "packages/app", "test"}, domain.StageProjectTest, true},
		{[]string{"yarn", "workspace", "@my/app", "test"}, domain.StageProjectTest, true},
		{[]string{"yarn", "workspace", "@my/app", "run", "build"}, domain.StageProjectCompile, true},
		{[]string{"npm", "t"}, domain.StageProjectTest, true},
		{[]string{"npm", "tst"}, domain.StageProjectTest, true},
		{[]string{"npm", "run", "t"}, "", false},
		{[]string{"npm", "run", "tst"}, "", false},
		{[]string{"pnpm", "run", "t"}, "", false},
		{[]string{"pnpm", "t"}, "", false},
		{[]string{"yarn", "run", "tst"}, "", false},
		{[]string{"yarn", "tst"}, "", false},
		{[]string{"node", "--test"}, domain.StageProjectTest, true},
		{[]string{"node", "--require", "./setup.js", "--test", "app.test.js"}, domain.StageProjectTest, true},
		{[]string{"node", "--test-reporter", "spec", "--test", "app.test.js"}, domain.StageProjectTest, true},
		{[]string{"node", "--inspect", "9229", "--test", "app.test.js"}, domain.StageProjectTest, true},
		{[]string{"node", "app.js", "--test"}, domain.StageProjectProcess, true},
		{[]string{"node", "--", "app.js", "--test"}, domain.StageProjectProcess, true},
		{[]string{"node", "--eval", "--test"}, domain.StageProjectProcess, true},
		{[]string{"node", "--eval=console.log(1)", "--test"}, domain.StageProjectProcess, true},
		{[]string{"node", "--title", "--test", "app.js"}, domain.StageProjectProcess, true},
		{[]string{"pnpm", "build"}, domain.StageProjectCompile, true},
		{[]string{"yarn", "run", "build"}, domain.StageProjectCompile, true},
		{[]string{"vite", "build"}, domain.StageProjectCompile, true},
		{[]string{"next", "build"}, domain.StageProjectCompile, true},
		{[]string{"tsup"}, domain.StageProjectCompile, true},
		{[]string{"webpack", "--mode", "production"}, domain.StageProjectCompile, true},
		{[]string{"esbuild", "src/index.ts", "--bundle"}, domain.StageProjectCompile, true},
		{[]string{"rollup", "-c"}, domain.StageProjectCompile, true},
		{[]string{"npm", "test"}, domain.StageProjectTest, true},
		{[]string{"pnpm", "test"}, domain.StageProjectTest, true},
		{[]string{"yarn", "test"}, domain.StageProjectTest, true},
		{[]string{"npm", "run", "test"}, domain.StageProjectTest, true},
		{[]string{"pnpm", "dlx", "jest"}, domain.StageProjectTest, true},
		{[]string{"pnpm", "exec", "mocha"}, domain.StageProjectTest, true},
		{[]string{"yarn", "dlx", "vitest", "run"}, domain.StageProjectTest, true},
		{[]string{"yarn", "exec", "tsc", "--noEmit"}, domain.StageProjectTypecheck, true},
		{[]string{"npm", "exec", "vitest", "run"}, domain.StageProjectTest, true},
		{[]string{"npm", "exec", "--", "tsc", "--noEmit"}, domain.StageProjectTypecheck, true},
		{[]string{"npm", "exec", "--package", "typescript", "--", "tsc", "--noEmit"}, domain.StageProjectTypecheck, true},
		{[]string{"pnpm", "dlx", "--package", "esbuild", "esbuild", "src/index.ts"}, domain.StageProjectCompile, true},
		{[]string{"npm", "exec", "--workspace", "jest", "--", "tsc", "--noEmit"}, domain.StageProjectTypecheck, true},
		{[]string{"npm", "exec", "-w", "jest", "-w", "mocha", "tsc"}, domain.StageProjectTypecheck, true},
		{[]string{"npm", "exec", "--workspace=jest", "--", "tsc"}, domain.StageProjectTypecheck, true},
		{[]string{"npm", "exec", "--workspaces", "jest"}, domain.StageProjectTest, true},
		{[]string{"npm", "exec", "-C", "jest", "tsc"}, domain.StageProjectTypecheck, true},
		{[]string{"npm", "exec", "-p", "jest"}, domain.StageProjectTest, true},
		{[]string{"npm", "exec", "-c", "jest"}, "", false},
		{[]string{"pnpm", "exec", "--filter", "jest", "tsc", "--noEmit"}, domain.StageProjectTypecheck, true},
		{[]string{"pnpm", "exec", "-F", "jest", "-C", "packages/jest", "tsc"}, domain.StageProjectTypecheck, true},
		{[]string{"pnpm", "exec", "-w", "jest"}, domain.StageProjectTest, true},
		{[]string{"yarn", "dlx", "-p", "jest", "tsc"}, domain.StageProjectTypecheck, true},
		{[]string{"pnpm", "run", "exec", "jest"}, "", false},
		{[]string{"jest"}, domain.StageProjectTest, true},
		{[]string{"vitest", "run"}, domain.StageProjectTest, true},
		{[]string{"mocha"}, domain.StageProjectTest, true},
		{[]string{"node", "server.js"}, domain.StageProjectProcess, true},
		{[]string{"node.exe", "server.js"}, domain.StageProjectProcess, true},
		{[]string{`C:\Program Files\nodejs\node.exe`, "x.js"}, domain.StageProjectProcess, true},
		{[]string{"cargo", "build"}, "", false},
		{[]string{"npm", "install"}, "", false},
		{[]string{"vite"}, "", false},
		{[]string{}, "", false},
	}
	for _, tt := range tests {
		got := a.ClassifyCommand(tt.argv)
		if got.Known != tt.known {
			t.Errorf("ClassifyCommand(%v).Known = %v, want %v", tt.argv, got.Known, tt.known)
			continue
		}
		if tt.known && got.Stage != tt.stage {
			t.Errorf("ClassifyCommand(%v).Stage = %q, want %q", tt.argv, got.Stage, tt.stage)
		}
	}
}

func TestClassifyCommandPreservesUnclassifiedToolNames(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		tool string
	}{
		{[]string{"biome", "format", "."}, "biome"},
		{[]string{"playwright", "codegen"}, "playwright"},
		{[]string{"cypress", "open"}, "cypress"},
		{[]string{"prettier", "--write", "."}, "prettier"},
		{[]string{"bun", "add", "axios"}, "bun"},
		{[]string{"turbo", "dev"}, "turbo"},
	} {
		got := (Adapter{}).ClassifyCommand(tc.argv)
		if got.Known || got.Tool != tc.tool {
			t.Errorf("ClassifyCommand(%v) = %+v, want unknown tool %q", tc.argv, got, tc.tool)
		}
	}
}

func TestClassifyTurboCommand(t *testing.T) {
	a := Adapter{}
	tests := []struct {
		name  string
		argv  []string
		stage domain.Stage
		known bool
	}{
		// Leading/trailing options and optional enum values.
		{"filter-eq-before", []string{"turbo", "--filter=web", "test"}, domain.StageProjectTest, true},
		{"short-filter-before-run", []string{"turbo", "-F", "web", "run", "build"}, domain.StageProjectCompile, true},
		{"cwd-before-task", []string{"turbo", "--cwd", "apps/web", "test"}, domain.StageProjectTest, true},
		{"only-flag", []string{"turbo", "--only", "test"}, domain.StageProjectTest, true},
		{"affected-flag", []string{"turbo", "--affected", "run", "test"}, domain.StageProjectTest, true},
		{"cache-inline", []string{"turbo", "--cache=local:r,remote:rw", "build"}, domain.StageProjectCompile, true},
		{"cache-separate", []string{"turbo", "--cache", "local:r,remote:rw", "run", "build"}, domain.StageProjectCompile, true},
		{"continue-bare-before-task", []string{"turbo", "--continue", "test"}, domain.StageProjectTest, true},
		{"continue-enum-separate", []string{"turbo", "--continue", "always", "test"}, domain.StageProjectTest, true},
		{"continue-enum-inline", []string{"turbo", "--continue=dependencies-successful", "run", "build"}, domain.StageProjectCompile, true},
		{"summarize-bare-before-task", []string{"turbo", "--summarize", "test"}, domain.StageProjectTest, true},
		{"summarize-enum-separate", []string{"turbo", "--summarize", "true", "lint"}, domain.StageProjectTypecheck, true},
		{"summarize-enum-inline", []string{"turbo", "--summarize=false", "check"}, domain.StageProjectTypecheck, true},
		{"trailing-options", []string{"turbo", "run", "build", "-F", "web", "--only", "--affected", "--continue"}, domain.StageProjectCompile, true},
		{"trailing-filter-named-test", []string{"turbo", "run", "build", "-F", "test"}, domain.StageProjectCompile, true},
		{"verbosity-and-bool-flags", []string{"turbo", "-vv", "--force", "--parallel", "--single-package", "--no-cache", "--no-daemon", "--remote-only", "--no-color", "--preflight", "--skip-infer", "--anon-profile", "--dangerously-disable-package-manager-check", "test"}, domain.StageProjectTest, true},
		{"value-options-variety", []string{"turbo", "--concurrency", "4", "--env-mode=strict", "--log-order", "grouped", "--output-logs=errors-only", "--ui", "stream", "--verbosity=2", "lint"}, domain.StageProjectTypecheck, true},
		{"remaining-known-value-options", []string{"turbo", "--cache-dir", ".turbo", "--cache-workers", "4", "--framework-inference", "false", "--global-deps", "*.md", "--heap", "4G", "--log-prefix", "task", "--pkg-inference-root", "apps", "--profile", "p.json", "--remote-cache-timeout", "30", "--root-turbo-json", "turbo.json", "--scope", "web", "--team", "acme", "--token", "tok", "--trace", "t.json", "--api", "https://api.example", "--login", "https://login.example", "-v", "--daemon", "--color", "-vvv", "build"}, domain.StageProjectCompile, true},

		// Package and root task qualifiers.
		{"package-build", []string{"turbo", "web#build"}, domain.StageProjectCompile, true},
		{"scoped-package-test", []string{"turbo", "run", "@repo/ui#test"}, domain.StageProjectTest, true},
		{"root-typecheck", []string{"turbo", "//#typecheck"}, domain.StageProjectTypecheck, true},
		{"root-lint", []string{"turbo", "//#lint"}, domain.StageProjectTypecheck, true},
		{"malformed-empty-prefix", []string{"turbo", "#build"}, "", false},
		{"malformed-empty-task", []string{"turbo", "web#"}, "", false},
		{"malformed-extra-hash", []string{"turbo", "web#a#test"}, "", false},

		// Multi-task convergence and mixed/unclassified rejection.
		{"multi-same-stage-typecheck", []string{"turbo", "lint", "typecheck"}, domain.StageProjectTypecheck, true},
		{"multi-qualified-same-stage", []string{"turbo", "web#lint", "//#typecheck", "check"}, domain.StageProjectTypecheck, true},
		{"mixed-stages", []string{"turbo", "build", "test"}, "", false},
		{"mixed-qualified-stages", []string{"turbo", "web#build", "@repo/ui#test"}, "", false},
		{"mixed-unclassified-task", []string{"turbo", "test", "dev"}, "", false},
		{"mixed-malformed-task", []string{"turbo", "web#test", "#build"}, "", false},

		// Option value collision, missing values, and ambiguous hyphen values.
		{"filter-value-collision", []string{"turbo", "-F", "test", "dev"}, "", false},
		{"cwd-value-collision", []string{"turbo", "--cwd", "build", "dev"}, "", false},
		{"filter-consumes-only-word", []string{"turbo", "--filter", "test"}, "", false},
		{"missing-value-at-end", []string{"turbo", "--filter"}, "", false},
		{"empty-inline-value", []string{"turbo", "--filter=", "test"}, "", false},
		{"hyphen-value-separate", []string{"turbo", "--filter", "-test", "build"}, "", false},
		{"hyphen-value-inline", []string{"turbo", "--filter=-test", "build"}, "", false},
		{"invalid-continue-inline", []string{"turbo", "--continue=bogus", "test"}, "", false},
		{"invalid-summarize-inline", []string{"turbo", "--summarize=bogus", "test"}, "", false},

		// Explicit "--" pass-through boundary.
		{"dev-before-boundary-test-after", []string{"turbo", "run", "dev", "--", "test"}, "", false},
		{"test-before-boundary-file-after", []string{"turbo", "run", "test", "--", "file.test.ts"}, domain.StageProjectTest, true},
		{"test-with-script-dry-run-after-boundary", []string{"turbo", "test", "--", "--dry-run"}, domain.StageProjectTest, true},
		{"boundary-before-task", []string{"turbo", "--", "test"}, "", false},
		{"run-boundary-before-task", []string{"turbo", "run", "--", "test"}, "", false},
		{"missing-value-before-boundary", []string{"turbo", "-F", "--", "test"}, "", false},

		// Non-executing flags.
		{"dry-flag", []string{"turbo", "run", "build", "--dry"}, "", false},
		{"dry-run-flag", []string{"turbo", "test", "--dry-run"}, "", false},
		{"dry-json-flag", []string{"turbo", "run", "test", "--dry=json"}, "", false},
		{"dry-run-json-flag", []string{"turbo", "run", "test", "--dry-run=json"}, "", false},
		{"graph-flag", []string{"turbo", "run", "build", "--graph"}, "", false},
		{"graph-file-flag", []string{"turbo", "run", "build", "--graph=out.svg"}, "", false},
		{"help-long", []string{"turbo", "run", "build", "--help"}, "", false},
		{"help-short", []string{"turbo", "build", "-h"}, "", false},
		{"version-short", []string{"turbo", "-V"}, "", false},
		{"version-long", []string{"turbo", "run", "test", "--version"}, "", false},

		// Non-execution subcommands.
		{"subcmd-prune", []string{"turbo", "prune", "test"}, "", false},
		{"subcmd-gen", []string{"turbo", "gen", "build"}, "", false},
		{"subcmd-generate", []string{"turbo", "generate", "test"}, "", false},
		{"subcmd-watch", []string{"turbo", "watch", "test"}, "", false},
		{"subcmd-help", []string{"turbo", "help", "test"}, "", false},
		{"subcmd-login", []string{"turbo", "login"}, "", false},
		{"subcmd-logout", []string{"turbo", "logout"}, "", false},
		{"subcmd-link", []string{"turbo", "link"}, "", false},
		{"subcmd-unlink", []string{"turbo", "unlink"}, "", false},
		{"subcmd-daemon", []string{"turbo", "daemon"}, "", false},
		{"subcmd-telemetry", []string{"turbo", "telemetry"}, "", false},
		{"subcmd-bin", []string{"turbo", "bin"}, "", false},
		{"subcmd-completion", []string{"turbo", "completion"}, "", false},
		{"subcmd-config", []string{"turbo", "config"}, "", false},
		{"subcmd-info", []string{"turbo", "info"}, "", false},
		{"subcmd-ls", []string{"turbo", "ls"}, "", false},
		{"subcmd-scan", []string{"turbo", "scan"}, "", false},
		{"subcmd-boundaries", []string{"turbo", "boundaries"}, "", false},
		{"subcmd-devtools", []string{"turbo", "devtools"}, "", false},
		{"subcmd-prune-after-cwd", []string{"turbo", "--cwd", "apps/web", "prune", "test"}, "", false},
		{"run-no-task", []string{"turbo", "run"}, "", false},
		{"turbo-bare", []string{"turbo"}, "", false},

		// Unsupported options.
		{"unknown-option-before", []string{"turbo", "--unknown-flag", "test"}, "", false},
		{"unknown-option-inline", []string{"turbo", "run", "--unknown=1", "test"}, "", false},
		{"unknown-option-after", []string{"turbo", "test", "--unknown"}, "", false},
		{"unknown-short-option", []string{"turbo", "-X", "test"}, "", false},

		// Delegation paths.
		{"npx-turbo-filter-test", []string{"npx", "turbo", "--filter=web", "test"}, domain.StageProjectTest, true},
		{"npx-turbo-dry-run", []string{"npx", "turbo", "run", "build", "--dry-run"}, "", false},
		{"pnpm-exec-turbo-build", []string{"pnpm", "exec", "turbo", "-F", "web", "run", "build"}, domain.StageProjectCompile, true},
		{"pnpm-exec-turbo-prune", []string{"pnpm", "exec", "turbo", "prune", "test"}, "", false},
		{"yarn-exec-turbo-typecheck", []string{"yarn", "exec", "turbo", "//#typecheck"}, domain.StageProjectTypecheck, true},
		{"bunx-turbo-multi-typecheck", []string{"bunx", "turbo", "--cwd", "apps/web", "lint", "typecheck"}, domain.StageProjectTypecheck, true},
		{"bun-x-turbo-test-boundary", []string{"bun", "x", "turbo", "run", "test", "--", "file.test.ts"}, domain.StageProjectTest, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.ClassifyCommand(tt.argv)
			if got.Known != tt.known || got.Tool != "turbo" {
				t.Fatalf("ClassifyCommand(%v) = %+v, want known=%v tool=turbo", tt.argv, got, tt.known)
			}
			if tt.known && got.Stage != tt.stage {
				t.Errorf("ClassifyCommand(%v).Stage = %q, want %q", tt.argv, got.Stage, tt.stage)
			}
		})
	}
}

func TestEnvironmentHints(t *testing.T) {
	a := Adapter{}
	ctx := context.Background()

	h := a.EnvironmentHints(ctx, "testdata/npmproj")
	want := map[string]string{
		"ecosystem":      "npm",
		"runtime":        "node",
		"language":       "typescript",
		"moduleSystem":   "cjs",
		"packageManager": "npm",
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("npmproj hint %q = %q, want %q", k, h[k], v)
		}
	}
	if h["runtimeVersion"] != "" {
		t.Errorf("runtimeVersion must stay empty (probed by caller), got %q", h["runtimeVersion"])
	}

	h = a.EnvironmentHints(ctx, "testdata/pnpmproj")
	want = map[string]string{
		"ecosystem":             "npm",
		"runtime":               "node",
		"language":              "javascript",
		"moduleSystem":          "esm",
		"packageManager":        "pnpm",
		"packageManagerVersion": "9.7.0",
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("pnpmproj hint %q = %q, want %q", k, h[k], v)
		}
	}

	h = a.EnvironmentHints(ctx, "testdata/yarnproj")
	if h["packageManager"] != "yarn" {
		t.Errorf("yarnproj packageManager = %q, want yarn", h["packageManager"])
	}
	if h["moduleSystem"] != "cjs" {
		t.Errorf("yarnproj moduleSystem = %q, want cjs", h["moduleSystem"])
	}
	if h["language"] != "javascript" {
		t.Errorf("yarnproj language = %q, want javascript", h["language"])
	}

	for _, lockfile := range []string{"bun.lock", "bun.lockb"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, lockfile), []byte("lockfile"), 0o600); err != nil {
			t.Fatalf("write %s: %v", lockfile, err)
		}
		h := a.EnvironmentHints(ctx, dir)
		if h["packageManager"] != "bun" {
			t.Errorf("%s packageManager = %q, want bun", lockfile, h["packageManager"])
		}
	}
}
