// The merge gate has two halves and only one of them is in this repository.
// `ci.yml` runs the PostgreSQL suite; a GitHub branch ruleset is what makes a
// red run stop a merge, and it names the required check by the string the job
// publishes as its `name:`. Rename that job and nothing breaks loudly — the
// ruleset keeps waiting for a `Test` context that no longer arrives, so every
// pull request hangs on a check that will never report.
//
// These tests are the loud part. They cannot read GitHub settings, so they
// hold the two things they can reach — the job name and the paragraph in
// docs/operations.md that tells an operator to update the ruleset with it — to
// each other, and fail the rename until the operator has read that paragraph.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// requiredCheckContext is the status check the `Protect main` ruleset requires.
// Changing this constant is changing a GitHub setting; the tests below say so.
const requiredCheckContext = "Test"

// mainRulesetID identifies the ruleset carrying that requirement, so the doc
// and the readback command point at the same object.
const mainRulesetID = "21240909"

func ciWorkflow(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci workflow: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

func operationsDoc(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "docs", "operations.md"))
	if err != nil {
		t.Fatalf("read operations doc: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// jobDisplayNames maps each top-level job key to the `name:` it publishes,
// falling back to the key when the job declares none — which is what GitHub
// itself does when it labels the check.
func jobDisplayNames(t *testing.T, workflow string) map[string]string {
	t.Helper()
	nameLine := regexp.MustCompile(`^    name:\s*(.+?)\s*$`)
	names := map[string]string{}
	lines := strings.Split(workflow, "\n")
	start := -1
	for i, line := range lines {
		if line == "jobs:" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatal("ci workflow declares no jobs:")
	}
	job := ""
	for _, line := range lines[start:] {
		if m := jobHeader.FindStringSubmatch(line); m != nil {
			job = m[1]
			names[job] = job
			continue
		}
		if job == "" {
			continue
		}
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			break // back out to a top-level key: jobs: is over
		}
		if m := nameLine.FindStringSubmatch(line); m != nil && names[job] == job {
			names[job] = strings.Trim(m[1], `"'`)
		}
	}
	return names
}

// The one binding fact: some job in ci.yml has to publish the exact context the
// ruleset requires, or the required check never reports at all.
func TestAJobStillPublishesTheRequiredCheckName(t *testing.T) {
	names := jobDisplayNames(t, ciWorkflow(t))
	for job, name := range names {
		if name == requiredCheckContext {
			t.Logf("job %q publishes the required check %q", job, name)
			return
		}
	}
	t.Fatalf("no job in ci.yml publishes the required status check %q; jobs publish %v.\n"+
		"The `Protect main` ruleset (id %s) requires that exact context, so every pull "+
		"request will now wait on a check that never reports. Rename the job back, or "+
		"update requiredCheckContext here, docs/operations.md and the ruleset together.",
		requiredCheckContext, names, mainRulesetID)
}

// The merge gate is only a gate on the branch the release is cut from, and only
// while the pull request event still reaches it.
func TestTheMergeGateStillCoversPullRequestsAndMain(t *testing.T) {
	workflow := ciWorkflow(t)
	on := workflow
	if i := strings.Index(workflow, "\njobs:"); i > 0 {
		on = workflow[:i]
	}
	if !strings.Contains(on, "pull_request:") {
		t.Fatal("ci.yml no longer runs on pull_request; the required check would never be produced for a merge")
	}
	if !strings.Contains(on, "branches: [main]") {
		t.Fatal("ci.yml no longer runs on pushes to main; nothing would record what was merged")
	}
	if !strings.Contains(on, "workflow_dispatch:") {
		t.Fatal("ci.yml has no authenticated manual retrigger when GitHub drops a push event")
	}
}

// The half of the gate that lives in GitHub settings cannot be asserted from
// here, so what is asserted is that an operator renaming the job is told where
// the other half is and how to read it back.
func TestOperationsDocumentsTheRulesetTheRenameWouldBreak(t *testing.T) {
	doc := operationsDoc(t)
	for _, want := range []string{
		mainRulesetID,
		"repos/r2cuerdame/CodeSampleX/rules/branches/main",
		"`" + requiredCheckContext + "` status check",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("docs/operations.md no longer mentions %q, so the merge gate's "+
				"GitHub-side half is undocumented and a job rename has nothing to warn the operator with", want)
		}
	}
	if !strings.Contains(doc, "renaming the `"+requiredCheckContext+"` job") {
		t.Fatalf("docs/operations.md no longer states that renaming the %q job requires updating ruleset %s",
			requiredCheckContext, mainRulesetID)
	}
}
