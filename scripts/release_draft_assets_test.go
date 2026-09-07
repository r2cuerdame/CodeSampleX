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

// Exercise the workflow shell itself: a draft is discoverable by gh release
// view, while the REST releases/tags endpoint only finds published releases.
func TestReleaseDraftAssetVerification(t *testing.T) {
	shell := "bash"
	if runtime.GOOS == "windows" {
		// System32 bash.exe launches WSL and does not preserve this test's env.
		shell = `C:\Program Files\Git\bin\bash.exe`
	}
	bash, err := exec.LookPath(shell)
	if err != nil {
		if runtime.GOOS == "linux" {
			t.Fatal("the canonical Linux release contract requires bash")
		}
		t.Skip("bash is unavailable")
	}

	script := releaseDraftAssetShell(t)
	assets := []string{
		"csx-darwin-amd64", "csx-darwin-arm64", "csx-linux-amd64", "csx-linux-arm64",
		"csx-windows-amd64.exe", "csx-windows-arm64.exe",
		"csx-launcher-windows-amd64.exe", "csx-launcher-windows-arm64.exe",
		"csx-server-linux-amd64", "SHA256SUMS.txt", "csx-update-stable.json", "csx-bootstrap-stable.json",
		"codesamplex-mcp.mcpb", "codesamplex-mcp.mcpb.sha256",
	}
	for _, tc := range []struct {
		name       string
		assets     []string
		lookupExit string
		ref        string
		repo       string
		wrongQuery bool
		wantPass   bool
	}{
		{name: "draft exact assets", assets: assets, wantPass: true},
		{name: "missing asset", assets: assets[1:]},
		{name: "extra asset", assets: append(append([]string{}, assets...), "unexpected.exe")},
		{name: "duplicate asset", assets: append(append([]string{}, assets...), assets[0])},
		{name: "lookup failure with valid output", assets: assets, lookupExit: "42"},
		{name: "unexpected tag", assets: assets, ref: "refs/tags/v0.1.999"},
		{name: "unexpected repository", assets: assets, repo: "r2cuerdame/OtherRepo"},
		{name: "unexpected query", assets: assets, wrongQuery: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "draft-assets"), []byte(strings.Join(tc.assets, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			body := script
			if tc.wrongQuery {
				body = strings.ReplaceAll(body, ".assets[].name", ".assets[].label")
				if body == script {
					t.Fatal("cannot construct the unexpected-query regression")
				}
			}
			ref, repo, lookupExit := tc.ref, tc.repo, tc.lookupExit
			if ref == "" {
				ref = "refs/tags/v0.1.146"
			}
			if repo == "" {
				repo = "r2cuerdame/CodeSampleX"
			}
			if lookupExit == "" {
				lookupExit = "0"
			}
			// A shell function intercepts gh; no API call or credential is needed.
			// Do not enable pipefail here: the workflow must supply that protection.
			mock := `gh() {
  printf 'called\n' >> gh-calls
  if [ "$#" -ne 9 ] || [ "$1" != release ] || [ "$2" != view ] ||
     [ "$3" != v0.1.146 ] || [ "$4" != --repo ] ||
     [ "$5" != r2cuerdame/CodeSampleX ] || [ "$6" != --json ] ||
     [ "$7" != assets ] || [ "$8" != --jq ] || [ "$9" != '.assets[].name' ]; then
    printf 'unexpected draft lookup arguments\n' >&2
    printf 'argument=<%s>\n' "$@" >&2
    return 64
  fi
  while IFS= read -r asset; do printf '%s\n' "$asset"; done < draft-assets
  return "$CSX_TEST_LOOKUP_EXIT"
}
`
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// A script file preserves shell quoting through Windows argv handling.
			if err := os.WriteFile(filepath.Join(dir, "verify.sh"), []byte(mock+body), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "verify.sh")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_REF="+ref, "GITHUB_REPOSITORY="+repo,
				"CSX_TEST_LOOKUP_EXIT="+lookupExit, "GH_TOKEN=", "GITHUB_TOKEN=")
			out, runErr := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("draft asset shell timed out: %v\n%s", ctx.Err(), out)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "gh-calls"))
			if err != nil || string(calls) != "called\n" {
				t.Fatalf("expected exactly one mocked draft lookup: calls=%q err=%v\n%s", calls, err, out)
			}
			if (runErr == nil) != tc.wantPass {
				t.Fatalf("draft asset gate pass=%v, want %v: %v\n%s", runErr == nil, tc.wantPass, runErr, out)
			}
		})
	}
}

func releaseDraftAssetShell(t *testing.T) string {
	t.Helper()
	job := releaseJobs(t, releaseWorkflow(t))["publish"]
	const marker = "      - name: Verify exact uploaded release asset set\n"
	start := strings.Index(job, marker)
	if start < 0 {
		t.Fatal("release publish job lacks the uploaded asset verification step")
	}
	step := job[start+len(marker):]
	if end := strings.Index(step, "\n      - "); end >= 0 {
		step = step[:end]
	}
	const run = "        run: |\n"
	start = strings.Index(step, run)
	if start < 0 {
		t.Fatal("uploaded asset verification has no executable shell block")
	}
	var lines []string
	for _, line := range strings.Split(step[start+len(run):], "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "          ") {
			t.Fatalf("unexpected shell block indentation: %q", line)
		}
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	// Relocate only scratch files, keeping concurrent tests out of shared /tmp.
	return strings.NewReplacer("/tmp/expected-assets", "expected-assets", "/tmp/actual-assets", "actual-assets").Replace(strings.Join(lines, "\n") + "\n")
}
