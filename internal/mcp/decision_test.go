package mcp

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A sample this network verified is a verified sample. Where it runs on every
// other platform is not something the network measured and not something it
// can say — so it says what it did: it ran the contract, here, and it passed.
//
// The decision line said the opposite. Every yaml sample answering a windows
// caller came back "REFERENCE_ONLY — it is not verified for this
// environment", about CROSS_PASS samples with signed contract receipts. The
// sample IS verified; what differs is the caller's environment, and that is
// the caller's to weigh — the delta is handed over for exactly that reason.
func TestAVerifiedSampleIsNotCalledUnverified(t *testing.T) {
	r := domain.SearchResult{
		Grade:        domain.GradeReferenceOnly,
		SampleID:     "sha256:aaa",
		Different:    []string{"os windows (sample: linux)"},
		Evidence:     domain.EvidenceSummary{ContractPasses: 2},
		SampleStatus: "CROSS_PASS",
	}
	got := renderDecision(r)
	if strings.Contains(got, "not verified") {
		t.Errorf("decision = %q, but this network verified the sample itself", got)
	}
	if !strings.Contains(got, "REVERIFY") {
		t.Errorf("decision = %q, want the caller told to verify HERE, not told it is unverified", got)
	}
}

// A sample with no contract pass at all is a different thing: nothing ran,
// so there is nothing for this network to stand behind and no verification
// line to print.
func TestASampleWithNoContractPassClaimsNoVerification(t *testing.T) {
	r := domain.SearchResult{
		Grade:    domain.GradeReferenceOnly,
		SampleID: "sha256:bbb",
		Evidence: domain.EvidenceSummary{ContractPasses: 0},
	}
	if got := renderVerified(r); got != "" {
		t.Errorf("renderVerified = %q, want nothing: no contract ever ran", got)
	}
}
