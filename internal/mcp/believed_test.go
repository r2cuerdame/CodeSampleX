package mcp

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func believedResult(believed string) domain.SearchResult {
	return domain.SearchResult{
		SampleID: "sha256:aaaa",
		Case: &domain.Case{
			Goal:     "Set a per-phase timeout on an httpx request",
			Packages: []string{"pkg:pypi/httpx@0.28.1"},
			Contract: []string{"connect, read, write and pool each get their own 5 seconds"},
			Believed: believed,
		},
		Evidence: domain.EvidenceSummary{ContractPasses: 1},
	}
}

// The belief is the sentence that changes what the caller writes next, so
// it moved out of this block entirely and now leads the whole hit — above
// the match deltas, the evidence counts and the failure clusters, not merely
// above the contract lines at the bottom of them. What this block must not
// do is print it a second time.
func TestContractBlockNoLongerRepeatsTheBelief(t *testing.T) {
	out := contractBlock(believedResult("a timeout of 5 covers the whole request"))
	if strings.Contains(out, "a timeout of 5 covers the whole request") {
		t.Errorf("the belief is printed twice in one answer, got:\n%s", out)
	}
	if !strings.HasPrefix(out, "Proven by its contract") {
		t.Errorf("block = %q, want it to open with the proof", out)
	}
}

// Most samples state no belief. The block must read exactly as it did
// before the field existed rather than printing an empty label.
func TestContractBlockWithoutABeliefIsUnchanged(t *testing.T) {
	out := contractBlock(believedResult("   "))
	if strings.Contains(out, "Commonly assumed") {
		t.Errorf("a blank belief must print nothing, got:\n%s", out)
	}
	if !strings.HasPrefix(out, "Proven by its contract") {
		t.Errorf("block = %q, want it to open with the proof", out)
	}
}
