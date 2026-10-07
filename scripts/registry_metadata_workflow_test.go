package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"      - uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5",
		"      - name: Fill the tagged server.json from the published checksum metadata",
		"      - name: Install mcp-publisher",
		"      - name: Publish to the MCP Registry",
	}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Registry step allowlist changed:\ngot %q\nwant %q", steps, want)
	}
}
