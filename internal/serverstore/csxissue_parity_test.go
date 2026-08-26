package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func csxIssueRow(fingerprint string) CSXIssueReportRow {
	return CSXIssueReportRow{
		Fingerprint:    fingerprint,
		ReportJSON:     `{"schemaVersion":1,"issueKind":"answer-masks-original-failure"}`,
		Surface:        domain.CSXSurfaceServer,
		IssueKind:      domain.CSXIssueAnswerMasksFailure,
		Component:      "/v2/search",
		ReporterBucket: "anon-2026-08-24-a",
	}
}

func runCSXIssueStoreContract(t *testing.T, store CSXIssueStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	first, duplicate, err := store.RecordCSXIssueReport(ctx, csxIssueRow("issue-one"), now)
	if err != nil || duplicate || first.ID == 0 || first.Occurrences != 1 {
		t.Fatalf("first = %+v duplicate=%v err=%v", first, duplicate, err)
	}
	if first.Status != domain.CSXIssueStatusTriage {
		t.Fatalf("status = %q, want triage", first.Status)
	}

	// A candidate is not a claim: nothing may be linked to a bug before a
	// verdict says it is one.
	linked, err := store.LinkCSXIssueCanonical(ctx, first.ID, "R2C-51")
	if err != nil {
		t.Fatal(err)
	}
	if linked {
		t.Fatal("an unconfirmed report was linked to a canonical bug")
	}

	if err := store.SetCSXIssueTriage(ctx, first.ID, domain.CSXIssueStatusNoReplayLane, "no replay lane: triaged by a person"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.SetCSXIssueVerdict(ctx, first.ID, domain.CSXIssueVerdictDefect, now.Add(time.Hour)); err != nil || !ok {
		t.Fatalf("verdict ok=%v err=%v", ok, err)
	}
	// A verdict already given is not rewritten by a later one.
	if ok, err := store.SetCSXIssueVerdict(ctx, first.ID, domain.CSXIssueVerdictExpectedBehavior, now.Add(2*time.Hour)); err != nil || ok {
		t.Fatalf("a verdict was overwritten: ok=%v err=%v", ok, err)
	}
	if ok, err := store.LinkCSXIssueCanonical(ctx, first.ID, "R2C-51"); err != nil || !ok {
		t.Fatalf("a confirmed defect could not be linked: ok=%v err=%v", ok, err)
	}

	// The same defect again: one row, a higher count, and the link comes
	// back — which is the answer a repeat reporter actually needs.
	again, duplicate, err := store.RecordCSXIssueReport(ctx, csxIssueRow("issue-one"), now.Add(3*time.Hour))
	if err != nil || !duplicate {
		t.Fatalf("duplicate=%v err=%v", duplicate, err)
	}
	if again.ID != first.ID || again.Occurrences != 2 {
		t.Fatalf("one defect became %d rows / %d occurrences", again.ID, again.Occurrences)
	}
	if again.CanonicalRef != "R2C-51" || again.Verdict != domain.CSXIssueVerdictDefect {
		t.Fatalf("a repeat reporter was not told what is already known: %+v", again)
	}

	if _, _, err := store.RecordCSXIssueReport(ctx, csxIssueRow("issue-two"), now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}

	insights, err := store.CSXIssueInsights(ctx, now.Add(5*time.Hour), 30)
	if err != nil {
		t.Fatal(err)
	}
	if insights.Occurrences != 3 || insights.Unique != 2 || insights.Duplicates != 1 {
		t.Fatalf("occurrences=%d unique=%d duplicates=%d, want 3/2/1",
			insights.Occurrences, insights.Unique, insights.Duplicates)
	}
	if insights.Confirmed != 1 || insights.Linked != 1 {
		t.Fatalf("confirmed=%d linked=%d, want 1/1", insights.Confirmed, insights.Linked)
	}
	if insights.Resolved != 1 || insights.Triage != 1 {
		t.Fatalf("resolved=%d triage=%d, want 1/1", insights.Resolved, insights.Triage)
	}

	rows, err := store.ListCSXIssueReports(ctx, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	closed, ok, err := store.CSXIssueReportByID(ctx, first.ID)
	if err != nil || !ok {
		t.Fatalf("read back ok=%v err=%v", ok, err)
	}
	if closed.ReplayReason == "" {
		t.Fatal("the operator page has no reason to show for a report nothing can re-run")
	}
}

func TestFakeCSXIssueStoreContract(t *testing.T) {
	runCSXIssueStoreContract(t, NewFake())
}

func TestIntegrationPGCSXIssueStoreMatchesTheFake(t *testing.T) {
	runCSXIssueStoreContract(t, openTestPG(t))
}
