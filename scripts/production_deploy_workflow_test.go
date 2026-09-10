package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const productionEnvironment = "codesamplex-production"

func TestProductionSeparatesReleasedPayloadFromReviewedController(t *testing.T) {
	workflow := productionWorkflow(t)
	deploy := releaseJobs(t, workflow)["deploy"]
	for _, required := range []string{
		"ref: ${{ github.sha }}", "path: operations", "path: payload",
		"./operations/deploy/lightsail/deploy-production.ps1",
		`-SourceRepoPath (Join-Path $env:GITHUB_WORKSPACE "payload")`,
		"-OperationalRevision $env:OPERATIONAL_SHA",
	} {
		if !strings.Contains(deploy, required) {
			t.Errorf("payload/controller separation missing %q", required)
		}
	}
	if strings.Contains(deploy, "./payload/deploy/") {
		t.Fatal("old payload must not select its old deployment scripts")
	}
	for _, required := range []string{`test "$GITHUB_REF" = refs/heads/main`, "head_sha=${OPERATIONAL_SHA}&branch=main&status=success"} {
		if !strings.Contains(workflow, required) {
			t.Errorf("controller provenance missing %q", required)
		}
	}
}

func productionWorkflow(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "production-deploy.yml"))
	if err != nil {
		t.Fatalf("read production deploy workflow: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

func productionWorkflowStep(t *testing.T, workflow, name string) string {
	t.Helper()
	marker := "      - name: " + name
	start := strings.Index(workflow, marker)
	if start < 0 {
		t.Fatalf("production workflow has no %q step", name)
	}
	tail := workflow[start+len(marker):]
	if end := strings.Index(tail, "\n      - name: "); end >= 0 {
		tail = tail[:end]
	}
	return tail
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
		"tracking_issue:",
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
	eligibility := releaseJobs(t, workflow)["eligibility"]
	for _, required := range []string{
		`ref: ${{ github.sha }}`,
		`ref: ${{ inputs.commit_sha }}`,
		`fetch-depth: 0`,
		`path: operations`,
		`path: payload`,
		`go-version-file: operations/go.mod`,
		`working-directory: operations`,
		`test "$(git rev-parse HEAD)" = "$GITHUB_SHA"`,
		`test "$(git -C ../payload rev-parse HEAD)" = "$TARGET_SHA"`,
		`git -C ../payload merge-base --is-ancestor "$TARGET_SHA" origin/main`,
		`go run ./cmd/csx-deploy-gate -repo ../payload`,
	} {
		if !strings.Contains(eligibility, required) {
			t.Errorf("immutable operational policy/payload guard is missing %q", required)
		}
	}
}

func TestProductionRequiresSuccessfulCanonicalMainCIRun(t *testing.T) {
	step := productionWorkflowStep(t, productionWorkflow(t), "Require successful canonical main CI run")
	for _, required := range []string{
		"actions/workflows/ci.yml/runs?head_sha=${TARGET_SHA}&branch=main&status=success&per_page=20",
		`select(.event == "push" or .event == "workflow_dispatch")`,
		`if ! [[ "$ci_run" =~ ^[1-9][0-9]*$ ]]; then`,
		"Canonical CI evidence:",
	} {
		if !strings.Contains(step, required) {
			t.Errorf("canonical push/main CI provenance gate is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"commits/$TARGET_SHA/check-runs",
		`select(.name == "Test" and .conclusion == "success")`,
		"pull_request",
		"repository_dispatch",
	} {
		if strings.Contains(step, forbidden) {
			t.Errorf("canonical CI provenance can still be satisfied by an arbitrary check run: %q", forbidden)
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
		"actions/workflows/release.yml/runs?head_sha=${TARGET_SHA}&status=success&per_page=20",
		`select(.event == "push" or .event == "workflow_dispatch")`,
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
		"servedRevision",
		"observation",
		"failureClass",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("production evidence contract is missing %q", required)
		}
	}
}

func TestProductionJobBudgetExcludesTheFullBuilderWait(t *testing.T) {
	workflow := productionWorkflow(t)
	deployJob, ok := releaseJobs(t, workflow)["deploy"]
	if !ok {
		t.Fatal("production workflow has no deploy job")
	}

	if !strings.Contains(deployJob, "timeout-minutes: ${{ fromJSON(needs.eligibility.outputs.deploy_job_minutes) }}") {
		t.Fatal("production timeout must use the separately validated migration budget")
	}

	raw, err := os.ReadFile(filepath.Join("..", "deploy", "lightsail", "deploy.ps1"))
	if err != nil {
		t.Fatalf("read production deploy script: %v", err)
	}
	script := string(raw)
	for _, forbidden := range []string{"builderFreshPollAttempts", "builderFreshPollSeconds", "collectBuilderFreshScript"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("production deploy still owns full-builder wait %q", forbidden)
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

func TestProductionProbeHasABoundedBOMSafeStdinEnvelope(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "deploy", "lightsail", "deploy-production.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	if !strings.Contains(script, `"{ printf '#'; cat; } | timeout --signal=TERM --kill-after=2 20 sh"`) {
		t.Fatal("production probe lacks the stdin envelope that neutralizes a Windows PowerShell BOM")
	}
}

func TestProductionRequiresTargetSpecificTrackingIssue(t *testing.T) {
	step := productionWorkflowStep(t, productionWorkflow(t), "Require target-specific GitHub tracking issue")
	for _, required := range []string{
		"TRACKING_ISSUE: ${{ inputs.tracking_issue }}",
		"repos/${GITHUB_REPOSITORY}/issues/${issue_number}",
		"--paginate",
		"TARGET_SHA",
		"Tracking issue evidence:",
	} {
		if !strings.Contains(step, required) {
			t.Errorf("target-specific tracking issue gate is missing %q", required)
		}
	}
}

func TestProductionCriticalPathHasAnExplicitRollbackReserve(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "deploy", "lightsail", "deploy.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	ceilings := map[string]int{"preparation": 180, "staging": 240, "activation": 30, "activation-smoke": 180, "rollback": 300, "host-recovery": 270, "cleanup": 60}
	for phase, seconds := range ceilings {
		expected := "Set-DeployPhase " + phase + " " + strconv.Itoa(seconds)
		if !strings.Contains(script, expected) {
			t.Errorf("critical-path ceiling changed: missing %q", expected)
		}
	}
	workflow := productionWorkflow(t)
	step := productionWorkflowStep(t, workflow, "Deploy and verify")
	// Migration is independent. Before/after work includes the COMPLETE
	// failure path and two identity probes, not just a successful startup.
	const overheadSeconds = 180 + 240 + 30 + 300 + 180 + 270 + 60 + 2*30 + 20
	if overheadSeconds >= 24*60 || !strings.Contains(step, "timeout-minutes: ${{ fromJSON(needs.eligibility.outputs.deploy_step_minutes) }}") {
		t.Error("step must cover bounded work and independent host recovery")
	}
	deploy := releaseJobs(t, workflow)["deploy"]
	if !strings.Contains(deploy, "timeout-minutes: ${{ fromJSON(needs.eligibility.outputs.deploy_job_minutes) }}") {
		t.Error("job must use its bounded checkout and artifact reserve")
	}
	for _, forbidden := range []string{"observe-production.ps1", "collect-extended-observation.sh", "collect-production-evidence.sh"} {
		if strings.Contains(step, forbidden) {
			t.Errorf("optional observation holds the deployment step open: %q", forbidden)
		}
	}
}
