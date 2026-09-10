// Package measurement implements the concrete two-layer measurement model for CodeSampleX
// (GitHub issue #206, docs/measurement-layers.md).
//
// The core design cleanly separates:
//  1. Retrieval / Memory Quality (internal system quality: "Is CSX returning the right execution memory?")
//  2. User / Agent Outcome Value (core product value: "Did that memory actually improve the user's/agent's result?")
//
// The causal relationship is:
//
//	retrieval/memory quality -> better evidence surfaced -> better user/agent outcome
//
// Field-first guardrails enforce that:
//   - Raw corpus size, lookup counts, and retrieval hit rate are never published as proof of agent uplift.
//   - User outcome remains the north star, not internal search quality.
//   - Uplift percentages must never be invented or claimed without controlled counterfactual measurement and sufficient sample size.
//   - Field-observed evidence takes precedence over synthetic benchmarks or dogfooding.
package measurement

import (
	"fmt"
	"strings"
)

// SchemaVersion defines the schema contract version for the two-layer report.
const SchemaVersion = 1

// Layer identifiers explicitly demarcate the two measurement layers.
const (
	// LayerRetrievalQuality (Layer 1) measures internal search and execution-memory quality.
	// Question: "Is CSX returning the right execution memory?"
	// Role: System diagnosis, tuning, and engineering improvements.
	LayerRetrievalQuality = "layer1_retrieval_memory_quality"

	// LayerOutcomeValue (Layer 2) measures real user and coding agent outcomes.
	// Question: "Did that memory actually improve the user's/agent's result?"
	// Role: Core product value, north-star metrics, and external product claims.
	LayerOutcomeValue = "layer2_user_outcome_value"
)

// CausalModelDescription expresses the explicit relationship between the two layers.
const CausalModelDescription = "retrieval/memory quality -> better evidence surfaced -> better user/agent outcome"

// RetrievalQuality represents Layer 1: internal search and memory quality.
// These metrics answer "Is CSX returning the right execution memory?"
// They explain why the product works, but are never an outcome value by themselves.
type RetrievalQuality struct {
	Hits                   int     `json:"hits"`
	Misses                 int     `json:"misses"`
	HitRate                float64 `json:"hitRate"`
	ExactFailureMatches    int     `json:"exactFailureMatches"`
	VerifiedDetoursOffered int     `json:"verifiedDetoursOffered"`
	KnownPackages          int     `json:"knownPackages"`
	CacheBytes             int64   `json:"cacheBytes"`
	EvidenceBatchesSent    int     `json:"evidenceBatchesSent"`
	OriginSeeds            int     `json:"originSeeds"`
	CrossVerifications     int     `json:"crossVerifications"`
}

// OutcomeValue represents Layer 2: user and agent outcome value.
// These metrics answer "Did that memory actually improve the user's/agent's result?"
// This is the north-star value that justifies product claims.
type OutcomeValue struct {
	Adoptions                 int     `json:"adoptions"`
	PostHitBuildReports       int     `json:"postHitBuildReports"`
	PostHitBuildPassRate      float64 `json:"postHitBuildPassRate"`
	VerifiedDetoursApplied    int     `json:"verifiedDetoursApplied"`
	DetourPostHitPass         int     `json:"detourPostHitPass"`
	DetourPostHitFail         int     `json:"detourPostHitFail"`
	DetourPostHitUnknown      int     `json:"detourPostHitUnknown"`
	ReportedFailuresAvoided   int     `json:"reportedFailuresAvoided"`
	EstimatedReasoningAvoided int     `json:"estimatedReasoningAvoided"`
	Estimated                 bool    `json:"estimated"`
}

// TwoLayerReport cleanly separates Layer 1 and Layer 2 for dashboards, CLI, and APIs.
type TwoLayerReport struct {
	SchemaVersion    int              `json:"schemaVersion"`
	Mode             string           `json:"mode"`
	CausalModel      string           `json:"causalModel"`
	RetrievalQuality RetrievalQuality `json:"retrievalQuality"`
	OutcomeValue     OutcomeValue     `json:"outcomeValue"`
}

// CalculateHitRate safely calculates the ratio of hits to total searches.
func CalculateHitRate(hits, misses int) float64 {
	total := hits + misses
	if total <= 0 {
		return 0.0
	}
	return float64(hits) / float64(total)
}

// NewTwoLayerReport constructs a validated TwoLayerReport.
func NewTwoLayerReport(mode string, retrieval RetrievalQuality, outcome OutcomeValue) TwoLayerReport {
	retrieval.HitRate = CalculateHitRate(retrieval.Hits, retrieval.Misses)
	// EstimatedReasoningAvoided is an estimate by construction (docs/activation-funnel.md §6).
	// The estimated flag must always accompany it.
	outcome.Estimated = true

	return TwoLayerReport{
		SchemaVersion:    SchemaVersion,
		Mode:             mode,
		CausalModel:      CausalModelDescription,
		RetrievalQuality: retrieval,
		OutcomeValue:     outcome,
	}
}

// SummaryText formats the two-layer report as human-readable text for terminal display.
func (r TwoLayerReport) SummaryText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mode:                          %s\n", r.Mode)

	fmt.Fprintf(&b, "\nLayer 1 · Retrieval & Memory Quality (internal search quality):\n")
	fmt.Fprintf(&b, "  Hits / Misses:               %d / %d  (hit rate: %.1f%%)\n",
		r.RetrievalQuality.Hits, r.RetrievalQuality.Misses, r.RetrievalQuality.HitRate*100)
	fmt.Fprintf(&b, "  Exact failure matches:       %d\n", r.RetrievalQuality.ExactFailureMatches)
	fmt.Fprintf(&b, "  Verified detours offered:    %d\n", r.RetrievalQuality.VerifiedDetoursOffered)
	fmt.Fprintf(&b, "  Known packages:              %d\n", r.RetrievalQuality.KnownPackages)
	fmt.Fprintf(&b, "  Local cache:                 %.1f MB\n", float64(r.RetrievalQuality.CacheBytes)/(1<<20))
	fmt.Fprintf(&b, "  Automatic evidence sent:     %d batches\n", r.RetrievalQuality.EvidenceBatchesSent)
	fmt.Fprintf(&b, "  Origin seeds:                %d\n", r.RetrievalQuality.OriginSeeds)
	fmt.Fprintf(&b, "  Cross verifications:         %d\n", r.RetrievalQuality.CrossVerifications)

	passHuman := "— (no build reports yet)"
	if r.OutcomeValue.PostHitBuildReports > 0 {
		passHuman = fmt.Sprintf("%.1f%% (%d reports)",
			r.OutcomeValue.PostHitBuildPassRate*100, r.OutcomeValue.PostHitBuildReports)
	}

	fmt.Fprintf(&b, "\nLayer 2 · User & Agent Outcome Value (core product value):\n")
	fmt.Fprintf(&b, "  Adoptions:                   %d\n", r.OutcomeValue.Adoptions)
	fmt.Fprintf(&b, "  Post-hit build pass:         %s\n", passHuman)
	fmt.Fprintf(&b, "  Reported failures avoided:   %d  (exact match + detour + applied + PASS)\n",
		r.OutcomeValue.ReportedFailuresAvoided)
	fmt.Fprintf(&b, "  Estimated reasoning avoided: %d  (Estimated — never measured)\n",
		r.OutcomeValue.EstimatedReasoningAvoided)

	return b.String()
}
