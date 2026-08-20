package compatibility

import (
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func presenceEnv() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
		Runtime: "node", RuntimeVersion: "22", ExecutionContext: "node",
	}
}

func presenceRow(stage, result string, n int64, now time.Time) serverstore.EvidenceRow {
	return serverstore.EvidenceRow{
		PURL: "pkg:npm/axios@1.12.0", Stage: stage, Result: result,
		ObservationCount: n, UniquePeerBuckets: 2, EnvHash: presenceEnv().Hash(),
		EnvJSON: string(domain.MustCanonicalJSON(presenceEnv())), LastSeen: now,
	}
}

// USED records that a package was installed. It has no failing form, so every
// one of them is a weighted PASS in the confidence computation — and the
// confidence computation is where the pass rate, the confidence tier and the
// elevated-failure flag all come from.
//
// Three quarters of these runs failed. Folding in the presence records made
// it read as 25% failing, and the flag that surfaces a failure cluster needs
// 25% to fire, so the evidence of a real problem was being diluted by the
// count of people who had the dependency installed.
func TestPresenceRecordsDoNotDiluteTheFailureSignal(t *testing.T) {
	now := time.Now().UTC()
	snap := BuildSnapshot("pkg:npm/axios@1.12.0", "", []serverstore.EvidenceRow{
		presenceRow("PROJECT_TEST", "PASS", 2, now),
		presenceRow("PROJECT_TEST", "FAIL", 6, now),
		presenceRow("USED", "PASS", 100, now),
	}, nil, nil, now)

	if len(snap.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(snap.Rows))
	}
	row := snap.Rows[0]
	if got := int(row.PassRate*100 + 0.5); got != 25 {
		t.Errorf("pass rate = %d%%, want 25%% — 2 of 8 runs passed", got)
	}
	if !row.ElevatedFailure {
		t.Error("a 75% failure rate did not raise the flag: presence records diluted it")
	}
	// The presence records are still recorded, just not as outcomes.
	if row.ByStage["USED"].Pass != 100 {
		t.Errorf("USED tally = %+v, want the 100 kept", row.ByStage["USED"])
	}
}

// A package that is only ever installed and never exercised has no pass rate
// to report, and inventing 100% from presence alone is the strongest possible
// claim made from the weakest possible evidence.
func TestPresenceAloneYieldsNoPassRate(t *testing.T) {
	now := time.Now().UTC()
	snap := BuildSnapshot("pkg:npm/axios@1.12.0", "", []serverstore.EvidenceRow{
		presenceRow("USED", "PASS", 229, now),
	}, nil, nil, now)

	if len(snap.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(snap.Rows))
	}
	if got := snap.Rows[0].PassRate; got != 0 {
		t.Errorf("pass rate = %v, want 0: nothing ran", got)
	}
	if snap.Rows[0].ElevatedFailure {
		t.Error("presence alone raised a failure flag")
	}
}

// Presence still says who reported and when. Skipping the weight must not
// skip the coordinate's own bookkeeping: a package known only through USED
// still has real reporters behind it and a real last-seen date.
func TestPresenceStillCarriesItsReportersAndDate(t *testing.T) {
	now := time.Now().UTC()
	snap := BuildSnapshot("pkg:npm/axios@1.12.0", "", []serverstore.EvidenceRow{
		presenceRow("USED", "PASS", 229, now),
	}, nil, nil, now)

	row := snap.Rows[0]
	if row.UniquePeerBuckets != 2 {
		t.Errorf("peer buckets = %d, want 2", row.UniquePeerBuckets)
	}
	if row.LastSeen == "" {
		t.Error("last seen was dropped with the weight")
	}
}
