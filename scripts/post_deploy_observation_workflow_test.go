package main

import (
	"os"
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
		"workflows: [Production deploy]",
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
		`.name == "Production deploy"`,
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

func TestPostDeployObservationOnlySupersedesFromAuthenticatedReplacement(t *testing.T) {
	step := postDeployObservationStep(t, postDeployObservationWorkflow(t), "Treat a validated newer deployment as superseding this observation")
	for _, required := range []string{
		"if: always() && steps.deployment.outputs.deploy_run_number != '' && steps.observer.outcome != 'success'",
		`.run_number > $original`,
		`gh api --paginate --slurp`,
		`.[].workflow_runs[]`,
		`.status == "completed" and .conclusion == "success"`,
		`.name == "Production deploy"`,
		`.path == ".github/workflows/production-deploy.yml"`,
		`.repository.full_name == $repo`,
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
		`"$observation_completed_epoch" -lt "$candidate_deploy_started_epoch"`,
		`"$observation_completed_epoch" -gt "$candidate_deploy_completed_epoch"`,
		`all(.samples[];`,
		`(.observed_at | fromdateiso8601) >= $deploy_started`,
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
		"Initialize fail-closed observation evidence",
		"post-deploy-observation.json",
		"post-deploy-observation.md",
		"if: always()",
		"actions/upload-artifact@",
		"if-no-files-found: error",
		"retention-days: 30",
		"Fail when production did not converge safely",
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
