package web

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The environment matrix counted USED in its Observations column, the same
// defect the compatibility grid and the network total both carried: presence
// records have no failing form, so a column headed "observations" was partly
// a count of installed dependencies.
func TestEnvironmentMatrixSeparatesPresenceFromRuns(t *testing.T) {
	rows := buildMatrix("en", snapshotDoc{Rows: []snapshotRow{{
		Env:      pvEnvForMatrix(),
		LastSeen: "2026-08-19T00:00:00Z",
		PassRate: 0.25,
		ByStage: map[string]stageCount{
			"PROJECT_TEST": {Pass: 2, Fail: 6},
			"USED":         {Pass: 100},
		},
	}}})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Observations != 8 {
		t.Errorf("observations = %d, want the 8 runs", rows[0].Observations)
	}
	if rows[0].Usage != 100 {
		t.Errorf("usage = %d, want the 100 presence records kept", rows[0].Usage)
	}
	// The stage breakdown still lists it — it is real, and a reader looking
	// at the detail line should see where the number came from.
	if !containsAll(rows[0].ObservedStages, "PROJECT_TEST") {
		t.Errorf("observed stages = %q", rows[0].ObservedStages)
	}
	if !containsAll(rows[0].UsageStages, "USED") {
		t.Errorf("usage stages = %q", rows[0].UsageStages)
	}
}

func containsAll(hay string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for i := 0; i+len(n) <= len(hay); i++ {
			if hay[i:i+len(n)] == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func pvEnvForMatrix() *domain.EnvironmentFingerprint {
	return &domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
		Runtime: "node", RuntimeVersion: "22", ExecutionContext: "node",
	}
}
