package measurement

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/metricname"
)

func TestTwoLayerReportStructureAndHitRate(t *testing.T) {
	retrieval := RetrievalQuality{
		Hits:                   80,
		Misses:                 20,
		ExactFailureMatches:    15,
		VerifiedDetoursOffered: 12,
		KnownPackages:          500,
		CacheBytes:             1024 * 1024 * 10,
		EvidenceBatchesSent:    5,
		OriginSeeds:            2,
		CrossVerifications:     8,
	}
	outcome := OutcomeValue{
		Adoptions:                 10,
		PostHitBuildReports:       8,
		PostHitBuildPassRate:      0.75,
		VerifiedDetoursApplied:    7,
		DetourPostHitPass:         5,
		DetourPostHitFail:         2,
		DetourPostHitUnknown:      1,
		ReportedFailuresAvoided:   5,
		EstimatedReasoningAvoided: 20,
	}

	report := NewTwoLayerReport("community", retrieval, outcome)

	if report.RetrievalQuality.HitRate != 0.8 {
		t.Errorf("HitRate = %v, want 0.8", report.RetrievalQuality.HitRate)
	}
	if !report.OutcomeValue.Estimated {
		t.Errorf("OutcomeValue.Estimated = false, want true")
	}
	if report.CausalModel != CausalModelDescription {
		t.Errorf("CausalModel = %q, want %q", report.CausalModel, CausalModelDescription)
	}

	summary := report.SummaryText()
	for _, want := range []string{
		"Layer 1 · Retrieval & Memory Quality",
		"Hits / Misses:               80 / 20  (hit rate: 80.0%)",
		"Layer 2 · User & Agent Outcome Value",
		"Adoptions:                   10",
		"Post-hit build pass:         75.0% (8 reports)",
		"Reported failures avoided:   5",
		"Estimated reasoning avoided: 20  (Estimated — never measured)",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing expected line %q", want)
		}
	}
}

func TestTwoLayerReportCompliesWithMetricnameRules(t *testing.T) {
	report := TwoLayerReport{}
	violations := metricname.Check(report)
	if len(violations) > 0 {
		t.Fatalf("TwoLayerReport violated metricname rules: %+v", violations)
	}
}

func TestGuardrailsRejectVanityMetricsForOutcomeClaims(t *testing.T) {
	claim := ProductClaim{
		Statement:      "CSX makes LLMs 50% better because we indexed 100,000 packages",
		TargetLayer:    LayerOutcomeValue,
		EvidenceSource: EvidenceFieldObserved,
		CitedMetrics:   []string{"packages", "lookup_count"},
	}
	err := ValidateClaim(claim)
	if !errors.Is(err, ErrVanityMetricClaim) {
		t.Fatalf("ValidateClaim() = %v, want %v", err, ErrVanityMetricClaim)
	}
}

func TestGuardrailsRejectInventedUpliftPercentages(t *testing.T) {
	uplift := 35.0
	// No controlled baseline.
	claimNoBaseline := ProductClaim{
		Statement:          "CSX improves agent coding by 35%",
		TargetLayer:        LayerOutcomeValue,
		EvidenceSource:     EvidenceFieldObserved,
		ClaimedUpliftRate:  &uplift,
		ControlledBaseline: false,
		SampleSize:         100,
	}
	if err := ValidateClaim(claimNoBaseline); !errors.Is(err, ErrInventedUplift) {
		t.Fatalf("claim without baseline: err = %v, want %v", err, ErrInventedUplift)
	}

	// Small sample size.
	claimSmallSample := ProductClaim{
		Statement:          "CSX improves agent coding by 35%",
		TargetLayer:        LayerOutcomeValue,
		EvidenceSource:     EvidenceFieldObserved,
		ClaimedUpliftRate:  &uplift,
		ControlledBaseline: true,
		SampleSize:         15, // Below MinControlledSampleSize (50)
	}
	if err := ValidateClaim(claimSmallSample); !errors.Is(err, ErrInventedUplift) {
		t.Fatalf("claim with small sample: err = %v, want %v", err, ErrInventedUplift)
	}

	// Valid claim with controlled baseline and sufficient sample.
	claimValid := ProductClaim{
		Statement:          "Measured 35% reduction in dead-end retries across 120 field sessions",
		TargetLayer:        LayerOutcomeValue,
		EvidenceSource:     EvidenceFieldObserved,
		ClaimedUpliftRate:  &uplift,
		ControlledBaseline: true,
		SampleSize:         120,
		CitedMetrics:       []string{"postHitBuildPassRate", "reportedFailuresAvoided"},
	}
	if err := ValidateClaim(claimValid); err != nil {
		t.Fatalf("valid claim rejected: %v", err)
	}
}

func TestGuardrailsRejectSyntheticBenchmarkAsSoleOutcomeBasis(t *testing.T) {
	claim := ProductClaim{
		Statement:      "Synthetic test suite passes 90% of cases",
		TargetLayer:    LayerOutcomeValue,
		EvidenceSource: EvidenceSyntheticBenchmark,
		CitedMetrics:   []string{"taskSuccessRate"},
	}
	if err := ValidateClaim(claim); !errors.Is(err, ErrSyntheticOutcomeOnly) {
		t.Fatalf("synthetic benchmark claim: err = %v, want %v", err, ErrSyntheticOutcomeOnly)
	}
}

func TestGuardrailsValidateReportConsistency(t *testing.T) {
	// Unlabelled estimate.
	badEstimate := TwoLayerReport{
		OutcomeValue: OutcomeValue{
			EstimatedReasoningAvoided: 10,
			Estimated:                 false,
		},
	}
	if err := ValidateReportGuardrails(badEstimate); !errors.Is(err, ErrUnlabelledEstimate) {
		t.Errorf("bad estimate: err = %v, want %v", err, ErrUnlabelledEstimate)
	}

	// Reported failures avoided > applied detours.
	badFunnel := TwoLayerReport{
		OutcomeValue: OutcomeValue{
			VerifiedDetoursApplied:  2,
			ReportedFailuresAvoided: 5, // impossible
		},
	}
	if err := ValidateReportGuardrails(badFunnel); !errors.Is(err, ErrInconsistentFunnel) {
		t.Errorf("bad funnel: err = %v, want %v", err, ErrInconsistentFunnel)
	}

	// Valid report passes.
	valid := NewTwoLayerReport("community",
		RetrievalQuality{Hits: 10, Misses: 2},
		OutcomeValue{VerifiedDetoursApplied: 5, ReportedFailuresAvoided: 4},
	)
	if err := ValidateReportGuardrails(valid); err != nil {
		t.Errorf("valid report failed guardrails: %v", err)
	}
}

func TestTwoLayerReportJSONRoundtrip(t *testing.T) {
	retrieval := RetrievalQuality{Hits: 10, Misses: 2, HitRate: CalculateHitRate(10, 2)}
	outcome := OutcomeValue{Adoptions: 4, ReportedFailuresAvoided: 3, EstimatedReasoningAvoided: 9}
	original := NewTwoLayerReport("community", retrieval, outcome)

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed TwoLayerReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed.RetrievalQuality.Hits != 10 || parsed.RetrievalQuality.Misses != 2 {
		t.Errorf("retrieval mismatch: %+v", parsed.RetrievalQuality)
	}
	if parsed.OutcomeValue.Adoptions != 4 || parsed.OutcomeValue.ReportedFailuresAvoided != 3 {
		t.Errorf("outcome mismatch: %+v", parsed.OutcomeValue)
	}
	if !parsed.OutcomeValue.Estimated {
		t.Errorf("parsed.OutcomeValue.Estimated = false, want true")
	}
}

func TestNewTwoLayerReport_HitRateCalculation(t *testing.T) {
	// Caller supplies inconsistent non-zero HitRate; constructor should recalculate it unconditionally.
	retrieval := RetrievalQuality{
		Hits:    9,
		Misses:  1,
		HitRate: 0.5, // inconsistent with 9 hits and 1 miss
	}
	report := NewTwoLayerReport("community", retrieval, OutcomeValue{})
	if got, want := report.RetrievalQuality.HitRate, 0.9; got != want {
		t.Errorf("HitRate = %v, want %v", got, want)
	}
	if err := ValidateReportGuardrails(report); err != nil {
		t.Errorf("ValidateReportGuardrails failed on report with recalculated hit rate: %v", err)
	}

	// Zero hits and misses safely yields 0.0.
	zeroReport := NewTwoLayerReport("community", RetrievalQuality{Hits: 0, Misses: 0, HitRate: 0.42}, OutcomeValue{})
	if got, want := zeroReport.RetrievalQuality.HitRate, 0.0; got != want {
		t.Errorf("zero HitRate = %v, want %v", got, want)
	}
}
