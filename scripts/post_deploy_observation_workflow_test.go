package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func postDeployObservationWorkflow(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "post-deploy-observation.yml"))
	if err != nil {
		t.Fatalf("read post-deploy observation workflow: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

func postDeployObservationStep(t *testing.T, workflow, name string) string {
	t.Helper()
	marker := "      - name: " + name
	start := strings.Index(workflow, marker)
	if start < 0 {
		t.Fatalf("post-deploy observation workflow has no %q step", name)
	}
	tail := workflow[start+len(marker):]
	if end := strings.Index(tail, "\n      - "); end >= 0 {
		tail = tail[:end]
	}
	return tail
}

func TestPostDeployObservationRunsAfterSuccessfulProductionOrManualRetry(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	head := workflow
	if i := strings.Index(workflow, "\njobs:"); i >= 0 {
		head = workflow[:i]
	}
	for _, required := range []string{
		"workflow_run:",
		"workflows: [Production deploy, Production reconciliation]",
		"types: [completed]",
		"workflow_dispatch:",
		"deploy_run_id:",
		"github.event.workflow_run.conclusion == 'success'",
		"github.event.workflow_run.event == 'workflow_dispatch'",
		"github.event.workflow_run.head_repository.full_name == github.repository",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("post-deploy trigger contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{"  push:", "pull_request:", "schedule:", "repository_dispatch:"} {
		if strings.Contains(head, forbidden) {
			t.Errorf("post-deploy observation has unintended trigger %q", forbidden)
		}
	}
}

func TestPostDeployObservationKeepsRunSpecificConcurrency(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	for _, required := range []string{
		"group: codesamplex-post-deploy-${{ github.event.workflow_run.id || inputs.deploy_run_id }}",
		"cancel-in-progress: false",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("post-deploy concurrency contract is missing %q", required)
		}
	}
	if strings.Contains(workflow, "\n  group: codesamplex-production\n") {
		t.Fatal("observer must not rely on the non-FIFO production concurrency group")
	}
}

func TestPostDeployObservationAuthenticatesDeploymentArtifact(t *testing.T) {
	step := postDeployObservationStep(t, postDeployObservationWorkflow(t), "Validate deployment provenance and download its evidence")
	for _, required := range []string{
		`actions/runs/${DEPLOY_RUN_ID}`,
		`.path == ".github/workflows/production-deploy.yml"`,
		`.event == "workflow_dispatch"`,
		`.conclusion == "success"`,
		`.repository.full_name == $repo`,
		`.run_number | select(type == "number" and . > 0)`,
		`deploy_run_number=${deploy_run_number}`,
		`artifact_name="production-evidence-${DEPLOY_RUN_ID}"`,
		`artifact_count`,
		`actions/artifacts/${artifact_id}/zip`,
		`.workflowRunId | tostring`,
		`.targetSha`,
		`.previousProductionSha`,
		`.imageDigest`,
		`image_digest=${image_digest}`,
		`.serverStartedAt`,
		`.migrationVersion`,
		`migration_version=${migration_version}`,
		`.trackingIssue`,
		`test "$deployed_sha" = "$target_sha"`,
	} {
		if !strings.Contains(step, required) {
			t.Errorf("deployment artifact provenance contract is missing %q", required)
		}
	}
	if strings.Contains(step, "actions/download-artifact") {
		t.Fatal("cross-workflow deploy evidence must be selected by authenticated run and artifact IDs")
	}
}

func TestPostDeployObservationDoesNotAuthenticateByDynamicRunName(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	for _, stepName := range []string{
		"Validate deployment provenance and download its evidence",
		"Treat a validated newer deployment as superseding this observation",
	} {
		step := postDeployObservationStep(t, workflow, stepName)
		if strings.Contains(step, `.name == "Production deploy"`) {
			t.Errorf("%s must authenticate by immutable workflow path/repository evidence, not dynamic run-name", stepName)
		}
		if !strings.Contains(step, `.path == ".github/workflows/production-deploy.yml"`) {
			t.Errorf("%s lost the exact production workflow path check", stepName)
		}
	}
}

func TestPostDeployObservationOnlySupersedesFromAuthenticatedReplacement(t *testing.T) {
	step := postDeployObservationStep(t, postDeployObservationWorkflow(t), "Treat a validated newer deployment as superseding this observation")
	for _, required := range []string{
		"if: always() && steps.deployment.outputs.deploy_run_number != '' && steps.observer.outcome != 'success'",
		`.run_number > $original`,
		`gh api --paginate --slurp`,
		`.[].workflow_runs[]`,
		`.status == "completed" and .conclusion == "success"`,
		`.path == ".github/workflows/production-deploy.yml"`,
		`.repository.full_name == $repo`,
		`WORKFLOW_SHA: ${{ github.workflow_sha }}`,
		`git merge-base --is-ancestor "$WORKFLOW_SHA" origin/main`,
		`git show "${WORKFLOW_SHA}:deploy/lightsail/collect-post-deploy-observation.sh"`,
		`collector="$RUNNER_TEMP/csx-supersession-collector.sh"`,
		`actions/runs/${candidate_id}/jobs?filter=latest&per_page=100`,
		`.name == "Roll out production"`,
		`"$deploy_job_count" != "1"`,
		`(.run_id | tostring) == $id`,
		`.started_at`,
		`.completed_at`,
		`artifact_name="production-evidence-${candidate_id}"`,
		`actions/artifacts/${artifact_id}/zip`,
		`(.workflowRunId | tostring) == $id`,
		`.targetSha == $sha and .deployedSha == $sha and .servedRevision == $sha`,
		`.health == "ok" and .smoke == "pass" and .rollback == "not-needed"`,
		`candidate_started_epoch=$(date -u -d "$candidate_started" +%s)`,
		`"$candidate_started_epoch" -le "$original_started_epoch"`,
		`"$candidate_started_epoch" -lt "$candidate_deploy_started_epoch"`,
		`"$candidate_started_epoch" -gt "$candidate_deploy_completed_epoch"`,
		`candidate_cutover_epoch=$(jq -er '.die_event_first_epoch | tonumber' "$fresh_json")`,
		`"$observation_completed_epoch" -lt "$candidate_cutover_epoch"`,
		`"$observation_completed_epoch" -gt "$candidate_deploy_completed_epoch"`,
		`all(.samples[];`,
		`(.observed_at | fromdateiso8601) >= $cutover_started`,
		`.revision == $original_sha`,
		`.health == "ok"`,
		`.restart_count == 0`,
		`CSX_OBSERVE_DETAIL=1`,
		`CSX_OBSERVE_SINCE=%s`,
		`StrictHostKeyChecking=yes`,
		`UserKnownHostsFile=$RUNNER_TEMP/csx-production-ssh/known_hosts`,
		`post-deploy-supersession-sample.json`,
		`reduce inputs as $line`,
		`.revision == $sha`,
		`.image_revision == $sha`,
		`.served_revision == $sha`,
		`.image_digest == $digest`,
		`.server_started_at == $started`,
		`.migration_version == $migration`,
		`.health == "ok"`,
		`.detail_collected == "true"`,
		`.restart_events == "0"`,
		`.oom_killed == "false"`,
		`.die_events == "1"`,
		`(.die_event_first_epoch | tonumber) == (.die_event_last_epoch | tonumber)`,
		`(.die_event_first_epoch | tonumber) >= $deploy_started`,
		`(.die_event_last_epoch | tonumber) <= $server_started`,
		`($server_started - (.die_event_first_epoch | tonumber)) <= 120`,
		`.conclusion = "superseded"`,
		`.supersessionSample = $fresh[0]`,
		`.supersededBy = {`,
		`workflowRunUrl: $run_url`,
		`cutoverEpoch: $cutover_epoch`,
		"## Post-deploy observation: SUPERSEDED",
		`echo "superseded=true" >> "$GITHUB_OUTPUT"`,
		`.status != "completed"`,
		`sleep 15`,
	} {
		if !strings.Contains(step, required) {
			t.Errorf("validated supersession contract is missing %q", required)
		}
	}
	for _, unsafe := range []string{
		`server OOM detected during observation`,
		`builder did not converge within the bounded 80-minute observation window`,
		`builder completion timestamps are malformed`,
	} {
		if strings.Contains(step, `. != "`+unsafe+`"`) {
			t.Errorf("supersession allowlist can hide safety anomaly %q", unsafe)
		}
	}
	workflow := postDeployObservationWorkflow(t)
	if strings.Index(workflow, "Remove production SSH material") < strings.Index(workflow, "Treat a validated newer deployment as superseding this observation") {
		t.Fatal("production SSH material is removed before the authenticated supersession re-sample")
	}
	if strings.Contains(step, `collector="$GITHUB_WORKSPACE/deploy/lightsail/collect-post-deploy-observation.sh"`) {
		t.Fatal("supersession parser can use the deployed target's older collector schema")
	}
	if strings.Contains(step, `.die_events == "0"`) {
		t.Fatal("supersession can proceed without an authenticated replacement cutover event")
	}
	if strings.Index(step, `candidate_cutover_epoch=$(jq -er`) < strings.Index(step, `.die_events == "1"`) {
		t.Fatal("replacement cutover is read before the fresh sample is authenticated")
	}
}

func TestPostDeployObservationRevalidatesTrackingIssue(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	validation := postDeployObservationStep(t, workflow, "Validate deployment provenance and download its evidence")
	for _, required := range []string{
		`repos/${GITHUB_REPOSITORY}/issues/${issue_number}`,
		`.pull_request | not`,
		`grep -F -q "$target_sha"`,
		`tracking_issue_number=${issue_number}`,
	} {
		if !strings.Contains(validation, required) {
			t.Errorf("tracking issue validation is missing %q", required)
		}
	}
	comment := postDeployObservationStep(t, workflow, "Comment observation on the validated tracking issue")
	for _, required := range []string{
		"if: always() && steps.deployment.outputs.tracking_issue_number != ''",
		"post-deploy-observation.md",
		`repos/${GITHUB_REPOSITORY}/issues/${TRACKING_ISSUE_NUMBER}/comments`,
		`--input "$RUNNER_TEMP/post-deploy-observation-comment.json"`,
	} {
		if !strings.Contains(comment, required) {
			t.Errorf("always-on tracking issue report is missing %q", required)
		}
	}
}

func TestPostDeployObservationUsesProductionIdentityWithoutDeploying(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	for _, required := range []string{
		"environment: codesamplex-production",
		"secrets.CSX_PRODUCTION_SSH_KEY",
		"secrets.CSX_PRODUCTION_KNOWN_HOSTS",
		"vars.CSX_PRODUCTION_HOST",
		"KnownHostsPath",
		"Remove production SSH material",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("production observation credential contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"deploy-production.ps1",
		"./deploy/lightsail/deploy.ps1",
		"CSX_UPDATE_SIGNING_KEY_B64",
		"contents: write",
		"packages: write",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("observation workflow can cross a deploy/publish boundary through %q", forbidden)
		}
	}
}

func TestPostDeployObservationInvokesTheSeparateObserver(t *testing.T) {
	step := postDeployObservationStep(t, postDeployObservationWorkflow(t), "Observe builder convergence and production pressure")
	for _, required := range []string{
		"continue-on-error: true",
		"./deploy/lightsail/observe-production.ps1",
		"-Ip $env:PRODUCTION_HOST",
		"-KeyPath",
		"-KnownHostsPath",
		"-ExpectedRevision $env:TARGET_SHA",
		"-ExpectedPreviousRevision $env:PREVIOUS_SHA",
		`IMAGE_DIGEST: ${{ steps.deployment.outputs.image_digest }}`,
		"-ExpectedImageDigest $env:IMAGE_DIGEST",
		"-ExpectedServerStartedAt $env:SERVER_STARTED_AT",
		"-TrackingIssue $env:TRACKING_ISSUE",
		"-DeploymentRunId $env:DEPLOY_RUN_ID",
		`WORKFLOW_SHA: ${{ github.workflow_sha }}`,
		`OPERATIONAL_SHA: ${{ steps.deployment.outputs.operational_sha }}`,
		"-ObservationControllerSha $env:WORKFLOW_SHA",
		"-DeploymentOperationalSha $env:OPERATIONAL_SHA",
		"-EvidencePath",
		"-SummaryPath",
	} {
		if !strings.Contains(step, required) {
			t.Errorf("observer invocation is missing %q", required)
		}
	}
}

func TestPostDeployObservationAlwaysRetainsEvidenceAndFailsClosed(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	for _, required := range []string{
		"Initialize incident-only observation evidence",
		"post-deploy-observation.json",
		"post-deploy-observation.md",
		"if: always()",
		"actions/upload-artifact@",
		"if-no-files-found: error",
		"retention-days: 30",
		"Report classified observation failure without deploying",
		`OBSERVER_OUTCOME: ${{ steps.observer.outcome }}`,
		`SUPERSEDED: ${{ steps.supersession.outputs.superseded }}`,
		`if [[ "$SUPERSEDED" == "true" && "$conclusion" == "superseded" ]]`,
		`conclusion=$(jq -r '.conclusion // "failure"' "$RUNNER_TEMP/post-deploy-observation.json")`,
		"::error title=Post-deploy observation failed::",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("fail-closed observation evidence contract is missing %q", required)
		}
	}
}

func TestEveryPostDeployObservationActionIsPinnedAndReviewed(t *testing.T) {
	allowed := map[string]bool{
		"actions/checkout":        true,
		"actions/upload-artifact": true,
	}
	uses := regexp.MustCompile(`(?m)^\s*- uses:\s*([^\s#]+)`).FindAllStringSubmatch(postDeployObservationWorkflow(t), -1)
	if len(uses) == 0 {
		t.Fatal("post-deploy observation workflow contains no actions")
	}
	pinned := regexp.MustCompile(`^([^@]+)@[0-9a-f]{40}$`)
	for _, use := range uses {
		ref := pinned.FindStringSubmatch(use[1])
		if ref == nil {
			t.Fatalf("action %q is not pinned to a full commit SHA", use[1])
		}
		if !allowed[ref[1]] {
			t.Fatalf("action %q is outside the reviewed post-deploy set", ref[1])
		}
	}
}

func TestPostDeployObservationDefaultsToIncidentAndNeverAutomatesRollback(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	initial := postDeployObservationStep(t, workflow, "Initialize incident-only observation evidence")
	for _, required := range []string{`classification = "incident-only"`, `rollbackRequested = $false`, `observer failure is not rollback proof`} {
		if !strings.Contains(initial, required) {
			t.Errorf("uncompleted observer does not default to incident-only: missing %q", required)
		}
	}
	for _, required := range []string{"timeout-minutes: 95", "timeout-minutes: 36", "timeout --kill-after=5s 60s gh api", "timeout --kill-after=5s 195s ssh", ".rollbackRequested != true"} {
		if !strings.Contains(workflow, required) {
			t.Errorf("observation deadline / classification guard missing %q", required)
		}
	}
	for _, forbidden := range []string{"actions: write", "gh workflow run", "/dispatches", "rollback.ps1", "docker compose up"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("observer can automatically roll back through %q", forbidden)
		}
	}
}

func TestPostDeployOptionalSourceBaselineIsBoundedAndAuthenticated(t *testing.T) {
	step := postDeployObservationStep(t, postDeployObservationWorkflow(t), "Find an optional authenticated previous source baseline")
	for _, required := range []string{
		"continue-on-error: true", "timeout-minutes: 3", "deadline=$((SECONDS + 120))", "per_page=20",
		"timeout --kill-after=5s 15s gh api", `.repository.full_name == $repo`, `.run_number < $current`,
		`.path == ".github/workflows/production-deploy.yml"`, `.event == "workflow_dispatch"`,
		`.status == "completed" and .conclusion == "success"`, `(.workflowRunId | tostring) == $id`,
		`.targetSha == $sha and .deployedSha == $sha and .servedRevision == $sha`,
		`.health == "ok" and .smoke == "pass" and .rollback == "not-needed"`,
		`.invariants as $counts`, `type == "number" and . >= 0 and floor == .`,
		`.path == ".github/workflows/post-deploy-observation.yml"`, `.extended.identity_before == $identity`,
		`.extended.identity_after == $identity`, `.extended.detail_invariants | fromjson`,
		`"$RUNNER_TEMP/source-baseline.json"`,
	} {
		if !strings.Contains(step, required) {
			t.Errorf("optional baseline trust / deadline check missing %q", required)
		}
	}
}

func TestObserverUsesCanonicalCurrentWorkflowAndAuthenticatedDeployment(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	if got := strings.Count(workflow, `ref: ${{ github.workflow_sha }}`); got != 2 {
		t.Fatalf("validator and observer must both use the immutable executing workflow SHA; got %d checkouts", got)
	}
	proof := postDeployObservationStep(t, workflow, "Prove observer and deployment sources are independently canonical")
	for _, required := range []string{
		`WORKFLOW_SHA: ${{ github.workflow_sha }}`,
		`TARGET_SHA: ${{ steps.deployment.outputs.target_sha }}`,
		`OPERATIONAL_SHA: ${{ steps.deployment.outputs.operational_sha }}`,
		`set -euo pipefail`,
		`test "$(git rev-parse HEAD)" = "$WORKFLOW_SHA"`,
		`timeout --kill-after=5s 120s git fetch --no-tags origin main`,
		`git merge-base --is-ancestor "$WORKFLOW_SHA" origin/main`,
		`git merge-base --is-ancestor "$TARGET_SHA" origin/main`,
		`git merge-base --is-ancestor "$OPERATIONAL_SHA" origin/main`,
		`test -f deploy/lightsail/observe-production.ps1`,
	} {
		if !strings.Contains(proof, required) {
			t.Errorf("independent observer/deployment source proof missing %q", required)
		}
	}
	deployment := postDeployObservationStep(t, workflow, "Validate deployment provenance and download its evidence")
	if !strings.Contains(deployment, `test "$(jq -er '.head_sha' <<<"$run_json")" = "$operational_sha"`) {
		t.Fatal("current observer code must not bypass the authenticated deployment controller identity")
	}
	observer := postDeployObservationStep(t, workflow, "Observe builder convergence and production pressure")
	if !strings.Contains(observer, `-ExpectedMigrationVersion '${{ steps.deployment.outputs.migration_version }}'`) {
		t.Fatal("current observer code must use the deployed payload's authenticated migration")
	}
	for _, forbidden := range []string{
		`ref: ${{ steps.deployment.outputs.target_sha }}`,
		`ref: ${{ steps.deployment.outputs.operational_sha }}`,
		`ref: ${{ inputs.`,
		`ref: ${{ github.ref }}`,
		`ref: main`,
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("observer source must not be stale, mutable or caller-selected through %q", forbidden)
		}
	}
	if strings.Index(workflow, "Prove observer and deployment sources are independently canonical") >
		strings.Index(workflow, "Install the dedicated SSH identity and pinned host key") {
		t.Fatal("canonical observer source must be authenticated before production credentials are installed")
	}
}

func TestPostDeployObservationRetainsDistinctControllerProvenanceOnFailure(t *testing.T) {
	initial := postDeployObservationStep(t, postDeployObservationWorkflow(t), "Initialize incident-only observation evidence")
	for _, required := range []string{
		`WORKFLOW_SHA: ${{ github.workflow_sha }}`,
		`OPERATIONAL_SHA: ${{ steps.deployment.outputs.operational_sha }}`,
		`observationControllerSha = $env:WORKFLOW_SHA`,
		`deploymentOperationalSha = $env:OPERATIONAL_SHA`,
	} {
		if !strings.Contains(initial, required) {
			t.Errorf("failed observer evidence must distinguish current observer and deployment controller: missing %q", required)
		}
	}
}

func TestCanonicalObserverSourceProofAcceptsRepairsAndRejectsUnmergedSources(t *testing.T) {
	bash, err := findCompatibleBash()
	if err != nil {
		t.Skipf("compatible bash unavailable: %v", err)
	}
	step := postDeployObservationStep(t, postDeployObservationWorkflow(t), "Prove observer and deployment sources are independently canonical")
	_, script, ok := strings.Cut(step, "        run: |\n")
	if !ok {
		t.Fatal("canonical source proof has no shell program")
	}
	var program strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "          ") {
			program.WriteString(strings.TrimPrefix(line, "          ") + "\n")
		} else if strings.TrimSpace(line) != "" {
			break
		}
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Observer contract")
	git("config", "user.email", "observer-contract@example.invalid")
	git("config", "commit.gpgSign", "false")
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "lightsail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy", "lightsail", "observe-production.ps1"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "released target")
	target := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "reconciliation controller")
	operational := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "read-only observer repair")
	controller := git("rev-parse", "HEAD")
	git("checkout", "-b", "unmerged", target)
	git("commit", "--allow-empty", "-m", "unmerged source")
	unmerged := git("rev-parse", "HEAD")
	git("remote", "add", "origin", filepath.ToSlash(dir))
	for _, tc := range []struct {
		name, head, controller, operational, target string
		accepted                                    bool
	}{
		{"new canonical observer can observe old deployment", controller, controller, operational, target, true},
		{"checkout must match immutable workflow", operational, controller, operational, target, false},
		{"unmerged observer refused", unmerged, unmerged, operational, target, false},
		{"unmerged deployment controller refused", controller, controller, unmerged, target, false},
		{"unmerged payload refused", controller, controller, operational, unmerged, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git("checkout", "--detach", tc.head)
			cmd := exec.Command(bash, "-c", program.String())
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "WORKFLOW_SHA="+tc.controller, "OPERATIONAL_SHA="+tc.operational, "TARGET_SHA="+tc.target)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.accepted {
				t.Fatalf("source proof acceptance = %v, want %v: %v: %s", err == nil, tc.accepted, err, out)
			}
		})
	}
}

func TestPostDeployObservationScansEveryTrackingIssueComment(t *testing.T) {
	step := postDeployObservationStep(t, postDeployObservationWorkflow(t), "Validate deployment provenance and download its evidence")
	commentsPath := "issues/${issue_number}/comments"
	start := strings.Index(step, commentsPath)
	if start < 0 {
		t.Fatalf("provenance step must read tracking issue comments by issue number, got no %q", commentsPath)
	}
	call := step[start:]
	if end := strings.Index(call, "--jq"); end >= 0 {
		call = call[:end]
	}
	if !strings.Contains(call, "--paginate") {
		t.Fatalf("tracking issue comment scan must paginate like production-deploy.yml; an incident issue outgrows one page and the deployed SHA sits in its newest comments: %q", call)
	}
}
