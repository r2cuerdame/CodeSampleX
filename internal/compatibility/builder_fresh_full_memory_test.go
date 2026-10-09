package compatibility

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Count the package evidence retained before the first snapshot is written.
// Bounded SQL pages alone cannot bound heap if every page is kept for a full corpus.
type freshFullEvidenceStore struct {
	*bulkReadStore
	packagesRead          map[string]bool
	firstSnapshotPackages int
}

func (s *freshFullEvidenceStore) EvidenceForTargets(ctx context.Context, targets []serverstore.SnapshotTarget) (map[serverstore.SnapshotTarget][]serverstore.EvidenceRow, error) {
	for _, target := range targets {
		p, err := domain.ParsePURL(target.PURL)
		if err != nil {
			return nil, err
		}
		s.packagesRead[p.Ecosystem+"/"+p.Name] = true
	}
	return s.bulkReadStore.EvidenceForTargets(ctx, targets)
}

func (s *freshFullEvidenceStore) noteSnapshot() {
	if s.firstSnapshotPackages < 0 {
		s.firstSnapshotPackages = len(s.packagesRead)
	}
}

func (s *freshFullEvidenceStore) PutSnapshots(ctx context.Context, rows []serverstore.SnapshotRow) error {
	s.noteSnapshot()
	for _, row := range rows {
		if err := s.Fake.PutSnapshot(ctx, row.PURL, row.Symbol, row.SnapshotJSON); err != nil {
			return err
		}
	}
	return nil
}

func (s *freshFullEvidenceStore) PutSnapshot(ctx context.Context, purl, symbol, raw string) error {
	s.noteSnapshot()
	return s.Fake.PutSnapshot(ctx, purl, symbol, raw)
}

func TestFreshHourlyFullBoundsEvidenceAndPreservesAllOutputs(t *testing.T) {
	ctx := context.Background()
	fake := serverstore.NewFake()
	corpus := seedBulkCorpus(t, fake, defaultRepairChunk+5, 0)
	store := &freshFullEvidenceStore{
		bulkReadStore: &bulkReadStore{newReadCounter(fake)},
		packagesRead:  map[string]bool{}, firstSnapshotPackages: -1,
	}
	// A completed fresh pass followed by its normal scheduled exhaustive repair,
	// exactly the production path that bypassed the stale-repair chunk bound.
	b := &Builder{Store: store, Now: func() time.Time { return testNow },
		lastRun: testNow.Add(-time.Minute), lastCompletedAt: testNow.Add(-time.Minute),
		fullRepairAt: testNow, passes: 1, statusLoaded: true,
	}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.firstSnapshotPackages <= 0 || store.firstSnapshotPackages > defaultRepairChunk {
		t.Fatalf("fresh hourly full retained evidence for %d packages before its first snapshot, want at most existing chunk bound %d",
			store.firstSnapshotPackages, defaultRepairChunk)
	}
	if len(store.packagesRead) != len(corpus.npmNames) {
		t.Fatalf("full repair read %d packages, want all %d", len(store.packagesRead), len(corpus.npmNames))
	}
	status := readBuilderStatus(t, fake)
	if status.LastPassOutcome != PassOutcomeSuccess || !status.LastPassFull || status.Repair != nil || b.repair != nil {
		t.Fatalf("bounded full did not complete its real durable pass: %+v", status)
	}
	// Compare to the original uninterrupted exhaustive materializer, including
	// snapshots, shards, failure clusters, package rows, and verification jobs.
	baselineFake := serverstore.NewFake()
	baselineCorpus := seedBulkCorpus(t, baselineFake, defaultRepairChunk+5, 0)
	if fmt.Sprint(baselineCorpus) != fmt.Sprint(corpus) {
		t.Fatal("comparison corpus differs")
	}
	baseline := &Builder{Store: &bulkReadStore{newReadCounter(baselineFake)}, Now: func() time.Time { return testNow }}
	phases := baseline.newPhaseRecorder(ctx)
	baselineCtx := withBuilderPhaseRecorder(ctx, phases)
	if _, err := baseline.materialize(baselineCtx, phases, nil, testNow, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := baseline.refreshStats(baselineCtx, testNow); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readOutputs(t, fake), readOutputs(t, baselineFake)) ||
		materializedState(t, fake, corpus) != materializedState(t, baselineFake, corpus) {
		t.Fatal("bounded fresh full changed the exhaustive materialized outputs")
	}
	t.Logf("fresh full: first output after %d/%d packages; all outputs match uninterrupted full", store.firstSnapshotPackages, len(corpus.npmNames))
}
