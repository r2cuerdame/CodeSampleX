package compatibility

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

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
