package compatibility

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func failRow(os, runtime, code, fp string, count int64, peers, projects int) serverstore.EvidenceRow {
	return serverstore.EvidenceRow{
		PURL: "pkg:npm/x@1.0.0", EnvHash: os + "|" + runtime,
		EnvJSON: `{"schemaVersion":1,"ecosystem":"npm","os":"` + os + `","arch":"x64",` +
			`"runtime":"node","runtimeVersion":"` + runtime + `"}`,
		Stage: "PROJECT_COMPILE", Result: "FAIL",
		ErrorFingerprint: fp, ErrorCode: code, ObservationCount: count,
		UniquePeerBuckets: peers, UniqueProjectBuckets: projects,
	}
}

// The same failure on two platforms is two facts. Collapsing the environment
// to the intersection across every failing row erased the one dimension that
// mattered: with windows and linux sharing a fingerprint, "os" disappeared,
// and the cell a reader actually wants -- windows / node 20 failed at COMPILE
// with ERR_REQUIRE_ESM -- existed nowhere.
func TestFailuresAreKeptPerEnvironment(t *testing.T) {
	got := failureSummaries([]serverstore.EvidenceRow{
		failRow("windows", "20.11", "ERR_REQUIRE_ESM", "sha256:aaa", 297, 184, 152),
		failRow("linux", "20.11", "ERR_REQUIRE_ESM", "sha256:aaa", 3, 2, 2),
	})
	if len(got) != 2 {
		t.Fatalf("summaries = %d, want one per environment: %+v", len(got), got)
	}
	for _, f := range got {
		if f.EnvSummary["os"] == "" {
			t.Errorf("summary lost the platform it happened on: %+v", f)
		}
	}
	if got[0].EnvSummary["os"] != "windows" {
		t.Errorf("first summary os = %q, want the most-reported platform first", got[0].EnvSummary["os"])
	}
}

// One machine building all afternoon inflates ObservationCount and says
// nothing about how widespread a failure is. Ordering by it puts a single
// looping developer above a fleet-wide break.
func TestFailuresCarryReportersAndRankByThem(t *testing.T) {
	got := failureSummaries([]serverstore.EvidenceRow{
		failRow("linux", "22", "LOOPING_LOCAL", "sha256:bbb", 5000, 1, 1),
		failRow("windows", "20.11", "WIDESPREAD", "sha256:ccc", 300, 184, 152),
	})
	if len(got) != 2 {
		t.Fatalf("summaries = %d, want 2", len(got))
	}
	if got[0].ErrorCode != "WIDESPREAD" {
		t.Errorf("ranked %q first; 5,000 reports from one machine is one data point",
			got[0].ErrorCode)
	}
	if got[0].Reporters != 184 || got[0].Projects != 152 {
		t.Errorf("summary = %d machines / %d projects, want 184/152", got[0].Reporters, got[0].Projects)
	}
	if got[1].Reporters != 1 {
		t.Errorf("the looping row reported %d machines, want the honest 1", got[1].Reporters)
	}
}

// Peak-within-an-epoch, never a sum across days: the dedup ledger is per
// epoch by design, and adding them would let one machine manufacture a fleet.
func TestReportersIsPeakNotSum(t *testing.T) {
	got := failureSummaries([]serverstore.EvidenceRow{
		failRow("windows", "20.11", "E", "sha256:ddd", 10, 3, 3),
		failRow("windows", "20.11", "E", "sha256:ddd", 10, 2, 2),
	})
	if len(got) != 1 {
		t.Fatalf("summaries = %d, want the two epochs folded into one cell", len(got))
	}
	if got[0].Reporters != 3 {
		t.Errorf("reporters = %d, want the peak 3 and never the sum 5", got[0].Reporters)
	}
	if got[0].Count != 20 {
		t.Errorf("count = %d, want observations summed even though reporters are not", got[0].Count)
	}
}
