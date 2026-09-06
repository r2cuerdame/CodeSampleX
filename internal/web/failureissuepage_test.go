package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// seedFailureIssueFixture builds a package whose failure has a boundary: it
// is recorded on 1.3.0, 1.2.0 passed the same stage, and one dependency moved
// between them.
func seedFailureIssueFixture(t *testing.T, f *fakeStore) []failureCluster {
	t.Helper()
	clusters := []failureCluster{{
		Stage: "PROJECT_TEST", Fingerprint: "sha256:aaa11122233344455566677788899900",
		TerminationKind: string(domain.TerminationExit), ExitCode: exitStatus(1),
		ErrorCode: "ERR_ASSERTION", ErrorSummary: "expected 2 arguments, got 1",
		EvidenceQuality: string(domain.EvidenceComplete),
		Count:           9, Versions: []string{"1.3.0"},
		EnvSummary: map[string]string{"os": "linux", "runtime": "node@22"},
		FirstSeen:  "2026-08-01T00:00:00Z", LastSeen: "2026-08-09T00:00:00Z",
	}, {
		// The same exit status, a different cause. It must never merge.
		Stage: "PROJECT_TEST", Fingerprint: "sha256:bbb11122233344455566677788899900",
		TerminationKind: string(domain.TerminationExit), ExitCode: exitStatus(1),
		ErrorCode: "ERR_MODULE_NOT_FOUND", ErrorSummary: "cannot find package left",
		EvidenceQuality: string(domain.EvidenceComplete),
		Count:           2, Versions: []string{"1.3.0"},
		EnvSummary: map[string]string{"os": "linux", "runtime": "node@22"},
	}}

	docs := make([]string, 0, len(clusters))
	for _, c := range clusters {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, string(b))
	}
	f.clusters["npm|libx"] = docs
	f.versions["npm|libx"] = []string{"1.3.0", "1.2.0", "1.1.0"}
	// 1.2.0 passed the stage this failure belongs to; 1.1.0 was never
	// measured at it, so it must stay unmeasured rather than become a PASS.
	f.snapshots[snapKey("pkg:npm/libx@1.2.0", "")] = `{"schemaVersion":1,"purl":"pkg:npm/libx@1.2.0",
	  "rows":[{"contextLabel":"node 22","byStage":{"PROJECT_TEST":{"pass":40,"fail":0}}}]}`
	f.snapshots[snapKey("pkg:npm/libx@1.1.0", "")] = `{"schemaVersion":1,"purl":"pkg:npm/libx@1.1.0",
	  "rows":[{"contextLabel":"node 22","byStage":{"PROJECT_LOAD":{"pass":3,"fail":0}}}]}`
	f.dependencies = []DependencyEdge{
		{ParentName: "libx", ParentVersion: "1.2.0", ChildName: "left", ChildVersion: "1.0.0", Projects: 4},
		{ParentName: "libx", ParentVersion: "1.2.0", ChildName: "right", ChildVersion: "2.0.0", Projects: 4},
		{ParentName: "libx", ParentVersion: "1.3.0", ChildName: "left", ChildVersion: "1.1.0", Projects: 6},
		{ParentName: "libx", ParentVersion: "1.3.0", ChildName: "right", ChildVersion: "2.0.0", Projects: 6},
	}
	return clusters
}

func issueIDFor(t *testing.T, clusters []failureCluster, fingerprint string) string {
	t.Helper()
	for _, issue := range buildFailureIssues(clusters) {
		if issue.Fingerprint == fingerprint {
			return issue.ID
		}
	}
	t.Fatalf("no issue for fingerprint %q", fingerprint)
	return ""
}

// The chain the reader follows is package → fingerprint → issue, so the
// fingerprint they are looking at has to be the thing they can click.
func TestAFailureClusterLinksToItsIssue(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios?f_version=1.12.0").Body.String()
	if !strings.Contains(body, "?issue=") {
		t.Errorf("no failure cluster on the page offers its issue:\n%s", truncate(body))
	}
}

// The question the page exists to answer: where does this failure start.
func TestTheIssuePageNamesTheLastPassAndTheFirstFail(t *testing.T) {
	mux, f := newTestMux(t, nil)
	clusters := seedFailureIssueFixture(t, f)
	id := issueIDFor(t, clusters, "sha256:aaa11122233344455566677788899900")

	body := get(t, mux, "/npm/libx?issue="+id).Body.String()
	mustContain(t, body, `data-kind="starts" data-pass="1.2.0" data-fail="1.3.0"`)
	mustContain(t, body, "expected 2 arguments, got 1")
	// The other cause shares the exit status and must not be folded in.
	mustNotContain(t, body, "cannot find package left")
}

// A dependency that moved across the boundary is a candidate and nothing
// more. Saying so is the difference between this page and a guess.
func TestTheIssuePageMarksAMovedDependencyAsAHypothesis(t *testing.T) {
	mux, f := newTestMux(t, nil)
	clusters := seedFailureIssueFixture(t, f)
	id := issueIDFor(t, clusters, "sha256:aaa11122233344455566677788899900")

	body := get(t, mux, "/npm/libx?issue="+id).Body.String()
	mustContain(t, body, `class="causal" data-basis="hypothesis" data-library="left"`)
	// "right" resolved identically on both sides and is not a candidate.
	mustNotContain(t, body, `data-library="right"`)
}

// An empty edge list can mean either unread or measured-empty. The resolver's
// explicit empty marker makes an added dependency a real comparison rather
// than an evidence gap.
func TestTheIssuePageUsesAProvenEmptyBoundaryTree(t *testing.T) {
	mux, f := newTestMux(t, nil)
	clusters := seedFailureIssueFixture(t, f)
	f.dependencies = f.dependencies[2:] // only the failing release's tree remains
	f.resolvedNone["libx@1.2.0"] = true
	id := issueIDFor(t, clusters, "sha256:aaa11122233344455566677788899900")

	body := get(t, mux, "/npm/libx?issue="+id).Body.String()
	mustContain(t, body, `data-library="left"`)
	mustContain(t, body, `data-library="right"`)
	mustNotContain(t, body, "One side of this boundary has no resolved dependency tree")
}

// A release nothing measured must not be presented as a passing one.
func TestTheIssuePageKeepsAnUnmeasuredReleaseUnmeasured(t *testing.T) {
	mux, f := newTestMux(t, nil)
	clusters := seedFailureIssueFixture(t, f)
	id := issueIDFor(t, clusters, "sha256:aaa11122233344455566677788899900")

	body := get(t, mux, "/npm/libx?issue="+id).Body.String()
	mustContain(t, body, `data-version="1.1.0" data-verdict="unmeasured"`)
	mustContain(t, body, `data-version="1.2.0" data-verdict="pass"`)
	mustContain(t, body, `data-version="1.3.0" data-verdict="fail"`)
}

// An address that names no issue is a 404, not an empty page pretending the
// issue exists.
func TestAnUnknownIssueIsNotFound(t *testing.T) {
	mux, f := newTestMux(t, nil)
	seedFailureIssueFixture(t, f)
	if rec := get(t, mux, "/npm/libx?issue=deadbeefdeadbeef"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// The package page is intentionally capped, but an issue address is durable.
// Resolving it through the display page made a valid URL turn into a 404 as
// higher-ranked clusters pushed its row past the cap.
func TestIssueLookupUsesTheCompleteClusterLedger(t *testing.T) {
	mux, f := newTestMux(t, nil)
	clusters := seedFailureIssueFixture(t, f)
	key := "npm|libx"
	complete := append([]string(nil), f.clusters[key]...)
	// Simulate the display page returning only the other failure while the
	// explicit issue read still has the complete package ledger.
	f.clusters[key] = complete[1:]
	f.issueClusters[key] = complete
	id := issueIDFor(t, clusters, "sha256:aaa11122233344455566677788899900")

	rec := get(t, mux, "/npm/libx?issue="+id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the issue beyond the display cap to resolve", rec.Code)
	}
	mustContain(t, rec.Body.String(), "expected 2 arguments, got 1")
}

// The issue view is a view OF the package page, so it declares the package
// page as its canonical rather than adding a second indexable address for the
// same coordinate.
func TestTheIssuePageCanonicalIsThePackagePage(t *testing.T) {
	mux, f := newTestMux(t, nil)
	clusters := seedFailureIssueFixture(t, f)
	id := issueIDFor(t, clusters, "sha256:aaa11122233344455566677788899900")

	body := get(t, mux, "/npm/libx?issue="+id).Body.String()
	mustContain(t, body, `id="issue-signature"`)
	mustContain(t, body, `rel="canonical" href="https://codesamplex.dev/npm/libx"`)
}

// A cluster recorded against no release cannot be placed on the release axis.
// Drawing the axis anyway would show every release as PASS or unmeasured with
// the failure nowhere on it, which reads as the opposite of what is known.
func TestAnIssueWithNoReleaseDrawsNoReleaseAxis(t *testing.T) {
	mux, f := newTestMux(t, nil)
	clusters := []failureCluster{{
		Stage: "PROJECT_TEST", Fingerprint: "sha256:ccc11122233344455566677788899900",
		TerminationKind: string(domain.TerminationExit), ExitCode: exitStatus(1),
		EvidenceQuality: string(domain.EvidenceComplete), Count: 3,
		EnvSummary: map[string]string{"os": "linux"},
	}}
	doc, err := json.Marshal(clusters[0])
	if err != nil {
		t.Fatal(err)
	}
	f.clusters["npm|liby"] = []string{string(doc)}
	f.versions["npm|liby"] = []string{"2.0.0", "1.0.0"}

	id := issueIDFor(t, clusters, "sha256:ccc11122233344455566677788899900")
	body := get(t, mux, "/npm/liby?issue="+id).Body.String()
	mustNotContain(t, body, `class="issue-release"`)
	mustContain(t, body, `id="issue-gaps"`)
}
