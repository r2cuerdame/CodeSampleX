package web

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func exitStatus(n int) *int { return &n }

// issueCluster is the shape the aggregator reads: a modern, fingerprinted
// failure recorded against one release in one environment.
func issueCluster(fp, version, env string, count int64) failureCluster {
	return failureCluster{
		Stage: "PROJECT_TEST", Fingerprint: fp,
		TerminationKind: string(domain.TerminationExit), ExitCode: exitStatus(1),
		EvidenceQuality: string(domain.EvidenceComplete),
		Count:           count, Versions: []string{version},
		EnvSummary: map[string]string{"os": env},
	}
}

// Exit 1 is what almost every broken build reports. Bucketing by it is the
// thing this view exists to replace: a reader who opens "exit 1" learns
// nothing, because a missing module and a failing assertion are both exit 1.
//
// The identity is the normalized fingerprint, which carries the stage, the
// failing toolchain, the error code and the normalized error text. Two
// different causes therefore stay two issues however the process ended.
func TestTwoCausesSharingAnExitCodeStayTwoIssues(t *testing.T) {
	got := buildFailureIssues([]failureCluster{
		issueCluster("sha256:aaa", "1.12.0", "linux", 4),
		issueCluster("sha256:bbb", "1.12.0", "linux", 9),
	})
	if len(got) != 2 {
		t.Fatalf("issues = %d, want one per cause", len(got))
	}
	if got[0].ID == got[1].ID {
		t.Errorf("two causes share the address %q", got[0].ID)
	}
}

// A cluster is a fact about one release in one environment. An ISSUE is the
// question a reader arrives with — where does this failure start and stop —
// so it spans the releases and the environments the same cause reproduced in.
func TestOneIssueSpansItsReleasesAndEnvironments(t *testing.T) {
	got := buildFailureIssues([]failureCluster{
		issueCluster("sha256:aaa", "1.11.0", "linux", 4),
		issueCluster("sha256:aaa", "1.12.0", "windows", 9),
	})
	if len(got) != 1 {
		t.Fatalf("issues = %d, want the one cause both describe", len(got))
	}
	issue := got[0]
	if strings.Join(issue.Versions, ",") != "1.12.0,1.11.0" {
		t.Errorf("versions = %v, want both releases newest first", issue.Versions)
	}
	if len(issue.Environments) != 2 {
		t.Errorf("environments = %+v, want both places it reproduced", issue.Environments)
	}
	if issue.Count != 13 {
		t.Errorf("count = %d, want the two environments added", issue.Count)
	}
}

// The recorder files one observation against the package AND one against
// every symbol it detected, so one broken build arrives twice. The
// package-level count already contains the symbol's, so an environment keeps
// the larger of them and never their sum.
func TestOneFailureSeenAtTwoGrainsIsCountedOnce(t *testing.T) {
	pkgLevel := issueCluster("sha256:aaa", "1.12.0", "linux", 181)
	symLevel := issueCluster("sha256:aaa", "1.12.0", "linux", 181)
	symLevel.Symbol = "axios.post"
	got := buildFailureIssues([]failureCluster{pkgLevel, symLevel})
	if len(got) != 1 {
		t.Fatalf("issues = %d, want one", len(got))
	}
	if got[0].Count != 181 {
		t.Errorf("count = %d, want 181 and never their sum", got[0].Count)
	}
	if strings.Join(got[0].Symbols, ",") != "axios.post" {
		t.Errorf("symbols = %v, want the symbol the package-level row could not name", got[0].Symbols)
	}
}

// A failure whose evidence was never preserved has no established cause. Its
// stored hash is provenance, not an identity, and folding it into a
// fingerprinted issue would attach real failures to a cause nothing proved.
func TestAnUnprovenFailureNeverJoinsAProvenIssue(t *testing.T) {
	proven := issueCluster("sha256:aaa", "1.12.0", "linux", 4)
	gap := issueCluster("sha256:aaa", "1.12.0", "linux", 6)
	gap.EvidenceQuality = string(domain.EvidenceLegacyIncomplete)

	got := buildFailureIssues([]failureCluster{proven, gap})
	if len(got) != 2 {
		t.Fatalf("issues = %d, want the proven cause kept apart from the gap", len(got))
	}
	var gaps int
	for _, issue := range got {
		if issue.EvidenceGap {
			gaps++
			if issue.Fingerprint != "" {
				t.Errorf("an unproven issue advertises fingerprint %q as its cause", issue.Fingerprint)
			}
		}
	}
	if gaps != 1 {
		t.Errorf("evidence-gap issues = %d, want exactly the one that lost its evidence", gaps)
	}
}

// The verdict is about ONE issue at the stage it belongs to, never about the
// release in general: a release with a passing observation at that stage and
// no record of this issue is the nearest thing to a PASS this network can
// honestly report, and a release nothing measured stays unmeasured.
func TestAReleaseNothingMeasuredIsNotAPass(t *testing.T) {
	issue := buildFailureIssues([]failureCluster{
		issueCluster("sha256:aaa", "1.12.0", "linux", 4),
	})[0]
	verdicts := failureIssueVerdicts(issue,
		[]string{"1.12.0", "1.11.0", "1.10.0"},
		map[string]int64{"1.11.0": 40})

	if verdicts["1.12.0"] != issueVerdictFail {
		t.Errorf("1.12.0 = %q, want FAIL: the issue was recorded there", verdicts["1.12.0"])
	}
	if verdicts["1.11.0"] != issueVerdictPass {
		t.Errorf("1.11.0 = %q, want PASS", verdicts["1.11.0"])
	}
	if verdicts["1.10.0"] != issueVerdictUnmeasured {
		t.Errorf("1.10.0 = %q, want unmeasured rather than a manufactured PASS", verdicts["1.10.0"])
	}
}

// The boundary is the pair of adjacent DECIDED releases the verdict changes
// across: the last release that passed, and the first that carries the issue.
func TestTheBoundaryNamesTheLastPassAndTheFirstFail(t *testing.T) {
	got := failureIssueBoundaries([]string{"1.12.0", "1.11.0"}, map[string]failureIssueVerdict{
		"1.11.0": issueVerdictPass, "1.12.0": issueVerdictFail,
	})
	if len(got) != 1 {
		t.Fatalf("boundaries = %d, want the one the verdict changes across", len(got))
	}
	b := got[0]
	if b.Kind != boundaryStarts || b.PassVersion != "1.11.0" || b.FailVersion != "1.12.0" {
		t.Errorf("boundary = %+v, want the failure starting at 1.12.0", b)
	}
}

// A release nobody measured must not close the gap between a PASS and a FAIL.
// The boundary is still the nearest KNOWN one, and it says how many releases
// in between nothing can speak for — otherwise "1.9.0 → 1.12.0" reads as
// though the two releases were next to each other.
func TestABoundaryCountsTheReleasesNothingMeasured(t *testing.T) {
	got := failureIssueBoundaries(
		[]string{"1.12.0", "1.11.0", "1.10.0", "1.9.0"},
		map[string]failureIssueVerdict{"1.9.0": issueVerdictPass, "1.12.0": issueVerdictFail})
	if len(got) != 1 {
		t.Fatalf("boundaries = %d, want the nearest known one", len(got))
	}
	if got[0].UnmeasuredBetween != 2 {
		t.Errorf("unmeasured between = %d, want the two releases nothing measured",
			got[0].UnmeasuredBetween)
	}
}

// A failure recorded on every release this network measured has no boundary
// at all, and inventing one would put a start date on a fact that has none.
func TestNoPassAnywhereProducesNoBoundary(t *testing.T) {
	got := failureIssueBoundaries([]string{"1.12.0", "1.11.0"}, map[string]failureIssueVerdict{
		"1.11.0": issueVerdictFail, "1.12.0": issueVerdictFail,
	})
	if len(got) != 0 {
		t.Errorf("boundaries = %+v, want none: nothing here ever passed", got)
	}
}

// A dependency that moved across the boundary is a CANDIDATE. Nothing about a
// version having changed proves it caused anything, so the edge is labelled
// hypothesis until a receipt says otherwise.
func TestAMovedDependencyIsOnlyAHypothesis(t *testing.T) {
	edges := []DependencyEdge{
		{ParentVersion: "1.11.0", ChildName: "follow-redirects", ChildVersion: "1.15.0"},
		{ParentVersion: "1.12.0", ChildName: "follow-redirects", ChildVersion: "1.16.0"},
		{ParentVersion: "1.11.0", ChildName: "form-data", ChildVersion: "4.0.0"},
		{ParentVersion: "1.12.0", ChildName: "form-data", ChildVersion: "4.0.0"},
	}
	got := failureIssueCausalEdges("npm", edges, "1.11.0", "1.12.0")
	if len(got) != 1 {
		t.Fatalf("edges = %+v, want only the child that moved", got)
	}
	if got[0].Library != "follow-redirects" || got[0].Basis != causalHypothesis {
		t.Errorf("edge = %+v, want follow-redirects as a hypothesis", got[0])
	}
}

// The same receipt that recorded the failure also recorded the tree it
// resolved. That is the one thing here which is evidence rather than
// correlation, and it has to read differently.
func TestASameReceiptFailureIsEvidence(t *testing.T) {
	edges := []DependencyEdge{
		{ParentVersion: "1.11.0", ChildName: "follow-redirects", ChildVersion: "1.15.0"},
		{ParentVersion: "1.12.0", ChildName: "follow-redirects", ChildVersion: "1.16.0",
			SameReceipt: true, Outcome: "fail"},
	}
	got := failureIssueCausalEdges("npm", edges, "1.11.0", "1.12.0")
	if len(got) != 1 || got[0].Basis != causalEvidence {
		t.Fatalf("edges = %+v, want the same-receipt proof marked as evidence", got)
	}
}

// A same-receipt PASS on the failing release rules the child OUT rather than
// in, and must never be dressed up as proof it caused the failure.
func TestASameReceiptPassIsNotCausalEvidence(t *testing.T) {
	edges := []DependencyEdge{
		{ParentVersion: "1.11.0", ChildName: "follow-redirects", ChildVersion: "1.15.0"},
		{ParentVersion: "1.12.0", ChildName: "follow-redirects", ChildVersion: "1.16.0",
			SameReceipt: true, Outcome: "pass"},
	}
	got := failureIssueCausalEdges("npm", edges, "1.11.0", "1.12.0")
	if len(got) != 1 || got[0].Basis != causalHypothesis {
		t.Fatalf("edges = %+v, want a passing combination left as a hypothesis", got)
	}
}

// The matrix a reader needs is the one AROUND the boundary, and a package
// with sixty releases cannot render them all. The window keeps the affected
// releases and their neighbours, which is where a boundary can be.
func TestTheVersionWindowKeepsTheNeighboursOfTheAffectedReleases(t *testing.T) {
	all := []string{"2.0.0", "1.9.0", "1.8.0", "1.7.0", "1.6.0", "1.5.0"}
	got := failureIssueVersionWindow(all, []string{"1.7.0"}, 1, 6)
	if strings.Join(got, ",") != "1.8.0,1.7.0,1.6.0" {
		t.Errorf("window = %v, want the affected release and one neighbour each side", got)
	}
}

func TestTheVersionWindowIsBounded(t *testing.T) {
	all := []string{"1.6.0", "1.5.0", "1.4.0", "1.3.0", "1.2.0", "1.1.0"}
	if got := failureIssueVersionWindow(all, []string{"1.4.0"}, 4, 3); len(got) != 3 {
		t.Errorf("window = %v, want it capped at 3", got)
	}
}

// A golang module is published as both "1.6.0" and "v1.6.0" and only one
// spelling carries a version row, so a release a failure was RECORDED on can
// be absent from the package's version list. Dropping it would take the FAIL
// out of a page whose whole subject is that failure.
func TestTheWindowKeepsAnAffectedReleaseTheVersionListLacks(t *testing.T) {
	got := failureIssueVersionWindow([]string{"1.9.0", "1.8.0"}, []string{"1.7.0"}, 1, 6)
	if !contains(got, "1.7.0") {
		t.Errorf("window = %v, want the release the failure was recorded on", got)
	}
	if !contains(got, "1.8.0") {
		t.Errorf("window = %v, want its neighbour from the version list", got)
	}
}
