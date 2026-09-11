package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionReconciliationRequiresPublishedPreparationBeforeRelease(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "production-reconciliation.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := strings.ReplaceAll(string(raw), "\r\n", "\n")
	previous := -1
	for _, step := range []string{
		"Require canonical operational source",
		"Require successful canonical operational CI",
		"Authenticate original failed run and retained host acceptance",
		"Install the dedicated SSH identity and pinned host key",
		"Verify exact owner and live acceptance without restart",
		"Durably publish verified preparation before owner release",
		"Revalidate and archive only the proven committed owner",
		"Retain final reconciliation evidence",
	} {
		index := strings.Index(workflow, "name: "+step)
		if index <= previous {
			t.Fatalf("reconciliation stage %q is missing or incorrectly ordered", step)
		}
		previous = index
	}
	for _, required := range []string{
		"workflow_dispatch:", "source_run_id:", "source_run_attempt:", "group: codesamplex-production", "cancel-in-progress: false",
		"SOURCE_RUN_ATTEMPT: ${{ inputs.source_run_attempt }}", `--run-attempt "$SOURCE_RUN_ATTEMPT"`,
		"environment: codesamplex-production", "ref: ${{ github.sha }}", "timeout-minutes: 20",
		"production-reconciliation-prepared-${{ github.run_id }}-${{ github.run_attempt }}",
		"production-evidence-${{ github.run_id }}-${{ github.run_attempt }}",
		"PREPARED_ARTIFACT_ID: ${{ steps.prepared.outputs.artifact-id }}",
		"PREPARED_ARTIFACT_DIGEST: ${{ steps.prepared.outputs.artifact-digest }}",
		"--prepared-artifact-id", "--prepared-artifact-digest", "if-no-files-found: error",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("missing reconciliation safety contract %q", required)
		}
	}
	for _, forbidden := range []string{"continue-on-error:", "overwrite: true", "  push:", "  pull_request:", "  schedule:",
		"deploy-production.ps1", "offline-migration.py", "docker compose up", "workflow_run:"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("reconciliation unexpectedly allows %q", forbidden)
		}
	}
	for _, step := range []string{"Remove production SSH material", "Retain final reconciliation evidence"} {
		if !strings.Contains(postDeployObservationStep(t, workflow, step), "if: always()") {
			t.Errorf("%s must run after failures", step)
		}
	}
	for _, step := range []string{"Durably publish verified preparation before owner release", "Revalidate and archive only the proven committed owner"} {
		if strings.Contains(postDeployObservationStep(t, workflow, step), "if: always()") {
			t.Errorf("%s must never run after failed acceptance", step)
		}
	}
}

func TestReconciledObservationPreservesAuthenticatedOriginalOrdering(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	step := postDeployObservationStep(t, workflow, "Validate deployment provenance and download its evidence")
	for _, required := range []string{
		`.path == ".github/workflows/production-reconciliation.yml"`,
		`.status == "completed" and .head_branch == "main"`,
		`.conclusion == "success"`, `.head_repository.full_name == $repo`,
		`artifact_name="${artifact_name}-${run_attempt}"`,
		`deploy_run_number=$(python3 -B .reconciliation-validator/deploy/lightsail/reconciliation-provenance.py`,
		`download-observation --run-id "$DEPLOY_RUN_ID" --output "$deploy_evidence"`,
	} {
		if !strings.Contains(step, required) {
			t.Errorf("missing reconciled observer provenance contract %q", required)
		}
	}
	if !strings.Contains(workflow, "ref: ${{ github.workflow_sha }}") ||
		!strings.Contains(workflow, `git -C .reconciliation-validator merge-base --is-ancestor "$WORKFLOW_SHA" origin/main`) {
		t.Fatal("reconciliation validator must come from canonical workflow source")
	}
}
