package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The Fake is the reference implementation of the dedupe rule and PG is held
// to it, exactly as they are for evidence merging. A rule that only the
// in-memory store enforces is not a rule: the Fake has no unique index to
// violate, so every handler test would pass while production queued a
// container per duplicate.

func anomalyRow(fingerprint, sampleID string) AnomalyReportRow {
	return AnomalyReportRow{
		Fingerprint:    fingerprint,
		ReportJSON:     `{"schemaVersion":1,"anomalyType":"csx-pass-local-fail"}`,
		AnomalyType:    domain.AnomalyCSXPassLocalFail,
		PURL:           "pkg:npm/axios@1.12.0",
		Symbol:         "axios.post",
		SampleID:       sampleID,
		ReporterBucket: "anon-2026-08-24-a",
	}
}

func runAnomalyStoreContract(t *testing.T, store AnomalyStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	first, duplicate, err := store.RecordAnomalyReport(ctx, anomalyRow("fp-one", "sha256:abc"), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if duplicate {
		t.Fatal("the first submission of a fingerprint is not a duplicate")
	}
	if first.ID == 0 || first.Reports != 1 || first.Status != domain.AnomalyStatusQueued {
		t.Fatalf("stored row = %+v", first)
	}

	// The whole spam defence in one assertion.
	again, duplicate, err := store.RecordAnomalyReport(ctx, anomalyRow("fp-one", "sha256:abc"), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("record duplicate: %v", err)
	}
	if !duplicate {
		t.Fatal("a repeated fingerprint must be recognized as a duplicate")
	}
	if again.ID != first.ID {
		t.Fatalf("a duplicate created a second row: %d then %d", first.ID, again.ID)
	}
	if again.Reports != 2 {
		t.Fatalf("duplicate submissions = %d, want 2", again.Reports)
	}
	if !again.LastSeen.After(first.FirstSeen) {
		t.Fatal("a duplicate must move lastSeen so a cooldown can be computed from it")
	}

	if err := store.AttachAnomalyVerificationJob(ctx, first.ID, 77, domain.AnomalyStatusQueued, ""); err != nil {
		t.Fatalf("attach job: %v", err)
	}
	open, err := store.OpenAnomalyReportsForSample(ctx, "sha256:abc")
	if err != nil || len(open) != 1 || open[0].JobID != 77 {
		t.Fatalf("open reports = %+v err=%v", open, err)
	}
	if !open[0].VerdictAt.IsZero() {
		t.Fatalf("a report with no verdict must carry a zero verdict time, got %v", open[0].VerdictAt)
	}

	verdictAt := now.Add(90 * time.Minute)
	updated, err := store.SetAnomalyVerdict(ctx, first.ID, domain.AnomalyVerdictCSXDefect, verdictAt)
	if err != nil || !updated {
		t.Fatalf("set verdict updated=%v err=%v", updated, err)
	}
	// A later receipt for the same sample answers a different question and
	// must not rewrite an answer already given.
	updated, err = store.SetAnomalyVerdict(ctx, first.ID, domain.AnomalyVerdictNotReproducible, verdictAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second verdict: %v", err)
	}
	if updated {
		t.Fatal("a verdict was overwritten by a later one")
	}

	closed, ok, err := store.AnomalyReportByID(ctx, first.ID)
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if closed.Verdict != domain.AnomalyVerdictCSXDefect || closed.Status != domain.AnomalyStatusVerified {
		t.Fatalf("closed row = %+v", closed)
	}
	if open, err := store.OpenAnomalyReportsForSample(ctx, "sha256:abc"); err != nil || len(open) != 0 {
		t.Fatalf("a decided report is no longer open: %+v err=%v", open, err)
	}

	// A second, unrelated report so the window aggregate has more than one
	// row and the duplicate arithmetic is actually exercised.
	if _, _, err := store.RecordAnomalyReport(ctx, anomalyRow("fp-two", ""), now.Add(2*time.Hour)); err != nil {
		t.Fatalf("record second: %v", err)
	}

	insights, err := store.AnomalyInsights(ctx, now.Add(3*time.Hour), 30)
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	if insights.Reports != 3 || insights.Unique != 2 || insights.Duplicates != 1 {
		t.Fatalf("reports=%d unique=%d duplicates=%d, want 3/2/1", insights.Reports, insights.Unique, insights.Duplicates)
	}
	if insights.Verified != 1 || insights.Queued != 1 {
		t.Fatalf("verified=%d queued=%d, want 1/1", insights.Verified, insights.Queued)
	}
	if insights.Confirmed != 1 || insights.CSXDefects != 1 {
		t.Fatalf("confirmed=%d csxDefects=%d, want 1/1", insights.Confirmed, insights.CSXDefects)
	}
	mean, measured := insights.MeanVerdictLatency()
	if !measured || mean != 90*time.Minute {
		t.Fatalf("mean verdict latency = %v measured=%v, want 1h30m", mean, measured)
	}
	if insights.BusiestReporter != 3 {
		t.Fatalf("busiest reporter bucket = %d, want 3", insights.BusiestReporter)
	}
	if len(insights.Verdicts) != 1 || insights.Verdicts[0].Verdict != domain.AnomalyVerdictCSXDefect {
		t.Fatalf("verdict buckets = %+v", insights.Verdicts)
	}
}

func TestFakeAnomalyStoreContract(t *testing.T) {
	runAnomalyStoreContract(t, NewFake())
}

func TestIntegrationPGAnomalyStoreMatchesTheFake(t *testing.T) {
	runAnomalyStoreContract(t, openTestPG(t))
}

// A mean over nothing is not zero, it is absent. Reporting 0ms for a channel
// that has answered nothing yet is the kind of number an operator acts on.
func TestMeanVerdictLatencyIsAbsentWithNothingMeasured(t *testing.T) {
	if _, measured := (AnomalyInsights{}).MeanVerdictLatency(); measured {
		t.Fatal("an empty window reported a measured latency")
	}
}
