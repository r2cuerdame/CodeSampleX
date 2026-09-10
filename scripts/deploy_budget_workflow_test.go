package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMigrationBudgetCalculatesActualWorkflowTimeouts(t *testing.T) {
	bash, err := findCompatibleBash()
	if err != nil {
		t.Skipf("compatible bash unavailable: %v", err)
	}
	step := productionWorkflowStep(t, productionWorkflow(t), "Calculate separate migration and deployment budgets")
	start := strings.Index(step, "        run: |\n")
	if start < 0 {
		t.Fatal("budget calculator not found")
	}
	lines := strings.Split(step[start+len("        run: |\n"):], "\n")
	var program strings.Builder
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "          ") {
			program.WriteString(strings.TrimPrefix(line, "          "))
			program.WriteByte('\n')
		} else if strings.TrimSpace(line) != "" {
			break
		}
	}
	for _, tc := range []struct{ input, want string }{
		{"60", "step_minutes=25\njob_minutes=28\n"},
		{"61", "step_minutes=26\njob_minutes=29\n"},
		{"1200", "step_minutes=44\njob_minutes=47\n"},
		{"1800", "step_minutes=54\njob_minutes=57\n"},
		{"0", ""}, {"59", ""}, {"1801", ""}, {"99999999999999999999", ""},
		{"1200;exit 0", ""}, {"", ""}, {"-1", ""}, {"60.5", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.Command(bash, "-c", program.String())
			cmd.Env = append(os.Environ(), "MIGRATION_TIMEOUT="+tc.input, "GITHUB_OUTPUT="+filepath.ToSlash(output))
			log, err := cmd.CombinedOutput()
			if (err == nil) != (tc.want != "") {
				t.Fatalf("budget acceptance: %v: %s", err, log)
			}
			got, _ := os.ReadFile(output)
			if string(got) != tc.want {
				t.Fatalf("outputs %q, want %q", got, tc.want)
			}
		})
	}
}

func findCompatibleBash() (string, error) {
	if runtime.GOOS != "windows" {
		return exec.LookPath("bash")
	}

	if p, err := exec.LookPath("bash"); err == nil && !isIncompatibleWindowsBash(p) {
		return p, nil
	}

	if gitPath, err := exec.LookPath("git"); err == nil {
		gitDir := filepath.Dir(gitPath)
		candidates := []string{
			filepath.Join(gitDir, "../bin/bash.exe"),
			filepath.Join(gitDir, "../usr/bin/bash.exe"),
			filepath.Join(gitDir, "bash.exe"),
			filepath.Join(gitDir, "../../bin/bash.exe"),
			filepath.Join(gitDir, "../../usr/bin/bash.exe"),
		}
		for _, candidate := range candidates {
			candidate = filepath.Clean(candidate)
			if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && !isIncompatibleWindowsBash(candidate) {
				return candidate, nil
			}
		}
	}

	var roots []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "LocalAppData"} {
		if val := os.Getenv(env); val != "" {
			roots = append(roots, val)
		}
	}
	subpaths := []string{
		filepath.Join("Git", "bin", "bash.exe"),
		filepath.Join("Git", "usr", "bin", "bash.exe"),
		filepath.Join("Programs", "Git", "bin", "bash.exe"),
		filepath.Join("Programs", "Git", "usr", "bin", "bash.exe"),
	}
	for _, root := range roots {
		for _, sub := range subpaths {
			candidate := filepath.Clean(filepath.Join(root, sub))
			if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && !isIncompatibleWindowsBash(candidate) {
				return candidate, nil
			}
		}
	}

	fallbacks := []string{
		`C:\msys64\usr\bin\bash.exe`,
		`C:\Git\bin\bash.exe`,
		`C:\cygwin64\bin\bash.exe`,
	}
	for _, candidate := range fallbacks {
		candidate = filepath.Clean(candidate)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && !isIncompatibleWindowsBash(candidate) {
			return candidate, nil
		}
	}

	return "", errors.New("compatible bash unavailable")
}

func isIncompatibleWindowsBash(path string) bool {
	if path == "" {
		return true
	}
	lower := strings.ToLower(filepath.Clean(path))
	if strings.Contains(lower, `\microsoft\windowsapps\`) || strings.Contains(lower, "/microsoft/windowsapps/") {
		return true
	}
	roots := []string{`c:\windows`}
	if sr := os.Getenv("SystemRoot"); sr != "" {
		roots = append(roots, strings.ToLower(filepath.Clean(sr)))
	}
	if windir := os.Getenv("WINDIR"); windir != "" {
		roots = append(roots, strings.ToLower(filepath.Clean(windir)))
	}
	for _, r := range roots {
		rClean := filepath.Clean(r)
		if strings.HasPrefix(lower, rClean+`\`) || strings.HasPrefix(lower, rClean+`/`) || lower == rClean {
			return true
		}
	}
	return false
}
