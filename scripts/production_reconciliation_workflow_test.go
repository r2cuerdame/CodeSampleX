package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionReconciliationIsReadOnlyAndCanonicallyGated(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "production-reconcile.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for _, required := range []string{
		"name: Production reconciliation", "workflow_dispatch:", "deploy_run_id:", "owner:",
		"group: codesamplex-production", "cancel-in-progress: false", "environment: codesamplex-production",
		`test "$GITHUB_REF" = refs/heads/main`, "ref: ${{ github.sha }}",
		"head_sha=${GITHUB_SHA}&branch=main&status=success", `git merge-base --is-ancestor "$sha" origin/main`,
		"python3 deploy/lightsail/reconciliation-provenance.py", "python3 deploy/lightsail/reconcile-production.py",
		"--expected reconciliation-original/expected.json", "--provenance reconciliation-original/provenance.json",
		"--known-hosts", "name: production-evidence-${{ github.run_id }}", "retention-days: 30",
		"Remove production SSH material", "if: always()", "Original workflow remains failed.",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("reconciliation workflow lost %q", required)
		}
	}
	for _, forbidden := range []string{
		"  push:", "pull_request:", "schedule:", "issues: write", "contents: write",
		"deploy-production.ps1", "./deploy.ps1", "docker compose up", "docker restart",
		"aws lightsail", "--insecure", "StrictHostKeyChecking=no", "continue-on-error:",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("read-only reconciliation includes %q", forbidden)
		}
	}
	provenance := strings.Index(workflow, "python3 deploy/lightsail/reconciliation-provenance.py")
	identity := strings.Index(workflow, "Install the dedicated SSH identity")
	verify := strings.Index(workflow, "python3 deploy/lightsail/reconcile-production.py")
	if provenance >= identity || identity >= verify {
		t.Fatal("authenticate original GitHub evidence before giving the collector SSH access")
	}
}

func TestObserverReconciliationKeepsOriginalDeploymentOrderingAndChain(t *testing.T) {
	workflow := postDeployObservationWorkflow(t)
	step := postDeployObservationStep(t, workflow, "Validate deployment provenance and download its evidence")
	for _, required := range []string{
		`.path == ".github/workflows/production-reconcile.yml"`, `.conclusion == "success"`,
		`.head_branch == "main"`, `.head_repository.full_name == $repo`,
		".reconciliation.originalDeploymentRunId", ".reconciliation.owner",
		"--reconciled-evidence", "--reconciliation-run", "reconciliation-provenance.py",
		".originalDeploymentRunNumber", `echo "deploy_run_number=${deploy_run_number}"`,
	} {
		if !strings.Contains(step, required) {
			t.Errorf("observer reconciliation chain lost %q", required)
		}
	}
	if strings.Contains(step, `.conclusion == "failure"`) {
		t.Fatal("observer must not admit the failed original run as successful deployment provenance")
	}
	if strings.Index(step, "--reconciled-evidence") >= strings.Index(step, `echo "deploy_run_number=`) {
		t.Fatal("observer outputs must follow validation of the original acceptance chain")
	}
}
