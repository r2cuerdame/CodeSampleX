package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const productionEnvironment = "codesamplex-production"

func productionWorkflow(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "production-deploy.yml"))
	if err != nil {
		t.Fatalf("read production deploy workflow: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

func TestProductionDeployIsExplicitSerializedAndImmutable(t *testing.T) {
	workflow := productionWorkflow(t)
	head := workflow
	if i := strings.Index(workflow, "\njobs:"); i >= 0 {
		head = workflow[:i]
	}
	for _, required := range []string{
		"workflow_dispatch:",
		"commit_sha:",
		"previous_production_sha:",
		"merge_verdict:",
		"requires_human_decision:",
		"side_effect_class:",
		"linear_issue:",
		"group: codesamplex-production",
		"cancel-in-progress: false",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("production workflow is missing %q", required)
		}
	}
	for _, forbidden := range []string{"  push:", "pull_request:", "schedule:", "repository_dispatch:"} {
		if strings.Contains(head, forbidden) {
			t.Errorf("production deploy has an implicit trigger %q", forbidden)
		}
	}
	for _, required := range []string{
		"ref: ${{ inputs.commit_sha }}",
		"fetch-depth: 0",
		"test \"$(git rev-parse HEAD)\" = \"$TARGET_SHA\"",
		"git merge-base --is-ancestor \"$TARGET_SHA\" origin/main",
		"commits/$TARGET_SHA/check-runs",
		`select(.name == "Test" and .conclusion == "success")`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("immutable-main/required-CI guard is missing %q", required)
		}
	}
}

func TestProductionRequiresSuccessfulSameTargetReleaseAndFarm(t *testing.T) {
	workflow := productionWorkflow(t)
	head := workflow
	if i := strings.Index(workflow, "\njobs:"); i >= 0 {
		head = workflow[:i]
	}
	if !strings.Contains(head, "actions: read") {
		t.Fatal("production eligibility cannot read the same-target Release run")
	}
	jobs := releaseJobs(t, workflow)
	eligibility, ok := jobs["eligibility"]
	if !ok {
		t.Fatalf("production workflow has no eligibility job; jobs=%v", jobKeys(jobs))
	}
	for _, required := range []string{
		"Require successful same-target release and farm rollout",
		"actions/workflows/release.yml/runs?head_sha=${TARGET_SHA}&event=push&status=success&per_page=1",
		".workflow_runs[0].id // 0",
		`if ! [[ "$release_run" =~ ^[1-9][0-9]*$ ]]; then`,
	} {
		if !strings.Contains(eligibility, required) {
			t.Errorf("same-target release/farm gate is missing %q", required)
		}
	}
	if strings.Contains(eligibility, "secrets.") {
		t.Fatal("release/farm eligibility introduced a secret before the production deploy job")
	}
	deploy := jobs["deploy"]
	if !strings.Contains(deploy, "needs: eligibility") {
		t.Fatal("production deploy no longer waits for release/farm eligibility")
	}
}

func TestOnlyTheDeployJobCanReachProductionCredentials(t *testing.T) {
	jobs := releaseJobs(t, productionWorkflow(t))
	deploy, ok := jobs["deploy"]
	if !ok {
		t.Fatalf("production workflow has no deploy job; jobs=%v", jobKeys(jobs))
	}
	if !strings.Contains(deploy, "environment: "+productionEnvironment) {
		t.Fatalf("deploy job does not enter %s", productionEnvironment)
	}
	for name, body := range jobs {
		if name != "deploy" && strings.Contains(body, "environment:") {
			t.Errorf("non-deploy job %q enters an environment", name)
		}
		if name != "deploy" && strings.Contains(body, "secrets.") {
			t.Errorf("non-deploy job %q references a secret", name)
		}
	}
	for _, required := range []string{
		"secrets.CSX_PRODUCTION_SSH_KEY",
		"secrets.CSX_PRODUCTION_KNOWN_HOSTS",
		"vars.CSX_PRODUCTION_HOST",
		"deploy/lightsail/deploy-production.ps1",
		"Remove production SSH material",
	} {
		if !strings.Contains(deploy, required) {
			t.Errorf("deploy credential/runner contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"CSX_UPDATE_SIGNING_KEY_B64", "codesamplex-release-signing",
		"contents: write", "id-token: write", "packages: write",
	} {
		if strings.Contains(productionWorkflow(t), forbidden) {
			t.Errorf("production workflow crosses the release/publish boundary through %q", forbidden)
		}
	}
}

func TestProductionEvidenceIsAlwaysRetained(t *testing.T) {
	workflow := productionWorkflow(t)
	for _, required := range []string{
		"production-deploy-evidence.json",
		"if: always()",
		"actions/upload-artifact@",
		"retention-days:",
		"deployedSha",
		"imageDigest",
		"migrationVersion",
		"health",
		"rollback",
		"invariants",
		"failureEvidenceQuality",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("production evidence contract is missing %q", required)
		}
	}
}

func TestEveryProductionActionIsPinnedAndReviewed(t *testing.T) {
	allowed := map[string]bool{
		"actions/checkout":        true,
		"actions/setup-go":        true,
		"actions/upload-artifact": true,
	}
	seen := 0
	for _, line := range strings.Split(productionWorkflow(t), "\n") {
		m := usesLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		seen++
		ref := pinnedRef.FindStringSubmatch(m[1])
		if ref == nil {
			t.Fatalf("action %q is not pinned to a full commit SHA", m[1])
		}
		if !allowed[ref[1]] {
			t.Fatalf("action %q is outside the reviewed production set", ref[1])
		}
	}
	if seen == 0 {
		t.Fatal("production workflow contains no actions")
	}
}

func TestSSHUsesOnlyThePinnedHostKey(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "deploy", "lightsail", "deploy.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{"KnownHostsPath", "StrictHostKeyChecking=yes", "UserKnownHostsFile"} {
		if !strings.Contains(script, required) {
			t.Errorf("deploy.ps1 is missing pinned-host guard %q", required)
		}
	}
	if regexp.MustCompile(`StrictHostKeyChecking=(?:no|accept-new)`).MatchString(script) {
		t.Fatal("deploy.ps1 still permits an unpinned or first-seen host key")
	}
}

func TestProductionProbeToleratesWindowsPowerShellStdinBOM(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "deploy", "lightsail", "deploy-production.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	if !strings.Contains(script, `"{ printf '#'; cat; } | sh"`) {
		t.Fatal("production probe lacks the stdin envelope that neutralizes a Windows PowerShell BOM")
	}
}
