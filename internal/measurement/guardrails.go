package measurement

import (
	"errors"
	"fmt"
	"strings"
)

// EvidenceSource classifies the origin of measurement data.
type EvidenceSource string

const (
	// EvidenceFieldObserved represents real-world usage observations from developer/agent workflows.
	// This is the primary and required evidence source for user outcome claims (docs/measurement-layers.md §4).
	EvidenceFieldObserved EvidenceSource = "field_observed"

	// EvidenceSyntheticBenchmark represents offline automated evaluation or benchmark tasks.
	// It is supporting evidence only, not a replacement for real-world field outcomes.
	EvidenceSyntheticBenchmark EvidenceSource = "synthetic_benchmark"

	// EvidenceInternalDogfood represents internal team testing and dogfooding.
	// It is supporting evidence only.
	EvidenceInternalDogfood EvidenceSource = "internal_dogfood"
)

// MinControlledSampleSize is the minimum sample size of observed/controlled events required
// before any statistical comparison or percentage rate may be cited as an outcome claim.
const MinControlledSampleSize = 50

var (
	// ErrVanityMetricClaim indicates a product claim cited corpus size or lookup volume as proof of agent outcome.
	ErrVanityMetricClaim = errors.New("guardrail violation: vanity metrics cannot be used to claim user/agent outcome value")

	// ErrInventedUplift indicates an uplift percentage was asserted without controlled baseline or sufficient sample size.
	ErrInventedUplift = errors.New("guardrail violation: invented uplift without controlled counterfactual evidence and sufficient sample size")

	// ErrSyntheticOutcomeOnly indicates an outcome claim relied solely on synthetic benchmarks without field evidence.
	ErrSyntheticOutcomeOnly = errors.New("guardrail violation: product outcome claim cannot rely solely on synthetic benchmarks")

	// ErrDogfoodOutcomeOnly indicates an outcome claim relied solely on internal dogfooding without field evidence.
	ErrDogfoodOutcomeOnly = errors.New("guardrail violation: product outcome claim cannot rely solely on internal dogfooding")

	// ErrFieldEvidenceRequired indicates an outcome claim lacks primary field-observed evidence.
	ErrFieldEvidenceRequired = errors.New("guardrail violation: product outcome claim requires field-observed evidence")

	// ErrLayerConfusion indicates an internal search metric was presented as an agent outcome value.
	ErrLayerConfusion = errors.New("guardrail violation: Layer 1 retrieval quality metric presented as Layer 2 outcome value")

	// ErrUnlabelledEstimate indicates an estimated outcome was not marked as estimated.
	ErrUnlabelledEstimate = errors.New("guardrail violation: estimated outcome must be explicitly flagged as estimated")

	// ErrInconsistentFunnel indicates outcome funnel stages violate logical containment constraints.
	ErrInconsistentFunnel = errors.New("guardrail violation: outcome funnel stages violate logical containment constraints")
)

// vanityMetrics are Layer 1 corpus and lookup counters that must never be presented as proof of agent uplift.
var vanityMetrics = map[string]struct{}{
	"corpus_size":   {},
	"packages":      {},
	"symbols":       {},
	"shards":        {},
	"cache_bytes":   {},
	"raw_lookups":   {},
	"lookup_count":  {},
	"evidence_rows": {},
	"search_volume": {},
}

// ProductClaim represents an asserted external or product-facing measurement statement.
type ProductClaim struct {
	Statement          string         `json:"statement"`
	TargetLayer        string         `json:"targetLayer"`
	EvidenceSource     EvidenceSource `json:"evidenceSource"`
	CitedMetrics       []string       `json:"citedMetrics"`
	ClaimedUpliftRate  *float64       `json:"claimedUpliftRate,omitempty"`
	ControlledBaseline bool           `json:"controlledBaseline"`
	SampleSize         int            `json:"sampleSize"`
}

// ValidateClaim applies the field-first guardrails (docs/measurement-layers.md §4) to a product claim.
func ValidateClaim(claim ProductClaim) error {
	// Guardrail 1: Do not publish vanity metrics as proof that CSX makes an LLM or agent better.
	if claim.TargetLayer == LayerOutcomeValue {
		for _, metric := range claim.CitedMetrics {
			norm := strings.ToLower(strings.ReplaceAll(metric, "-", "_"))
			if _, isVanity := vanityMetrics[norm]; isVanity {
				return fmt.Errorf("%w: metric %q is a Layer 1 volume/corpus metric; raw size or lookup count does not prove agent or developer uplift",
					ErrVanityMetricClaim, metric)
			}
		}
	}

	// Guardrail 2 & 3: Do not invent uplift percentages without a credible measurement design and enough data.
	if claim.ClaimedUpliftRate != nil {
		rate := *claim.ClaimedUpliftRate
		if !claim.ControlledBaseline {
			return fmt.Errorf("%w: claimed uplift of %.1f%% requires a controlled counterfactual baseline (A/B or matched cohort)",
				ErrInventedUplift, rate)
		}
		if claim.SampleSize < MinControlledSampleSize {
			return fmt.Errorf("%w: claimed uplift of %.1f%% requires at least %d observed samples (got %d)",
				ErrInventedUplift, rate, MinControlledSampleSize, claim.SampleSize)
		}
	}

	// Guardrail 4: Field-first measurement — synthetic benchmarks and internal dogfood are supporting only, not replacements for field outcomes.
	if claim.TargetLayer == LayerOutcomeValue {
		switch claim.EvidenceSource {
		case EvidenceFieldObserved:
			// Primary and required evidence source for user outcome claims.
		case EvidenceSyntheticBenchmark:
			return fmt.Errorf("%w: benchmark %q is supporting evidence, not a replacement for real-world field outcomes",
				ErrSyntheticOutcomeOnly, claim.Statement)
		case EvidenceInternalDogfood:
			return fmt.Errorf("%w: internal dogfood %q is supporting evidence, not a replacement for real-world field outcomes",
				ErrDogfoodOutcomeOnly, claim.Statement)
		default:
			return fmt.Errorf("%w: outcome claim %q requires field-observed evidence (got %q)",
				ErrFieldEvidenceRequired, claim.Statement, claim.EvidenceSource)
		}
	}

	// Guardrail: Layer confusion — Layer 1 retrieval metrics explain search quality, not task success.
	if claim.TargetLayer == LayerRetrievalQuality {
		for _, m := range claim.CitedMetrics {
			lower := strings.ToLower(m)
			if strings.Contains(lower, "uplift") || strings.Contains(lower, "timesaved") || strings.Contains(lower, "speedup") {
				return fmt.Errorf("%w: Layer 1 retrieval metrics cannot claim outcome uplift (%s)",
					ErrLayerConfusion, m)
			}
		}
	}

	return nil
}

// ValidateReportGuardrails validates logical and honesty constraints of a TwoLayerReport.
func ValidateReportGuardrails(r TwoLayerReport) error {
	// Estimates must be explicitly flagged.
	if r.OutcomeValue.EstimatedReasoningAvoided > 0 && !r.OutcomeValue.Estimated {
		return fmt.Errorf("%w: estimatedReasoningAvoided=%d requires estimated=true",
			ErrUnlabelledEstimate, r.OutcomeValue.EstimatedReasoningAvoided)
	}

	// Funnel stage 2 -> stage 3: applied detours cannot exceed offered detours.
	if r.OutcomeValue.VerifiedDetoursApplied > r.RetrievalQuality.VerifiedDetoursOffered {
		return fmt.Errorf("%w: verifiedDetoursApplied (%d) cannot exceed verifiedDetoursOffered (%d)",
			ErrInconsistentFunnel, r.OutcomeValue.VerifiedDetoursApplied, r.RetrievalQuality.VerifiedDetoursOffered)
	}

	// Funnel stage 3 -> post-hit outcomes: post-hit outcomes cannot exceed applied detours.
	postHitTotal := r.OutcomeValue.DetourPostHitPass + r.OutcomeValue.DetourPostHitFail + r.OutcomeValue.DetourPostHitUnknown
	if postHitTotal > r.OutcomeValue.VerifiedDetoursApplied {
		return fmt.Errorf("%w: detour post-hit outcomes (%d) cannot exceed verifiedDetoursApplied (%d)",
			ErrInconsistentFunnel, postHitTotal, r.OutcomeValue.VerifiedDetoursApplied)
	}

	// Funnel stage 4: reported failures avoided requires all 4 measured stages (match -> offer -> apply -> PASS),
	// so it cannot exceed verifiedDetoursApplied or detourPostHitPass.
	if r.OutcomeValue.ReportedFailuresAvoided > r.OutcomeValue.VerifiedDetoursApplied {
		return fmt.Errorf("%w: reportedFailuresAvoided (%d) cannot exceed verifiedDetoursApplied (%d)",
			ErrInconsistentFunnel, r.OutcomeValue.ReportedFailuresAvoided, r.OutcomeValue.VerifiedDetoursApplied)
	}
	if r.OutcomeValue.ReportedFailuresAvoided > r.OutcomeValue.DetourPostHitPass {
		return fmt.Errorf("%w: reportedFailuresAvoided (%d) cannot exceed detourPostHitPass (%d)",
			ErrInconsistentFunnel, r.OutcomeValue.ReportedFailuresAvoided, r.OutcomeValue.DetourPostHitPass)
	}

	// Hit rate must match hits/(hits+misses) when searches have occurred.
	total := r.RetrievalQuality.Hits + r.RetrievalQuality.Misses
	if total > 0 {
		expected := float64(r.RetrievalQuality.Hits) / float64(total)
		diff := r.RetrievalQuality.HitRate - expected
		if diff < -0.001 || diff > 0.001 {
			return fmt.Errorf("%w: hitRate %.4f does not match hits/(hits+misses) = %.4f",
				ErrInconsistentFunnel, r.RetrievalQuality.HitRate, expected)
		}
	}

	return nil
}
