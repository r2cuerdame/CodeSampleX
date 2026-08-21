package mcp

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A hit's finding — the belief its contract contradicts — was printed inside
// the contract block, below the match grade, the deltas, the evidence counts
// and the failure clusters. That is the one line that changes what the model
// writes next, and it sat behind everything a reader skims past.
func TestSearchHitLeadsWithTheFinding(t *testing.T) {
	text := renderSearchResponse(domain.SearchResponse{Results: []domain.SearchResult{{
		Grade:      domain.GradeExact,
		Confidence: "HIGH",
		SampleID:   "sha256:aaa",
		Exact:      []string{"npm axios@1.12.0"},
		Different:  []string{"linux vs windows"},
		Evidence:   domain.EvidenceSummary{ContractPasses: 2, ProjectCompileObservations: 40},
		Case: &domain.Case{
			Goal:     "post json with a timeout",
			Packages: []string{"pkg:npm/axios@1.12.0"},
			Believed: "a timeout of five covers the whole request",
			Contract: []string{"connect, read and write each get their own five seconds"},
		},
	}}})

	iFinding := strings.Index(text, "a timeout of five covers the whole request")
	if iFinding < 0 {
		t.Fatal("the finding is missing from the hit")
	}
	// Section HEADERS, not the bare words: the answer now opens by saying
	// what this network verified, and that sentence points at the Different
	// section by name. Matching the word found the pointer rather than the
	// section and read the order backwards.
	for _, later := range []string{"\nEvidence\n", "\nDifferent\n", "\nGoal: "} {
		if i := strings.Index(text, later); i >= 0 && iFinding > i {
			t.Errorf("the finding is below %q: finding=%d %s=%d", later, iFinding, later, i)
		}
	}
	// And it is stated once, not once at the top and again in the contract.
	if n := strings.Count(text, "a timeout of five covers the whole request"); n != 1 {
		t.Errorf("the finding is printed %d times, want 1", n)
	}
}
