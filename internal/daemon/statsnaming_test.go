package daemon

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/metricname"
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
		Mode:                   "community",
		Hits:                   10,
		Misses:                 2,
		ExactFailureMatches:    3,
		VerifiedDetoursOffered: 3,
		Packages:               100,
		Adoptions:              5,
		PostHitBuildReports:    4,
		PostHitBuildPassRate:   0.75,
		VerifiedDetoursApplied: 4,
		DetourPostHitPass:      3,
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

