package main

import (
	"context"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// countingTargets records how often the whole-network snapshot-target read is
// actually issued. That read is two unbounded queries — a DISTINCT over
// evidence_agg and a samples/receipts join whose JSONB predicates no index
// covers — so the count is a direct proxy for page latency.
type countingTargets struct {
	*serverstore.Fake
	mu    sync.Mutex
	calls int
}

func (c *countingTargets) ListSnapshotTargets(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.Fake.ListSnapshotTargets(ctx)
}

func (c *countingTargets) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// Assembling one package's cube asks for its versions once and its symbols
// once per version. Each of those went straight to the unbounded read, so a
// single package page paid seven of them and the landing page — six cubes —
// paid forty-three. They are all the same immutable answer within a request.
func TestSnapshotTargetsReadOncePerCacheWindow(t *testing.T) {
	ctx := context.Background()
	counting := &countingTargets{Fake: serverstore.NewFake()}
	w := &webStore{s: counting}

	if _, err := w.PackageVersions(ctx, "npm", "axios"); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		if _, err := w.PackageSymbols(ctx, "npm", "axios", v); err != nil {
			t.Fatal(err)
		}
	}
	if got := counting.count(); got != 1 {
		t.Errorf("ListSnapshotTargets called %d times for one cube assembly, want 1", got)
	}
}

// Concurrent readers must not each issue the read: on the production host the
// pool is eight connections, so a stampede is what turns a slow page into a
// stalled server.
func TestSnapshotTargetsCollapsesConcurrentReaders(t *testing.T) {
	ctx := context.Background()
	counting := &countingTargets{Fake: serverstore.NewFake()}
	w := &webStore{s: counting}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.PackageSymbols(ctx, "npm", "axios", "1.0.0"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := counting.count(); got != 1 {
		t.Errorf("ListSnapshotTargets called %d times for 16 concurrent readers, want 1", got)
	}
}
