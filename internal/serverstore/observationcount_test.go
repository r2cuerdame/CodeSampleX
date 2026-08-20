package serverstore

import (
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The network's observation figure counts runs, and USED is not a run: it
// records that a package was present in a project, has no failing form, and
// carried 8,686 of the 42,808 package-level events in production. Counting
// it made "observations" partly a head count of installed dependencies.
func TestNetworkObservationsExcludePresenceRecords(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Now().UTC()

	record := func(stage string, n int64) {
		acc, rej, err := f.IngestBatches(ctx, []domain.ObservationBatch{{
			SchemaVersion:    1,
			Epoch:            now.Format("2006-01-02"),
			AnonID:           "anon-1",
			ProjectBucket:    "proj-1",
			Package:          "pkg:npm/axios@1.12.0",
			Stage:            domain.Stage(stage),
			Result:           domain.ResultPass,
			ObservationCount: int(n),
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
				Runtime: "node", RuntimeVersion: "22",
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if acc != 1 {
			t.Fatalf("stage %s: accepted=%d rejected=%+v", stage, acc, rej)
		}
	}
	record("PROJECT_TEST", 40)
	record("USED", 100)

	c, err := f.NetworkCounts(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if c.Observations != 40 {
		t.Errorf("observations = %d, want 40 — the 100 presence records are not runs", c.Observations)
	}
}
