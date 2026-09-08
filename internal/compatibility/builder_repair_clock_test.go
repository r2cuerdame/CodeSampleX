package compatibility

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type persistentScopedFailureStore struct {
	*serverstore.Fake
	failure    error
	fullReads  int
	onFullRead func()
}

func (s *persistentScopedFailureStore) BuilderChangesSince(context.Context, time.Time) (serverstore.Changes, error) {
	return serverstore.Changes{}, s.failure
}
func (s *persistentScopedFailureStore) ListBuilderSnapshotTargets(context.Context, []serverstore.BuilderPackage) ([]serverstore.SnapshotTarget, serverstore.BuilderReadMetrics, error) {
	return nil, serverstore.BuilderReadMetrics{}, s.failure
}
func (s *persistentScopedFailureStore) ListBuilderSamplesPage(context.Context, []serverstore.BuilderPackage, int, int) ([]serverstore.SampleRow, error) {
	return nil, s.failure
}
func (s *persistentScopedFailureStore) BuilderSnapshotKeys(context.Context, []serverstore.BuilderPackage) ([]serverstore.SnapshotTarget, error) {
	return nil, s.failure
}
func (s *persistentScopedFailureStore) ListSnapshotTargets(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	s.fullReads++
	if s.onFullRead != nil {
		s.onFullRead()
	}
	return s.Fake.ListSnapshotTargets(ctx)
}

func TestPersistentScopedFailureCannotStarveHourlyFullRepair(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	now := start
	stamp := start.Add(-5 * time.Minute)
	failure := errors.New("ambiguous source projection")
	store := &persistentScopedFailureStore{Fake: serverstore.NewFake(), failure: failure}
	b := &Builder{Store: store, Now: func() time.Time { return now }, lastRun: stamp, passes: 1}
	for _, elapsed := range []time.Duration{0, 10 * time.Minute, 59 * time.Minute} {
		now = start.Add(elapsed)
		if err := b.RunOnce(ctx); !errors.Is(err, failure) {
			t.Fatalf("elapsed=%s error=%v", elapsed, err)
		}
		if b.passes != 1 || !b.lastRun.Equal(stamp) || store.fullReads != 0 {
			t.Fatalf("failed scoped pass advanced completion: passes=%d lastRun=%s fullReads=%d", b.passes, b.lastRun, store.fullReads)
		}
		if !b.fullRepairAt.Equal(start.Add(time.Hour)) {
			t.Fatalf("failure postponed repair: %s", b.fullRepairAt)
		}
	}
	// The scheduled repair must use the exhaustive source methods despite
	// the persistent scoped failure, and a slow pass resets its next due time
	// from completion rather than from its stale start timestamp.
	now = start.Add(time.Hour)
	completion := now.Add(90 * time.Minute)
	store.onFullRead = func() { now = completion }
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("scheduled full repair: %v", err)
	}
	if store.fullReads != 1 || b.passes != 2 || !b.lastRun.Equal(start.Add(time.Hour)) {
		t.Fatalf("repair completion: fullReads=%d passes=%d lastRun=%s", store.fullReads, b.passes, b.lastRun)
	}
	if !b.fullRepairAt.Equal(completion.Add(time.Hour)) {
		t.Fatalf("next repair due=%s", b.fullRepairAt)
	}
	now = completion.Add(5 * time.Minute)
	if err := b.RunOnce(ctx); !errors.Is(err, failure) {
		t.Fatalf("next tick should be incremental: %v", err)
	}
	if store.fullReads != 1 || b.passes != 2 {
		t.Fatal("slow full repair immediately scheduled another full pass")
	}
}
