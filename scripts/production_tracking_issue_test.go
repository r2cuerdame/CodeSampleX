package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProductionTrackingIssueTargetSHA(t *testing.T) {
	shell := "bash"
	if runtime.GOOS == "windows" {
		// System32 bash.exe launches WSL and does not preserve this test's env.
		shell = `C:\Program Files\Git\bin\bash.exe`
	}
	bash, err := exec.LookPath(shell)
	if err != nil {
		if runtime.GOOS == "linux" {
			t.Fatal("the canonical Linux production contract requires bash")
		}
		t.Skip("bash is unavailable")
	}

	script := productionTrackingIssueShell(t)
	const targetSHA = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name     string
		content  string
		wantPass bool
	}{
		{
			name:     "target SHA present with pipefail",
			content:  targetSHA + "\n" + strings.Repeat("unrelated tracking evidence\n", 250000),
			wantPass: true,
		},
		{
			name:    "target SHA absent",
			content: strings.Repeat("unrelated tracking evidence\n", 250000),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "issue-content"), []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			mock := `gh() {
  if [ "$1" != api ]; then
    printf 'unexpected gh command: %s\n' "$*" >&2
    return 64
  fi
  case "$4" in
    */comments) return 0 ;;
    */issues/174) cat issue-content ;;
    *) printf 'unexpected API URL: %s\n' "$4" >&2; return 64 ;;
  esac
}
`
			if err := os.WriteFile(filepath.Join(dir, "verify.sh"), []byte(mock+script), 0600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "verify.sh")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"TARGET_SHA="+targetSHA,
				"TRACKING_ISSUE=#174",
				"GITHUB_REPOSITORY=r2cuerdame/CodeSampleX",
				"GITHUB_SERVER_URL=https://github.com",
				"GITHUB_STEP_SUMMARY=summary",
				"GH_TOKEN=",
				"GITHUB_TOKEN=",
			)
			out, runErr := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("tracking issue shell timed out: %v\n%s", ctx.Err(), out)
			}
			if (runErr == nil) != tc.wantPass {
				t.Fatalf("tracking issue gate pass=%v, want %v: %v\n%s", runErr == nil, tc.wantPass, runErr, out)
			}
		})
	}
}

func productionTrackingIssueShell(t *testing.T) string {
	t.Helper()
	step := productionWorkflowStep(t, productionWorkflow(t), "Require target-specific GitHub tracking issue")
	const run = "\n        run: |\n"
	start := strings.Index(step, run)
	if start < 0 {
		t.Fatal("target-specific tracking issue step has no executable shell block")
	}
	var lines []string
	for _, line := range strings.Split(step[start+len(run):], "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "          ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	return strings.Join(lines, "\n") + "\n"
}
