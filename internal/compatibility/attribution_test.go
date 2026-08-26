package compatibility

import (
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func attrRow(stage, result, code string, n int64, now time.Time) serverstore.EvidenceRow {
	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
		Runtime: "node", RuntimeVersion: "22",
	}
	return serverstore.EvidenceRow{
		PURL: "pkg:npm/negotiator@1.0.0", Stage: stage, Result: result,
		ErrorCode: code, ErrorFingerprint: "fp", ObservationCount: n,
		UniquePeerBuckets: 1, EnvHash: env.Hash(),
		EnvJSON: string(domain.MustCanonicalJSON(env)), LastSeen: now,
	}
}

// An observation failure without an error code says a build containing this
// package broke, and nothing about which package broke it: one tsc failure
// wrote a FAIL row for all 412 packages in the lockfile, under one
// fingerprint. That is what USAGE_OBSERVATION means — co-occurrence, not
// execution proof — and it is why 82% of production's failures carry no code.
//
// The rate keeps them: the build really did fail. What the snapshot has to
// carry is how many of those failures anyone could attribute, so a reader can
// tell a package that breaks from a package that was merely present.
func TestSnapshotCarriesHowManyFailuresWereAttributed(t *testing.T) {
	now := time.Now().UTC()
	snap := BuildSnapshot("pkg:npm/negotiator@1.0.0", "", []serverstore.EvidenceRow{
		attrRow("PROJECT_TEST", "PASS", "", 30, now),
		func() serverstore.EvidenceRow {
			r := attrRow("PROJECT_TEST", "FAIL", "ERR_REQUIRE_ESM", 2, now)
			r.EvidenceQuality = string(domain.EvidenceComplete)
			return r
		}(),
		attrRow("PROJECT_TEST", "FAIL", "", 8, now),
	}, nil, nil, now)

	if len(snap.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(snap.Rows))
	}
	sc := snap.Rows[0].ByStage["PROJECT_TEST"]
	if sc.Pass != 30 || sc.Fail != 10 {
		t.Errorf("stage = %+v, want 30 pass / 10 fail — uncoded failures still count", sc)
	}
	if sc.FailAttributed != 2 {
		t.Errorf("attributed failures = %d, want 2", sc.FailAttributed)
	}
	// And the rate is unchanged by the split: 30 of 40.
	if got := int(snap.Rows[0].PassRate*100 + 0.5); got != 75 {
		t.Errorf("pass rate = %d%%, want 75%%", got)
	}
}
