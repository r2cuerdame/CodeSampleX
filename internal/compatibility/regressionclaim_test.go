package compatibility

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

var regEnv = domain.EnvironmentFingerprint{
	SchemaVersion: 1, OS: "linux", Arch: "x64", Ecosystem: "npm",
	Runtime: "node", RuntimeVersion: "22",
}

// §10.3 reads "version V shows failRate >= 0.25 while V-1 shows passRate
// >= 0.9 in the SAME environment bucket", and the candidate publishes V-1's
// measured pass rate. Every observation stage was pooled into one tally, so
// "V-1 passed" could be carried entirely by USED rows — a symbol appearing
// in source, recorded as a pass, which essentially never fails — while V's
// failures came from PROJECT_COMPILE.
//
// That published "1.11.0 passed 100% of 10 observations" about a version
// nothing in that bucket ever compiled, and the bias ran one way: the
// always-passing stage inflated exactly the half of the rule that gates the
// badge.
func TestRegressionDoesNotCompareOneStageAgainstAnother(t *testing.T) {
	prev := []serverstore.EvidenceRow{
		evRow("pkg:npm/axios@1.11.0", "get", regEnv, string(domain.StageUsed), string(domain.ResultPass), 10, "", "", 1),
	}
	cur := []serverstore.EvidenceRow{
		evRow("pkg:npm/axios@1.12.0", "get", regEnv, string(domain.StageProjectCompile), string(domain.ResultFail), 4, "", "", 1),
		evRow("pkg:npm/axios@1.12.0", "get", regEnv, string(domain.StageProjectCompile), string(domain.ResultPass), 1, "", "", 1),
	}

	got := DetectRegressions("pkg:npm/axios@1.12.0", "pkg:npm/axios@1.11.0", "get", cur, prev)
	if len(got) != 0 {
		t.Fatalf("claimed a regression against a stage the previous version was never measured at: %+v", got)
	}
}

// The same shape, measured at the same stage on both sides, must still be
// detected — the rule has to keep working.
func TestRegressionIsStillDetectedWithinOneStage(t *testing.T) {
	stage := string(domain.StageProjectCompile)
	prev := []serverstore.EvidenceRow{
		evRow("pkg:npm/axios@1.11.0", "get", regEnv, stage, string(domain.ResultPass), 10, "", "", 1),
	}
	cur := []serverstore.EvidenceRow{
		evRow("pkg:npm/axios@1.12.0", "get", regEnv, stage, string(domain.ResultFail), 4, "", "", 1),
		evRow("pkg:npm/axios@1.12.0", "get", regEnv, stage, string(domain.ResultPass), 1, "", "", 1),
	}

	got := DetectRegressions("pkg:npm/axios@1.12.0", "pkg:npm/axios@1.11.0", "get", cur, prev)
	if len(got) != 1 {
		t.Fatalf("a same-stage regression went undetected: %+v", got)
	}
	if got[0].Stage != stage {
		t.Errorf("candidate stage = %q, want %q — the claim has to say what it compared", got[0].Stage, stage)
	}
}

// A candidate is a claim about ONE version pair. The badge was keyed by
// symbol alone, so it spread over every failure cluster sharing that
// symbol: a cluster whose only version was 0.9.0 rendered
// "▲ regression candidate  0.9.0" from a 1.11→1.12 comparison that never
// looked at 0.9.0.
func TestRegressionBadgeStaysOnTheVersionItIsAbout(t *testing.T) {
	stage := string(domain.StageProjectCompile)
	rows := map[string][]serverstore.EvidenceRow{
		"0.9.0": {evRow("pkg:npm/axios@0.9.0", "get", regEnv, stage, string(domain.ResultFail), 3, "", "", 1)},
	}
	cands := []RegressionCandidate{{
		Package: "pkg:npm/axios@1.12.0", PreviousPackage: "pkg:npm/axios@1.11.0",
		Symbol: "get", Stage: stage, EnvBucketHash: "h",
	}}

	clusters := BuildClusters("npm", "axios", rows, cands, testNow)
	for _, c := range clusters {
		if c.RegressionCandidate && !strings.Contains(c.VersionsJSON, "1.12.0") {
			t.Errorf("regression badge on a cluster the comparison never involved: versions=%s", c.VersionsJSON)
		}
	}
}
