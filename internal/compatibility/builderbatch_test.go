package compatibility

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type snapshotBatchRecordingStore struct {
	*serverstore.Fake
	targets   []serverstore.SnapshotTarget
	batches   [][]serverstore.SnapshotRow
	singles   int
	failBatch int
}

func (s *snapshotBatchRecordingStore) ListSnapshotTargets(context.Context) ([]serverstore.SnapshotTarget, error) {
	return append([]serverstore.SnapshotTarget(nil), s.targets...), nil
}

func (s *snapshotBatchRecordingStore) PutSnapshot(ctx context.Context, purl, symbol, snapshotJSON string) error {
	s.singles++
	return s.Fake.PutSnapshot(ctx, purl, symbol, snapshotJSON)
}

func (s *snapshotBatchRecordingStore) PutSnapshots(ctx context.Context, rows []serverstore.SnapshotRow) error {
	batch := append([]serverstore.SnapshotRow(nil), rows...)
	s.batches = append(s.batches, batch)
	if s.failBatch > 0 && len(s.batches) == s.failBatch {
		return errors.New("scripted snapshot batch failure")
	}
	for _, row := range rows {
		if err := s.Fake.PutSnapshot(ctx, row.PURL, row.Symbol, row.SnapshotJSON); err != nil {
			return err
		}
	}
	return nil
}

func snapshotTargets(n int) []serverstore.SnapshotTarget {
	rows := make([]serverstore.SnapshotTarget, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, serverstore.SnapshotTarget{
			PURL: fmt.Sprintf("pkg:npm/bounded-%04d@1.0.0", i), Symbol: "run",
		})
	}
	return rows
}

type clusterBatchRecordingStore struct {
	*serverstore.Fake
	batches [][]serverstore.ClusterRow
	singles int
}

func (s *clusterBatchRecordingStore) UpsertFailureCluster(ctx context.Context, row serverstore.ClusterRow) error {
	s.singles++
	return s.Fake.UpsertFailureCluster(ctx, row)
}

func (s *clusterBatchRecordingStore) UpsertFailureClusters(ctx context.Context, rows []serverstore.ClusterRow) error {
	batch := append([]serverstore.ClusterRow(nil), rows...)
	s.batches = append(s.batches, batch)
	return s.Fake.UpsertFailureClusters(ctx, rows)
}

// A package aggregate is one consistency unit and therefore one durable
// write. The old row-at-a-time path paid 1,161 commits for a representative
// production package even though its indexed evidence read took 232ms.
func TestBuilderBatchesFailureClustersPerPackage(t *testing.T) {
	ctx := context.Background()
	store := &clusterBatchRecordingStore{Fake: serverstore.NewFake()}
	store.NowFn = func() time.Time { return testNow }
	seedBuilderFixture(t, store.Fake)

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.singles != 0 {
		t.Fatalf("single cluster writes = %d, want 0", store.singles)
	}
	if len(store.batches) != 1 {
		t.Fatalf("cluster batches = %d, want one package batch", len(store.batches))
	}
	if len(store.batches[0]) != 1 {
		t.Fatalf("clusters in package batch = %d, want 1", len(store.batches[0]))
	}
	rows, err := store.ListFailureClusters(ctx, "axios")
	if err != nil || len(rows) != 1 {
		t.Fatalf("materialized clusters = %d, err=%v", len(rows), err)
	}
}

func TestBuilderBatchesSnapshotsInBoundedChunks(t *testing.T) {
	ctx := context.Background()
	store := &snapshotBatchRecordingStore{
		Fake: serverstore.NewFake(), targets: snapshotTargets(snapshotWriteBatch*2 + 1),
	}
	store.NowFn = func() time.Time { return testNow }
	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.singles != 0 {
		t.Fatalf("single snapshot writes = %d, want 0", store.singles)
	}
	want := []int{snapshotWriteBatch, snapshotWriteBatch, 1}
	if len(store.batches) != len(want) {
		t.Fatalf("snapshot batches = %d, want %d", len(store.batches), len(want))
	}
	for i, batch := range store.batches {
		if len(batch) != want[i] {
			t.Errorf("snapshot batch %d size = %d, want %d", i, len(batch), want[i])
		}
	}
}

func TestSnapshotBatchFailureDoesNotAdvancePass(t *testing.T) {
	ctx := context.Background()
	store := &snapshotBatchRecordingStore{
		Fake: serverstore.NewFake(), targets: snapshotTargets(snapshotWriteBatch + 1), failBatch: 2,
	}
	store.NowFn = func() time.Time { return testNow }
	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err == nil {
		t.Fatal("RunOnce succeeded after the final snapshot batch failed")
	}
	if b.passes != 0 || !b.lastRun.IsZero() {
		t.Fatalf("failed pass advanced state: passes=%d lastRun=%v", b.passes, b.lastRun)
	}
	if _, ok, err := store.GetLatestStats(ctx); err != nil || ok {
		t.Fatalf("failed pass wrote completion stats: ok=%v err=%v", ok, err)
	}
	keys, err := store.SnapshotKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != snapshotWriteBatch {
		t.Fatalf("durable snapshots = %d, want only first bounded chunk %d", len(keys), snapshotWriteBatch)
	}
}

type snapshotSingleRecordingStore struct {
	*serverstore.Fake
	targets []serverstore.SnapshotTarget
	singles int
}

func (s *snapshotSingleRecordingStore) ListSnapshotTargets(context.Context) ([]serverstore.SnapshotTarget, error) {
	return append([]serverstore.SnapshotTarget(nil), s.targets...), nil
}

func (s *snapshotSingleRecordingStore) PutSnapshot(ctx context.Context, purl, symbol, snapshotJSON string) error {
	s.singles++
	return s.Fake.PutSnapshot(ctx, purl, symbol, snapshotJSON)
}

func TestBuilderKeepsSingleWriteFallback(t *testing.T) {
	ctx := context.Background()
	store := &snapshotSingleRecordingStore{Fake: serverstore.NewFake(), targets: snapshotTargets(3)}
	store.NowFn = func() time.Time { return testNow }
	if err := (&Builder{Store: store, Now: func() time.Time { return testNow }}).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.singles != 3 {
		t.Fatalf("single snapshot writes = %d, want 3", store.singles)
	}
}
