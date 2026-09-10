package compatibility

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/metricname"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// GET /v1/stats is the public rollup, and its field names are quoted far away
// from the caveats printed beside them. So the names themselves have to be
// unable to claim a head count: this document counts observation records,
// samples and rotating anonymous buckets, and the README's promise that
// unique/active users and successful installs are NOT measured stays true
// only while no field here is named as though they were.
//
// docs/activation-funnel.md §6 is the rule; internal/metricname is the rule
// as code.
func TestPublicStatsDocumentNamesNothingItCannotMeasure(t *testing.T) {
	for _, v := range metricname.Check(StatsDoc{}) {
		t.Errorf("/v1/stats field %q: %s — %s", v.Field, v.Rule, v.Why)
	}
}

// peers and projectsMonth are the two fields most likely to be read as people.
// They are allowed because they are declared with their unit; this pins the
// declaration so removing it is a test failure rather than a silent loss of
// the only place that says what one unit is.
func TestTheBucketNounsOnThePublicDocumentStayDeclared(t *testing.T) {
	for _, name := range []string{"peers", "projectsMonth"} {
		if metricname.BucketNouns[name] == "" {
			t.Errorf("bucket noun %q lost its declared unit", name)
		}
	}
}

// TestStatsJSON_TwoLayerPopulated provides regression test coverage asserting that
// StatsJSON actively populates the nested retrievalQuality and outcomeValue objects
// (P1 regression guard against silent zeroing or omission in GET /v1/stats).
func TestStatsJSON_TwoLayerPopulated(t *testing.T) {
	counts := serverstore.NetworkCounts{
		Packages:        120,
		Symbols:         3400,
		Observations:    5600,
		VerifiedSamples: 42,
		Peers:           7,
		ProjectsMonth:   15,
	}
	adopt := serverstore.AdoptionCounts{
		Applied:   10,
		BuildPass: 8,
		BuildFail: 2,
	}
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

	payload, err := StatsJSON(counts, adopt, now)
	if err != nil {
		t.Fatalf("StatsJSON returned error: %v", err)
	}

	var doc StatsDoc
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("unmarshal StatsDoc failed: %v", err)
	}

	// Assert Layer 1: RetrievalQuality populated from producer
	if got, want := doc.RetrievalQuality.Packages, counts.Packages; got != want {
		t.Errorf("RetrievalQuality.Packages = %d, want %d", got, want)
	}
	if got, want := doc.RetrievalQuality.Symbols, counts.Symbols; got != want {
		t.Errorf("RetrievalQuality.Symbols = %d, want %d", got, want)
	}
	if got, want := doc.RetrievalQuality.Evidence, counts.Observations; got != want {
		t.Errorf("RetrievalQuality.Evidence = %d, want %d", got, want)
	}
	if got, want := doc.RetrievalQuality.VerifiedSamples, counts.VerifiedSamples; got != want {
		t.Errorf("RetrievalQuality.VerifiedSamples = %d, want %d", got, want)
	}
	if got, want := doc.RetrievalQuality.Peers, counts.Peers; got != want {
		t.Errorf("RetrievalQuality.Peers = %d, want %d", got, want)
	}
	if got, want := doc.RetrievalQuality.ProjectsMonth, counts.ProjectsMonth; got != want {
		t.Errorf("RetrievalQuality.ProjectsMonth = %d, want %d", got, want)
	}

	// Assert Layer 2: OutcomeValue populated from producer
	expectedRate := float64(8) / float64(8+2) // 0.8
	if got, want := doc.OutcomeValue.PostHitSuccessRate, expectedRate; got != want {
		t.Errorf("OutcomeValue.PostHitSuccessRate = %v, want %v", got, want)
	}
	if got, want := doc.OutcomeValue.PostHitBuildPass.Value, 8.0; got != want {
		t.Errorf("OutcomeValue.PostHitBuildPass.Value = %v, want %v", got, want)
	}
	if got, want := doc.OutcomeValue.PostHitBuildsReported, int64(10); got != want {
		t.Errorf("OutcomeValue.PostHitBuildsReported = %d, want %d", got, want)
	}
	if got, want := doc.OutcomeValue.EstimatedReasoningAvoided.Value, int64(30); got != want {
		t.Errorf("OutcomeValue.EstimatedReasoningAvoided.Value = %d, want %d", got, want)
	}
	if !doc.OutcomeValue.EstimatedReasoningAvoided.Estimated {
		t.Errorf("OutcomeValue.EstimatedReasoningAvoided.Estimated = false, want true")
	}
	if !doc.OutcomeValue.Estimated {
		t.Errorf("OutcomeValue.Estimated = false, want true")
	}

	// Raw JSON inspection to guarantee nested wire objects are present
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("unmarshal raw JSON failed: %v", err)
	}
	if _, ok := raw["retrievalQuality"]; !ok {
		t.Errorf("missing wire key 'retrievalQuality'")
	}
	if _, ok := raw["outcomeValue"]; !ok {
		t.Errorf("missing wire key 'outcomeValue'")
	}
}

// TestStatsDoc verifies that StatsDoc two-layer models satisfy all metric naming rules.
func TestStatsDoc(t *testing.T) {
	doc := StatsDoc{
		RetrievalQuality: PublicRetrievalQuality{
			Packages: 10,
		},
		OutcomeValue: PublicOutcomeValue{
			Estimated: true,
		},
	}
	for _, v := range metricname.Check(doc) {
		t.Errorf("StatsDoc field %q violates rule: %s (%s)", v.Field, v.Rule, v.Why)
	}
}
