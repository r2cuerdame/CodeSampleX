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

// This is the boundary that makes a Registry replay safe to dispatch for an
// existing tag: it cannot enter the Release or Farm job graph.
func TestRegistryOnlyWorkflowHasNoReleaseOrFarmPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "registry-metadata.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := strings.ReplaceAll(string(raw), "\r\n", "\n")
	jobs := releaseJobs(t, workflow)
	job, ok := jobs["registry"]
	if !ok || len(jobs) != 1 {
		t.Fatalf("Registry workflow must have exactly one registry job; got %v", jobKeys(jobs))
	}
	for _, required := range []string{
		"  workflow_dispatch:\n", "      tag:\n", "  contents: read\n",
		"      contents: read", "      id-token: write", "ref: refs/tags/${{ inputs.tag }}",
		"gh api \"repos/${GITHUB_REPOSITORY}/git/ref/tags/${TAG}\"",
		"gh release download \"$TAG\"", "server.json > server.tmp",
		"./mcp-publisher login github-oidc", "./mcp-publisher publish",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("Registry path lost required guard or publish operation %q", required)
		}
	}
	if strings.Contains(job, "    needs:") {
		t.Fatal("Registry job has a dependency that can connect it to another job")
	}
	for _, forbidden := range []string{
		"actions/download-artifact", "actions/upload-artifact", "actions/setup-go",
		"gh release create", "gh release edit", "gh release upload", "gh release delete",
		"gh workflow run", "npm publish", "farm", "deploy", "contents: write",
		"CSX_FARM_DISPATCH_TOKEN", "CSX_UPDATE_SIGNING_KEY_B64",
	} {
		if strings.Contains(strings.ToLower(job), strings.ToLower(forbidden)) {
			t.Errorf("Registry job crosses forbidden Release/Farm boundary %q", forbidden)
		}
	}
	var steps []string
	for _, line := range strings.Split(job, "\n") {
		if strings.HasPrefix(line, "      - name: ") || strings.HasPrefix(line, "      - uses: ") {
			steps = append(steps, line)
		}
	}
	want := []string{
		"      - name: Require an existing version tag",
		"      - name: Require a public GitHub release",
		"      - uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5",
		"      - name: Fill the tagged server.json from the published checksum metadata",
		"      - name: Install mcp-publisher",
		"      - name: Publish to the MCP Registry",
	}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Registry step allowlist changed:\ngot %q\nwant %q", steps, want)
	}
}

// A draft has a tag and authenticated gh release download can read its assets.
// The public API gate must reject it before any checkout or checksum download.
func TestRegistryRequiresPublicReleaseBeforeDownload(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "registry-metadata.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := strings.ReplaceAll(string(raw), "\r\n", "\n")
	jobs := releaseJobs(t, workflow)
	job := jobs["registry"]
	const marker = "      - name: Require a public GitHub release\n"
	guard := strings.Index(job, marker)
	checkout := strings.Index(job, "      - uses: actions/checkout@")
	download := strings.Index(job, "gh release download \"$TAG\"")
	if guard < 0 || checkout < 0 || download < 0 || guard > checkout || guard > download {
		t.Fatal("public Release gate must precede checkout and checksum download")
	}
	step := job[guard+len(marker) : checkout]
	for _, required := range []string{
		"https://api.github.com/repos/${GITHUB_REPOSITORY}/releases/tags/${TAG}",
		"curl -q -fsS --max-time 30 --output /dev/null",
	} {
		if !strings.Contains(step, required) {
			t.Fatalf("public Release gate lacks %q", required)
		}
	}
	if strings.Contains(step, "GH_TOKEN") || strings.Contains(step, "Authorization") {
		t.Fatal("public Release gate must not send credentials")
	}

	shell := "bash"
	if runtime.GOOS == "windows" {
		shell = `C:\Program Files\Git\bin\bash.exe`
	}
	bash, err := exec.LookPath(shell)
	if err != nil {
		if runtime.GOOS == "linux" {
			t.Fatal("the Linux workflow requires bash")
		}
		t.Skip("bash is unavailable")
	}
	const run = "        run: |\n"
	start := strings.Index(step, run)
	if start < 0 {
		t.Fatal("public Release gate lacks executable shell")
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(step[start+len(run):], "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "          ") {
			t.Fatalf("unexpected shell indentation: %q", line)
		}
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	body := strings.Join(lines, "\n") + "\n"
	for _, tc := range []struct {
		name     string
		status   string
		wantPass bool
	}{
		{name: "published release", status: "0", wantPass: true},
		{name: "draft release returns public 404", status: "22"},
		{name: "release lookup unavailable", status: "6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mock := `curl() {
  printf 'called\n' >> curl-calls
  if [ "$#" -ne 9 ] || [ "$1" != -q ] || [ "$2" != -fsS ] ||
     [ "$3" != --max-time ] || [ "$4" != 30 ] ||
     [ "$5" != --output ] || [ "$6" != /dev/null ] ||
     [ "$7" != --header ] || [ "$8" != 'Accept: application/vnd.github+json' ] ||
     [ "$9" != 'https://api.github.com/repos/r2cuerdame/CodeSampleX/releases/tags/v0.2.5' ]; then
    printf 'unexpected public Release lookup arguments\n' >&2
    return 64
  fi
  return "$CSX_TEST_CURL_STATUS"
}
`
			if err := os.WriteFile(filepath.Join(dir, "verify.sh"), []byte(mock+body), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "verify.sh")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "TAG=v0.2.5", "GITHUB_REPOSITORY=r2cuerdame/CodeSampleX", "CSX_TEST_CURL_STATUS="+tc.status, "GH_TOKEN=", "GITHUB_TOKEN=")
			out, runErr := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("public Release gate timed out: %v\n%s", ctx.Err(), out)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "curl-calls"))
			if err != nil || string(calls) != "called\n" {
				t.Fatalf("expected one public Release lookup: calls=%q err=%v\n%s", calls, err, out)
			}
			if (runErr == nil) != tc.wantPass {
				t.Fatalf("public Release gate pass=%v, want %v: %v\n%s", runErr == nil, tc.wantPass, runErr, out)
			}
		})
	}
}
