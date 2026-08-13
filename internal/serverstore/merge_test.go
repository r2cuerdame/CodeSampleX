package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func testEnv() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18",
	}
}

func obsBatch(anon, proj string, count int) domain.ObservationBatch {
	return domain.ObservationBatch{
		SchemaVersion:    1,
		Epoch:            "2026-08-13",
		AnonID:           anon,
		ProjectBucket:    proj,
		Package:          "pkg:npm/axios@1.12.0",
		Symbol:           "axios.post",
		SymbolConfidence: domain.SymbolProbable,
		Environment:      testEnv(),
		Stage:            domain.StageProjectCompile,
		Result:           domain.ResultPass,
		ObservationCount: count,
	}
}

// TestNetworkCountsPeersFollowEvidence pins what the headline "Peers"
// number means. It used to count rows in the P2P blob tracker, which a
// node only joins by opting into peerListen — so the site reported 0
// peers while evidence was arriving every minute from real users.
func TestNetworkCountsPeersFollowEvidence(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	epoch := now.UTC().Format("2006-01-02")

	batch := func(anonID, pkg string) domain.ObservationBatch {
		return domain.ObservationBatch{
			SchemaVersion: 1, Epoch: epoch, AnonID: anonID, ProjectBucket: "proj-" + anonID,
			Package: pkg, Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
			},
			Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 3,
		}
	}
	if _, _, err := f.IngestBatches(ctx, []domain.ObservationBatch{
		batch("peer-a", "pkg:npm/axios@1.12.0"),
		batch("peer-b", "pkg:npm/axios@1.12.0"),
		batch("peer-a", "pkg:npm/zod@4.1.12"), // same peer again: still one peer
	}); err != nil {
		t.Fatal(err)
	}

	c, err := f.NetworkCounts(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if c.Peers != 2 {
		t.Errorf("Peers = %d, want 2 (distinct contributing buckets today)", c.Peers)
	}
	if c.ServingPeers != 0 {
		t.Errorf("ServingPeers = %d, want 0 — nobody joined the blob tracker", c.ServingPeers)
	}

	// Yesterday's contributors do not pad today's number: buckets rotate
	// daily, so counting across days would multiply one person into many.
	if _, _, err := f.IngestBatches(ctx, []domain.ObservationBatch{
		batch("peer-c", "pkg:npm/axios@1.12.0"),
	}); err != nil {
		t.Fatal(err)
	}
	tomorrow := now.AddDate(0, 0, 1)
	if c2, err := f.NetworkCounts(ctx, tomorrow); err != nil {
		t.Fatal(err)
	} else if c2.Peers != 0 {
		t.Errorf("Peers on a later day = %d, want 0 — yesterday's buckets must not carry over", c2.Peers)
	}
}

func TestDeltaContribution(t *testing.T) {
	cases := []struct {
		name           string
		prev, incoming int64
		want           int64
	}{
		{"first report", 0, 5, 5},
		{"identical re-send", 5, 5, 0},
		{"grown epoch total", 5, 8, 3},
		{"shrunk report adds nothing", 8, 5, 0},
		{"negative incoming adds nothing", 0, -3, 0},
		{"zero incoming", 4, 0, 0},
	}
	for _, tc := range cases {
		if got := deltaContribution(tc.prev, tc.incoming); got != tc.want {
			t.Errorf("%s: deltaContribution(%d,%d) = %d, want %d",
				tc.name, tc.prev, tc.incoming, got, tc.want)
		}
	}
}

// TestMergeResendIdenticalAddsZero is the BINDING ingest semantic: a client
// re-sending the exact same batch (same epoch, same bucket, same count)
// must not inflate the aggregate.
func TestMergeResendIdenticalAddsZero(t *testing.T) {
	m := newMergeState()
	b := obsBatch("anonaaaa", "projaaaa", 5)

	if delta := m.apply(b); delta != 5 {
		t.Fatalf("first apply delta = %d, want 5", delta)
	}
	if delta := m.apply(b); delta != 0 {
		t.Fatalf("re-sent identical batch delta = %d, want 0", delta)
	}
	k := aggKeyOf(b)
	if got := m.observations[k]; got != 5 {
		t.Fatalf("observation_count = %d, want 5 after duplicate send", got)
	}
}

func TestMergeGrownCountAddsOnlyDelta(t *testing.T) {
	m := newMergeState()
	if delta := m.apply(obsBatch("anonaaaa", "projaaaa", 5)); delta != 5 {
		t.Fatalf("first delta = %d, want 5", delta)
	}
	// Same client's epoch total grew from 5 to 8: contributes 3, not 8.
	if delta := m.apply(obsBatch("anonaaaa", "projaaaa", 8)); delta != 3 {
		t.Fatalf("grown delta = %d, want 3", delta)
	}
	k := aggKeyOf(obsBatch("anonaaaa", "projaaaa", 8))
	if got := m.observations[k]; got != 8 {
		t.Fatalf("observation_count = %d, want 8", got)
	}
}

func TestMergeUniqueBuckets(t *testing.T) {
	m := newMergeState()
	m.apply(obsBatch("anonaaaa", "projaaaa", 5)) // new peer: +5
	m.apply(obsBatch("anonbbbb", "projaaaa", 2)) // second peer, same project: +2
	// Same peer bucket grew its epoch total 2→3 under a second project:
	// the peer bucket is the contribution ledger, so this adds only +1
	// while the new project bucket is still recorded.
	if delta := m.apply(obsBatch("anonbbbb", "projcccc", 3)); delta != 1 {
		t.Fatalf("same-peer growth delta = %d, want 1", delta)
	}

	k := aggKeyOf(obsBatch("anonaaaa", "projaaaa", 5))
	if got := m.observations[k]; got != 8 {
		t.Fatalf("observation_count = %d, want 8", got)
	}
	if got := len(m.peerBuckets[k]); got != 2 {
		t.Fatalf("unique peer buckets = %d, want 2", got)
	}
	if got := len(m.projectBuckets[k]); got != 2 {
		t.Fatalf("unique project buckets = %d, want 2", got)
	}
}

// Different epochs are independent dedup rows: the same bucket reporting in a
// new epoch starts from a zero previous contribution.
func TestMergeNewEpochStartsFresh(t *testing.T) {
	m := newMergeState()
	b := obsBatch("anonaaaa", "projaaaa", 5)
	m.apply(b)
	b2 := b
	b2.Epoch = "2026-08-14"
	if delta := m.apply(b2); delta != 5 {
		t.Fatalf("new-epoch delta = %d, want 5", delta)
	}
	k := aggKeyOf(b)
	if got := m.observations[k]; got != 10 {
		t.Fatalf("observation_count = %d, want 10 across two epochs", got)
	}
	if got := len(m.peerBuckets[k]); got != 1 {
		t.Fatalf("unique peer buckets = %d, want 1 (same bucket, two epochs)", got)
	}
}

// Distinct agg targets (different stage/result/error) never share counters.
func TestMergeSeparatesAggTargets(t *testing.T) {
	m := newMergeState()
	pass := obsBatch("anonaaaa", "projaaaa", 5)
	fail := pass
	fail.Result = domain.ResultFail
	fail.ObservationCount = 2
	m.apply(pass)
	m.apply(fail)
	if got := m.observations[aggKeyOf(pass)]; got != 5 {
		t.Fatalf("pass row = %d, want 5", got)
	}
	if got := m.observations[aggKeyOf(fail)]; got != 2 {
		t.Fatalf("fail row = %d, want 2", got)
	}
}
