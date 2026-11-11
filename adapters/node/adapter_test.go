package node

import (
	"context"
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
}
