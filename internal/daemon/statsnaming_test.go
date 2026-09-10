package daemon

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/measurement"
	"github.com/r2cuerdame/codesamplex/internal/metricname"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// The local dashboard document (GET /local/v1/stats, and csx stats --json)
// is read by the user, by csx ui and by the get_local_stats MCP tool, which
// hands it to a model that will paraphrase it. A field named for people would
// come back out of that paraphrase as a claim about people.
//
// docs/activation-funnel.md §6 is the rule; internal/metricname is the rule
// as code.
func TestLocalStatsDocumentNamesNothingItCannotMeasure(t *testing.T) {
	for _, v := range metricname.Check(Stats{}) {
		t.Errorf("local stats field %q: %s — %s", v.Field, v.Rule, v.Why)
	}
}

// Issue #206: verify that Stats correctly exposes the concrete two-layer report.
func TestStatsExposesTwoLayerMeasurementReport(t *testing.T) {
	st := Stats{
		Mode:                    "community",
		Hits:                    10,
		Misses:                  2,
		ExactFailureMatches:     3,
		VerifiedDetoursOffered:  3,
		Packages:                100,
		Adoptions:               5,
		PostHitBuildReports:     4,
		PostHitBuildPassRate:    0.75,
		VerifiedDetoursApplied:  4,
		DetourPostHitPass:       3,
		ReportedFailuresAvoided: 3,
	}
	// Simulate StatsNow population of two-layer model
	st.RetrievalQuality.Hits = st.Hits
	st.RetrievalQuality.Misses = st.Misses
	st.RetrievalQuality.HitRate = float64(st.Hits) / float64(st.Hits+st.Misses)
	st.RetrievalQuality.ExactFailureMatches = st.ExactFailureMatches
	st.RetrievalQuality.VerifiedDetoursOffered = st.VerifiedDetoursOffered
	st.RetrievalQuality.KnownPackages = st.Packages

	st.OutcomeValue.Adoptions = st.Adoptions
	st.OutcomeValue.PostHitBuildReports = st.PostHitBuildReports
	st.OutcomeValue.PostHitBuildPassRate = st.PostHitBuildPassRate
	st.OutcomeValue.VerifiedDetoursApplied = st.VerifiedDetoursApplied
	st.OutcomeValue.DetourPostHitPass = st.DetourPostHitPass
	st.OutcomeValue.ReportedFailuresAvoided = st.ReportedFailuresAvoided
	st.OutcomeValue.Estimated = true

	report := st.TwoLayerReport()
	if report.RetrievalQuality.HitRate < 0.83 || report.RetrievalQuality.HitRate > 0.84 {
		t.Errorf("HitRate = %v, want ~0.833", report.RetrievalQuality.HitRate)
	}
	if report.OutcomeValue.ReportedFailuresAvoided != 3 {
		t.Errorf("ReportedFailuresAvoided = %d, want 3", report.OutcomeValue.ReportedFailuresAvoided)
	}
	if !report.OutcomeValue.Estimated {
		t.Errorf("OutcomeValue.Estimated = false, want true")
	}
}

// TestStatsNow_TwoLayerPopulated provides regression coverage asserting that
// StatsNow(ctx) actively populates st.RetrievalQuality and st.OutcomeValue from
// seeded database and memory state (P1 regression guard against silent zeroing).
func TestStatsNow_TwoLayerPopulated(t *testing.T) {
	home := newTestHome(t, nil)
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	// Seed search offer with exact failure matched and verified detour offered
	offerID, err := d.DB.RecordSearchOffer(ctx, localdb.HitRow{
		TS:       time.Now(),
		Query:    "q",
		Grade:    domain.GradeCompatible,
		SampleID: "sha256:aaa1",
	}, localdb.InterventionRow{
		SampleID:            "sha256:aaa1",
		ExactFailureMatched: true,
		VerifiedOffer:       true,
	})
	if err != nil {
		t.Fatalf("record search offer: %v", err)
	}

	// Correlate adoption with verified detour applied and post-hit build PASS
	if _, err := d.DB.CorrelateInterventionAdoption(ctx, offerID, "sha256:aaa1", true,
		sql.NullBool{Bool: true, Valid: true}, ""); err != nil {
		t.Fatalf("correlate intervention: %v", err)
	}

	// 1 miss
	d.incrStat(ctx, statMisses, 1)

	// Call live producer
	st, err := d.StatsNow(ctx)
	if err != nil {
		t.Fatalf("StatsNow failed: %v", err)
	}

	// Assert Layer 1: RetrievalQuality is populated by producer
	if got, want := st.RetrievalQuality.Hits, 1; got != want {
		t.Errorf("RetrievalQuality.Hits = %d, want %d", got, want)
	}
	if got, want := st.RetrievalQuality.Misses, 1; got != want {
		t.Errorf("RetrievalQuality.Misses = %d, want %d", got, want)
	}
	// 1 hit, 1 miss -> 0.5
	if got, want := st.RetrievalQuality.HitRate, 0.5; got != want {
		t.Errorf("RetrievalQuality.HitRate = %v, want %v", got, want)
	}
	if got, want := st.RetrievalQuality.ExactFailureMatches, 1; got != want {
		t.Errorf("RetrievalQuality.ExactFailureMatches = %d, want %d", got, want)
	}
	if got, want := st.RetrievalQuality.VerifiedDetoursOffered, 1; got != want {
		t.Errorf("RetrievalQuality.VerifiedDetoursOffered = %d, want %d", got, want)
	}

	// Assert Layer 2: OutcomeValue is populated by producer
	if got, want := st.OutcomeValue.Adoptions, 1; got != want {
		t.Errorf("OutcomeValue.Adoptions = %d, want %d", got, want)
	}
	if got, want := st.OutcomeValue.VerifiedDetoursApplied, 1; got != want {
		t.Errorf("OutcomeValue.VerifiedDetoursApplied = %d, want %d", got, want)
	}
	if got, want := st.OutcomeValue.DetourPostHitPass, 1; got != want {
		t.Errorf("OutcomeValue.DetourPostHitPass = %d, want %d", got, want)
	}
	if got, want := st.OutcomeValue.ReportedFailuresAvoided, 1; got != want {
		t.Errorf("OutcomeValue.ReportedFailuresAvoided = %d, want %d", got, want)
	}
	if !st.OutcomeValue.Estimated {
		t.Errorf("OutcomeValue.Estimated = false, want true")
	}

	// Verify report passed through TwoLayerReport() obeys guardrails
	report := st.TwoLayerReport()
	if got, want := report.RetrievalQuality.HitRate, 0.5; got != want {
		t.Errorf("report.RetrievalQuality.HitRate = %v, want %v", got, want)
	}
	if got, want := report.OutcomeValue.ReportedFailuresAvoided, 1; got != want {
		t.Errorf("report.OutcomeValue.ReportedFailuresAvoided = %d, want %d", got, want)
	}
	if err := measurement.ValidateReportGuardrails(report); err != nil {
		t.Errorf("report from StatsNow failed guardrails: %v", err)
	}
}
