package lightsail

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// remoteRunner extracts the one-line shell program deploy.ps1 hands ssh, so a
// test can run it instead of reading it.
func remoteRunner(t *testing.T) string {
	t.Helper()
	script := readDeployFixture(t, "deploy.ps1")
	m := regexp.MustCompile(`(?m)^\s*\$remoteRunner = '(.*)'\s*$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("deploy.ps1 has no $remoteRunner assignment to test")
	}
	// PowerShell single-quoted strings escape a quote by doubling it.
	return strings.ReplaceAll(strings.ReplaceAll(m[1], "''", "'"), "__CSX_COMMAND_SECONDS__", "2")
}

// The runner stages the remote program in a temp file and executes it. Every
// step of that staging must stop the run when it fails, because a deploy that
// staged nothing, promoted nothing and took no lock would otherwise be
// recorded as a successful rollout.
//
// The first version did exactly that. With mktemp broken, $f is empty, the
// redirection fails, and `sh` with no argument reads an already-drained stdin
// and exits 0 — demonstrated against the production host with TMPDIR pointed
// at a missing directory: both steps printed errors and the whole thing still
// returned 0.
func remoteRunnerExecutionFixture(t *testing.T) (string, string) {
	t.Helper()
	runner := remoteRunner(t)
	lock := filepath.ToSlash(filepath.Join(t.TempDir(), "command.lock"))
	runner = strings.ReplaceAll(runner, "$HOME/.csx-deploy-command.lock", "'"+lock+"'")
	testPath := os.Getenv("PATH")
	if _, err := exec.LookPath("flock"); err != nil {
		if runtime.GOOS == "linux" {
			t.Fatal("Linux deployment tests require real flock; refusing a serialization substitute")
		}
		// Git Bash has no flock. This fixture tests staging/exit propagation;
		// Linux CI uses real flock. The cancellation contract is pinned below.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "flock"), []byte("#!/bin/sh\nshift 3\nexec \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		testPath = dir + string(os.PathListSeparator) + testPath
		t.Log("flock unavailable: staging/exit fixture uses pass-through; Linux CI exercises real command lock")
	}

	return runner, testPath
}

func TestTheRemoteRunnerIsFailClosed(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX sh on this machine to run the runner against")
	}
	runner, testPath := remoteRunnerExecutionFixture(t)

	for _, tc := range []struct {
		name    string
		env     []string
		program string
		want    int
	}{
		{"a temp file that cannot be made stops the run", []string{"TMPDIR=/nonexistent-csx-dir"},
			"CSX-SCRIPT-V1\necho SHOULD NOT RUN\n", 91},
		{"a program that is not ours stops the run", nil,
			"echo NO MARKER\n", 93},
		{"a real program runs", nil,
			"CSX-SCRIPT-V1\nset -eu\necho ran\n", 0},
		{"a stalled remote program is terminated", nil,
			"CSX-SCRIPT-V1\nset -eu\nsleep 20\n", 124},
		{"and its exit code is what comes back", nil,
			"CSX-SCRIPT-V1\nset -eu\nexit 42\n", 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", runner)
			cmd.Stdin = strings.NewReader(tc.program)
			cmd.Env = append(os.Environ(), "PATH="+testPath)
			if tc.env != nil {
				cmd.Env = append(cmd.Env, tc.env...)
			}
			out, err := cmd.CombinedOutput()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatalf("running the runner: %v", err)
			}
			if code != tc.want {
				t.Errorf("exit = %d, want %d\noutput: %s", code, tc.want, out)
			}
		})
	}
}

// The runner is embedded in $psi.Arguments inside a "..." argv element, so a
// double quote in it ends the remote command early. The first fail-closed
// runner used trap "rm -f $f" and printf "#", and the host answered
//
//	bash: -c: line 2: syntax error: unexpected end of file
//
// The deploy stopped without touching production — which is what a
// fail-closed runner is for — but it stopped on its own quoting rather than
// on a real failure, and the fixture above did not catch it because it runs
// the program through sh -c directly and never crosses the wrapper.
func TestTheRemoteRunnerSurvivesTheArgvWrapper(t *testing.T) {
	runner := remoteRunner(t)
	if strings.Contains(runner, `"`) {
		t.Errorf("the remote runner contains a double quote, which ends the ssh argument early:\n%s", runner)
	}
	// And the wrapper it actually goes into still balances.
	script := readDeployFixture(t, "deploy.ps1")
	line := `$psi.Arguments = '-i "' + $resolvedKeyPath + '" -o StrictHostKeyChecking=yes -o UserKnownHostsFile="' + $resolvedKnownHostsPath + '" -o ConnectTimeout=20 ' + $remote + ' "' + $remoteRunner + '"'`
	if !strings.Contains(script, line) {
		t.Error("the ssh invocation no longer wraps the runner the way this test reasons about")
	}
}

func TestRemoteAbortFencesLateCommandsBeforeRollback(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")
	for _, required := range []string{
		`flock -w 5 $HOME/.csx-deploy-command.lock sh $f`,
		`test ! -e "$HOME/.csx-deploy-aborted-`,
		`$script:deployRecoveryMode`,
		`Invoke-RemoteScript $rollbackServer 170`,
		`Invoke-RemoteScript $rollbackCaddy 80`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("remote rollback fencing missing %q", required)
		}
	}
}

func TestCanceledGenerationCannotRunALateRemoteCommand(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	script := readDeployFixture(t, "deploy.ps1")
	pattern := regexp.MustCompile(`\$guard = if \(\$script:deployRecoveryMode\) \{ "" \} else \{ '([^']+)' \+ \$deployLockOwner \+ '([^']+)' \}`)
	parts := pattern.FindStringSubmatch(script)
	if parts == nil {
		t.Fatal("cannot extract the shipped generation guard")
	}
	const owner = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name, marker string
		want         int
	}{
		{"active generation", "", 0},
		{"canceled generation", ".csx-deploy-aborted-" + owner, 75},
		{"different generation", ".csx-deploy-aborted-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.marker != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.marker), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			guard := strings.ReplaceAll(parts[1]+owner+parts[2], "$HOME/", filepath.ToSlash(dir)+"/")
			runner, testPath := remoteRunnerExecutionFixture(t)
			cmd := exec.Command(sh, "-c", runner)
			cmd.Env = append(os.Environ(), "PATH="+testPath)
			cmd.Stdin = strings.NewReader("CSX-SCRIPT-V1\nset -e\n" + guard + "\nprintf MUTATION-EXECUTED\n")
			out, err := cmd.CombinedOutput()
			code := 0
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.want {
				t.Fatalf("generation guard exit=%d want=%d: %s", code, tc.want, out)
			}
			if (strings.Contains(string(out), "MUTATION-EXECUTED")) != (tc.want == 0) {
				t.Fatalf("canceled command mutation boundary failed: %s", out)
			}
		})
	}
}

func TestUnknownRemoteOutcomeAndFailedRecoveryKeepTheTransactionLocked(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")
	for _, marker := range []string{"if ($script:remoteOutcomeUnknown) {", "if ($allFailures.Count -gt 1) {"} {
		start := strings.Index(script, marker)
		if start < 0 {
			t.Fatalf("missing safety branch %q", marker)
		}
		body := script[start:]
		end := strings.Index(body, "\n    }")
		if end < 0 {
			t.Fatalf("unterminated safety branch %q", marker)
		}
		body = body[:end]
		if !strings.Contains(body, "$script:retainDeployLock = $true") || !strings.Contains(body, "throw") {
			t.Errorf("%s does not retain the lock and stop unsafe recovery", marker)
		}
	}
	if !strings.Contains(script, "if ($deployLockHeld -and -not $script:retainDeployLock) {") {
		t.Error("cleanup can release a retained production transaction lock")
	}
	outer := strings.Index(script, "$deployScriptFailure = $_")
	if outer < 0 {
		t.Fatal("pre-activation failures have no outer failure handler")
	}
	tail := script[outer:]
	end := strings.Index(tail, "} finally {")
	if end < 0 {
		t.Fatal("outer failure handler has no cleanup boundary")
	}
	outerHandler := tail[:end]
	for _, required := range []string{
		"if (-not $script:deployGenerationFenced -and -not $script:retainDeployLock)",
		"Set-DeployPhase failure-fence 20", "$script:deployRecoveryMode = $true",
		"Invoke-RemoteScript ('umask 077; touch", "$script:deployGenerationFenced = $true",
		"catch { $script:retainDeployLock = $true }",
		"if ($script:remoteOutcomeUnknown) { $script:retainDeployLock = $true }",
	} {
		if !strings.Contains(outerHandler, required) {
			t.Errorf("pre-inner-transaction failure is not fenced before cleanup: missing %q", required)
		}
	}
}
