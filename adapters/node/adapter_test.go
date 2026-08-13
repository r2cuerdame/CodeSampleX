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
		{[]string{"pnpm", "build"}, domain.StageProjectCompile, true},
		{[]string{"yarn", "run", "build"}, domain.StageProjectCompile, true},
		{[]string{"vite", "build"}, domain.StageProjectCompile, true},
		{[]string{"next", "build"}, domain.StageProjectCompile, true},
		{[]string{"tsup"}, domain.StageProjectCompile, true},
		{[]string{"webpack", "--mode", "production"}, domain.StageProjectCompile, true},
		{[]string{"npm", "test"}, domain.StageProjectTest, true},
		{[]string{"pnpm", "test"}, domain.StageProjectTest, true},
		{[]string{"yarn", "test"}, domain.StageProjectTest, true},
		{[]string{"npm", "run", "test"}, domain.StageProjectTest, true},
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
