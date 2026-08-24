package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The GPTBrowser regression, as the fixture it was asked to be.
//
// A real session: `npm run typecheck` failed, CSX put an unrelated Dart
// crypto recommendation in front of the failure, and the developer could not
// see their own stderr. That is not a fact about a package — no container can
// settle it — it is a defect in this product, and it is the shape this
// channel exists to catch.
func gptBrowserIssue() domain.CSXIssueReport {
	return domain.CSXIssueReport{
		SchemaVersion:      1,
		AffectedSurface:    domain.CSXSurfaceServer,
		IssueKind:          domain.CSXIssueAnswerMasksFailure,
		Component:          "/v2/search",
		RequestFingerprint: "b7c1f0a94e2d5836",
		ActualBehavior: "the npm/TypeScript typecheck failure was displaced by a Dart crypto sample " +
			"promoted as a recommendation, and the original stderr was not shown first",
		ExpectedBehavior: "the local failure is primary evidence and is shown first; an unrelated-ecosystem " +
			"result is labelled a reference candidate rather than promoted",
		Reproducible:  "yes",
		Confidence:    "high",
		LLMHypothesis: "the ranker probably ignores ecosystem when the error fingerprint matches",
	}
}

func csxIssueEnvelopeFor(report domain.CSXIssueReport) csxIssueEnvelope {
	return csxIssueEnvelope{SchemaVersion: 1, Epoch: "2026-08-24", AnonID: "anon-bucket-1", Report: report}
}

func TestTheGPTBrowserDefectIsAcceptedDedupedAndLinkedToItsCanonicalBug(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	ctx := context.Background()

	var first csxIssueResponse
	resp := postJSON(t, srv.URL+"/v1/csx-issues", csxIssueEnvelopeFor(gptBrowserIssue()), &first)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if first.Status != "accepted" || first.ReportID == 0 {
		t.Fatalf("response = %+v", first)
	}
	// No ticket was created, and the wording has to stop an agent from
	// saying one was. It must state both halves: that this is a candidate,
	// and that the agent must not report it as filed.
	if !strings.Contains(first.Note, "NOT A BUG") ||
		!strings.Contains(strings.ToLower(first.Note), "no ticket was created") ||
		!strings.Contains(strings.ToLower(first.Note), "do not tell the user") {
		t.Fatalf("the response does not prevent an agent claiming a bug was filed: %q", first.Note)
	}
	if first.CanonicalRef != "" {
		t.Fatal("a fresh report must not claim a canonical bug")
	}
	// A search defect cannot be replayed here, and the reason must say why
	// rather than leaving it looking queued.
	if first.ReportStatus != domain.CSXIssueStatusNoReplayLane {
		t.Fatalf("reportStatus = %q", first.ReportStatus)
	}
	if !strings.Contains(first.ReplayReason, "question") {
		t.Fatalf("the reason does not explain what cannot be re-run: %q", first.ReplayReason)
	}

	// An operator triages it: confirmed, and linked to the bug that already
	// tracks it.
	if ok, err := store.SetCSXIssueVerdict(ctx, first.ReportID, domain.CSXIssueVerdictDefect, testNow); err != nil || !ok {
		t.Fatalf("verdict ok=%v err=%v", ok, err)
	}
	if ok, err := store.LinkCSXIssueCanonical(ctx, first.ReportID, "R2C-51"); err != nil || !ok {
		t.Fatalf("link ok=%v err=%v", ok, err)
	}

	// The same defect, met by another agent, worded differently.
	second := gptBrowserIssue()
	second.ActualBehavior = "CSX answered a TypeScript build failure with a Dart package"
	second.LLMHypothesis = "maybe the ecosystem filter is off"
	envelope := csxIssueEnvelopeFor(second)
	envelope.AnonID = "anon-bucket-2"

	var out csxIssueResponse
	postJSON(t, srv.URL+"/v1/csx-issues", envelope, &out)
	if out.Status != "duplicate" || out.MatchedReportID != first.ReportID {
		t.Fatalf("second report = %+v", out)
	}
	if out.Occurrences != 2 {
		t.Fatalf("occurrences = %d, want 2", out.Occurrences)
	}
	if out.CanonicalRef != "R2C-51" {
		t.Fatalf("a repeat reporter was not told the issue is already tracked: %+v", out)
	}
	rows, err := store.ListCSXIssueReports(ctx, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("one defect became %d rows: %v", len(rows), err)
	}
}

// A candidate is not a claim. Linking an unconfirmed report to a bug is how
// the first quietly becomes the second.
func TestAnUnconfirmedReportCannotBeLinkedToABug(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	ctx := context.Background()

	var out csxIssueResponse
	postJSON(t, srv.URL+"/v1/csx-issues", csxIssueEnvelopeFor(gptBrowserIssue()), &out)

	linked, err := store.LinkCSXIssueCanonical(ctx, out.ReportID, "R2C-51")
	if err != nil {
		t.Fatal(err)
	}
	if linked {
		t.Fatal("a report with no verdict was linked to a canonical bug")
	}
	row, _, _ := store.CSXIssueReportByID(ctx, out.ReportID)
	if row.CanonicalRef != "" {
		t.Fatalf("canonicalRef = %q", row.CanonicalRef)
	}
}

func TestAnIssueReportWithNothingReCheckableIsRefused(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)

	report := gptBrowserIssue()
	report.RequestFingerprint = ""
	report.PublicInput = nil

	var body map[string]string
	resp := postJSON(t, srv.URL+"/v1/csx-issues", csxIssueEnvelopeFor(report), &body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body["error"], "re-checkable") {
		t.Fatalf("the refusal must say what is missing: %q", body["error"])
	}
	rows, _ := store.ListCSXIssueReports(context.Background(), 10)
	if len(rows) != 0 {
		t.Fatalf("a refused report was stored: %+v", rows)
	}
}

// A report where the two behaviours agree is describing something working.
func TestAnIssueReportThatDescribesNoDifferenceIsRefused(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	report := gptBrowserIssue()
	report.ExpectedBehavior = report.ActualBehavior
	var body map[string]string
	if resp := postJSON(t, srv.URL+"/v1/csx-issues", csxIssueEnvelopeFor(report), &body); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// Product defects and compatibility anomalies must not share a table, a
// verdict vocabulary or a queue: an anomaly can be promoted to evidence and a
// defect in this product never can.
func TestProductDefectsAndCompatibilityAnomaliesStayApart(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "c9")
	ctx := context.Background()

	postJSON(t, srv.URL+"/v1/csx-issues", csxIssueEnvelopeFor(gptBrowserIssue()), &csxIssueResponse{})
	var anomaly anomalyResponse
	postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(mismatchAgainst(sampleID)), &anomaly)

	issues, _ := store.ListCSXIssueReports(ctx, 10)
	anomalies, _ := store.ListAnomalyReports(ctx, 10)
	if len(issues) != 1 || len(anomalies) != 1 {
		t.Fatalf("issues=%d anomalies=%d; the two channels are sharing storage", len(issues), len(anomalies))
	}
	// The defect verdict vocabulary must not be promotable through the
	// anomaly gate, and vice versa.
	if domain.AnomalyVerdictConfirmed(domain.CSXIssueVerdictExpectedBehavior) {
		t.Fatal("a product-defect verdict passed the anomaly promotion gate")
	}
	if domain.CSXIssueVerdictConfirmed(domain.AnomalyVerdictCompatibilityBoundary) {
		t.Fatal("a compatibility verdict passed the repair-queue gate")
	}
}

// The reporter's guess is stored and shown, and decides nothing here either.
func TestTheIssueHypothesisStaysOutOfTheFingerprint(t *testing.T) {
	a := gptBrowserIssue()
	a.LLMHypothesis = "the ranker ignores ecosystem"
	b := gptBrowserIssue()
	b.LLMHypothesis = "the error fingerprint weight is too high"
	b.Confidence = "low"
	b.ActualBehavior = "a Dart result was promoted over the TypeScript failure"
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("two wordings of one defect produced two fingerprints, so one bug becomes a pile of tickets")
	}
	c := gptBrowserIssue()
	c.IssueKind = domain.CSXIssueRuntimeBehavior
	if a.Fingerprint() == c.Fingerprint() {
		t.Fatal("a different defect shape on the same endpoint must be a different row")
	}
	_ = serverstore.CSXIssueReportRow{}
}
