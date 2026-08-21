package mcp

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func hit(diff []string, passes int, status string) domain.SearchResponse {
	return domain.SearchResponse{Results: []domain.SearchResult{{
		Grade: domain.GradeReferenceOnly, SampleID: "sha256:aaa",
		SampleStatus: status, Different: diff,
		Exact:    []string{"yaml 2.9", "arch x64"},
		Evidence: domain.EvidenceSummary{ContractPasses: 2, IndependentCrossPeers: 2},
	}}}
}

// What this network knows is that it ran the contract and it passed, and
// exactly where. Whether the sample also runs on the caller's platform is
// not something it measured, and not its to say.
//
// The answer led with a verdict about the caller's environment — MATCH:
// REFERENCE_ONLY over a CROSS_PASS sample with two signed contract receipts —
// and put the fact that the network had verified it at all under "Evidence",
// below the deltas. Every windows caller reads that, because every verifier
// is a linux container: the one thing the network owns outright was the one
// thing it buried.
func TestTheAnswerLeadsWithTheOnlyThingOffered(t *testing.T) {
	out := renderSearchResponse(hit([]string{"os windows (sample: linux)"}, 2, "CROSS_PASS"))

	v := strings.Index(out, "BUILT:")
	if v < 0 {
		t.Fatalf("the answer never states the one thing on offer: that the sample built:\n%s", out)
	}
	if m := strings.Index(out, "MATCH:"); m >= 0 && m < v {
		t.Error("a grade about the caller's environment comes before the fact that it built")
	}
	if !strings.Contains(out, "2") {
		t.Error("the line does not say how many times it built")
	}
	// The ladder grades, and a grade is the one thing this network does not
	// offer — half of production's CROSS_PASS labels do not hold under the
	// rule that grants them.
	if strings.Contains(out, "CROSS_PASS") {
		t.Error("the answer still shows a status grade")
	}
}

// And it says plainly whose question the rest is.
func TestTheAnswerSaysThePlatformQuestionIsNotItsToAnswer(t *testing.T) {
	out := renderSearchResponse(hit([]string{"os windows (sample: linux)"}, 2, "CROSS_PASS"))
	if !strings.Contains(out, "not something this network measured") {
		t.Errorf("the answer does not hand the platform question back:\n%s", out)
	}
}

// With nothing differing there is no platform question to hand back.
func TestNoDeltaMeansNoDisclaimer(t *testing.T) {
	out := renderSearchResponse(hit(nil, 2, "CROSS_PASS"))
	if strings.Contains(out, "not something this network measured") {
		t.Error("hedged about an environment that does not differ")
	}
}
