//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsGoTestRunnerRestoresProcessState(t *testing.T) {
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s not installed", shell)
			}
			cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-File", "windows-go-test-state-test.ps1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("process state regression: %v\n%s", err, out)
			} else {
				t.Logf("%s", out)
			}
		})
	}
}

func TestWindowsGoTestRunner(t *testing.T) {
	cache, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-File", "scripts/go-test.ps1", "-count=1", "-v", "-run", "^TestWindowsGoTestArtifactProbe$", "./scripts")
	cmd.Dir = ".."
	// Simulate the old core workspace and ordinary Go/T.TempDir defaults.
	// The runner must override inherited paths, without writing there first.
	legacy := filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp")
	cmd.Env = append(os.Environ(), "CSX_GO_TEST_PROBE=1", "CSX_EXPECT_GOCACHE="+strings.TrimSpace(string(cache)),
		"GOTMPDIR="+filepath.Join(legacy, "loopoffice-go", "csx-path-probe"),
		"TEMP="+legacy, "TMP="+legacy)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Windows Go test runner: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestWindowsGoTestArtifactProbe") {
		t.Fatalf("artifact probe did not execute:\n%s", out)
	}
	t.Logf("%s", out)
}

func TestWindowsGoTestArtifactProbe(t *testing.T) {
	if os.Getenv("CSX_GO_TEST_PROBE") != "1" {
		t.Skip("executed by TestWindowsGoTestRunner in a fresh Go process")
	}
	root := filepath.Join(os.Getenv("LOCALAPPDATA"), "csx", "gotmp")
	underRoot := func(label, path string) {
		t.Helper()
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			t.Fatalf("%s escaped gotmp: %q (root %q, rel %q, err %v)", label, path, root, rel, err)
		}
		t.Logf("%s: %s", label, path)
	}
	for _, key := range []string{"GOTMPDIR", "TEMP", "TMP"} {
		underRoot(key, os.Getenv(key))
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	underRoot("test executable", exe)
	underRoot("os.TempDir", os.TempDir())
	dir := t.TempDir()
	underRoot("t.TempDir", dir)
	f, err := os.CreateTemp("", "csx-artifact-probe-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	underRoot("os.CreateTemp", f.Name())
	payload := filepath.Join(dir, "payloads", "v1.0.0", "csx-payload.exe")
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("csx artifact location probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	underRoot("fixture payload", payload)
	cache, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil || strings.TrimSpace(string(cache)) != os.Getenv("CSX_EXPECT_GOCACHE") {
		t.Fatalf("GOCACHE changed: %q, err %v", cache, err)
	}
}
