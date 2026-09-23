package daemon

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/measurement"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// TestStatsNow_OutcomeCountsSpanWholeStore pins #346: with more hits than one
// ListHits page (10,000), adoptions and build reports still come from the
// whole store. The outcome rows are the OLDEST ones, so a page of the newest
// 10,000 would have held none of them.
func TestStatsNow_OutcomeCountsSpanWholeStore(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts >10,000 hits")
	}
	home := newTestHome(t, nil)
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	ts := time.Now().UTC()
	pass := sql.NullBool{Bool: true, Valid: true}
	fail := sql.NullBool{Bool: false, Valid: true}
	const adopted, passed, failed, plain = 12, 8, 3, 10_000
	for i := 0; i < adopted; i++ {
		h := localdb.HitRow{TS: ts, Query: "q", Grade: domain.GradeExact, SampleID: "sha256:old", Adopted: true}
		switch {
		case i < passed:
			h.PostBuildPass = pass
		case i < passed+failed:
			h.PostBuildPass = fail
		}
		if err := d.DB.RecordHit(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < plain; i++ {
		if err := d.DB.RecordHit(ctx, localdb.HitRow{TS: ts, Query: "q", Grade: domain.GradeExact, SampleID: "sha256:new"}); err != nil {
			t.Fatal(err)
		}
	}

	st, err := d.StatsNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hits != adopted+plain {
		t.Errorf("Hits = %d, want %d", st.Hits, adopted+plain)
	}
	if st.Adoptions != adopted || st.OutcomeValue.Adoptions != adopted {
		t.Errorf("Adoptions = %d / outcomeValue %d, want %d", st.Adoptions, st.OutcomeValue.Adoptions, adopted)
	}
	if st.PostHitBuildReports != passed+failed || st.OutcomeValue.PostHitBuildReports != passed+failed {
		t.Errorf("PostHitBuildReports = %d / outcomeValue %d, want %d",
			st.PostHitBuildReports, st.OutcomeValue.PostHitBuildReports, passed+failed)
	}
	if want := float64(passed) / float64(passed+failed); st.OutcomeValue.PostHitBuildPassRate != want {
		t.Errorf("PostHitBuildPassRate = %v, want %v", st.OutcomeValue.PostHitBuildPassRate, want)
	}
	if want := adopted*measurement.ReasoningCallsPerAdoption - failed; st.OutcomeValue.EstimatedReasoningAvoided != want {
		t.Errorf("EstimatedReasoningAvoided = %d, want %d", st.OutcomeValue.EstimatedReasoningAvoided, want)
	}
	if err := measurement.ValidateReportGuardrails(st.TwoLayerReport()); err != nil {
		t.Errorf("whole-store report fails guardrails: %v", err)
	}
}
