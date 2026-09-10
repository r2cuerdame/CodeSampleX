package lightsail

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func identityGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repo}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func initIdentityRepo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	identityGit(t, path, "init", "-q")
	identityGit(t, path, "config", "user.email", "test@example.invalid")
	identityGit(t, path, "config", "user.name", "Test")
}

func sourceCheck(t *testing.T, shell, script, helper, payload, target, control string) (string, error) {
	t.Helper()
	output, err := exec.Command(shell, "-NoProfile", "-File", script,
		"-Helper", helper,
		"-Payload", payload,
		"-Target", target,
		"-Control", control,
	).CombinedOutput()
	return string(output), err
}

func TestDeploymentSourceUsesGitRootIdentityOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path identity contract")
	}
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		shell, err = exec.LookPath("powershell")
	}
	if err != nil {
		t.Fatal("PowerShell is required on the Windows runner")
	}

	root := t.TempDir()
	control := filepath.Join(root, "control")
	payload := filepath.Join(root, "payload")
	initIdentityRepo(t, control)
	initIdentityRepo(t, payload)

	helper := filepath.Join(control, "deploy", "lightsail", "deployment-source.ps1")
	if err := os.MkdirAll(filepath.Dir(helper), 0o755); err != nil {
		t.Fatal(err)
	}
	helperSource, err := os.ReadFile("deployment-source.ps1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, helperSource, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "payload.txt"), []byte("exact payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(payload, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{control, payload} {
		identityGit(t, repo, "add", ".")
		identityGit(t, repo, "commit", "-qm", "fixture")
	}

	controlSHA := identityGit(t, control, "rev-parse", "HEAD")
	payloadSHA := identityGit(t, payload, "rev-parse", "HEAD")
	script := filepath.Join(root, "check.ps1")
	checkScript := `param($Helper,$Payload,$Target,$Control)
$ErrorActionPreference='Stop'
. $Helper
Resolve-CSXDeploymentSource -SourceRepoPath $Payload -ExpectedRevision $Target -OperationalRevision $Control | ConvertTo-Json -Compress
`
	if err := os.WriteFile(script, []byte(checkScript), 0o644); err != nil {
		t.Fatal(err)
	}

	if output, err := sourceCheck(t, shell, script, helper, payload, payloadSHA, controlSHA); err != nil {
		t.Fatalf("clean repository root was rejected: %v\n%s", err, output)
	}
	if output, err := sourceCheck(t, shell, script, helper, filepath.Join(payload, "subdir"), payloadSHA, controlSHA); err == nil {
		t.Fatalf("repository subdirectory was accepted: %s", output)
	} else if !strings.Contains(output, "payload source must be a repository root") {
		t.Fatalf("subdirectory failed for the wrong reason: %v\n%s", err, output)
	}

	nonRepo := filepath.Join(root, "not-a-repository")
	if err := os.Mkdir(nonRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"non-repository": nonRepo,
		"nonexistent":    filepath.Join(root, "missing"),
	} {
		if output, err := sourceCheck(t, shell, script, helper, path, payloadSHA, controlSHA); err == nil {
			t.Fatalf("%s payload path was accepted: %s", name, output)
		}
	}

	if err := os.WriteFile(filepath.Join(payload, "payload.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := sourceCheck(t, shell, script, helper, payload, payloadSHA, controlSHA); err == nil {
		t.Fatalf("dirty payload was accepted: %s", output)
	} else if !strings.Contains(output, "payload checkout must be clean") {
		t.Fatalf("dirty payload failed for the wrong reason: %v\n%s", err, output)
	}
	identityGit(t, payload, "checkout", "--", "payload.txt")

	wrongSHA := strings.Repeat("0", 40)
	if output, err := sourceCheck(t, shell, script, helper, payload, wrongSHA, controlSHA); err == nil {
		t.Fatalf("wrong payload SHA was accepted: %s", output)
	} else if !strings.Contains(output, "payload checkout does not match the immutable target revision") {
		t.Fatalf("wrong SHA failed for the wrong reason: %v\n%s", err, output)
	}
}
