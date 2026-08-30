package lightsail

import (
	"os/exec"
	"regexp"
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
	return strings.ReplaceAll(m[1], "''", "'")
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
func TestTheRemoteRunnerIsFailClosed(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX sh on this machine to run the runner against")
	}
	runner := remoteRunner(t)

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
		{"and its exit code is what comes back", nil,
			"CSX-SCRIPT-V1\nset -eu\nexit 42\n", 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", runner)
			cmd.Stdin = strings.NewReader(tc.program)
			if tc.env != nil {
				cmd.Env = append(cmd.Environ(), tc.env...)
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
