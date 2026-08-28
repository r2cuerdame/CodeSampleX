package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

// countingTargets records how often the materialized page-key read is issued.
// Web pages must not call ListSnapshotTargets: that is the builder's expensive
// source inventory and includes rows whose snapshot page does not exist yet.
type countingTargets struct {
	*serverstore.Fake
	mu    sync.Mutex
	calls int
}

func (c *countingTargets) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.Fake.SnapshotKeys(ctx)
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
	ctx := serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive)
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
		t.Errorf("SnapshotKeys called %d times for one cube assembly, want 1", got)
	}
}

// Concurrent readers must not each issue the read: on the production host the
// pool is eight connections, so a stampede is what turns a slow page into a
// stalled server.
func TestSnapshotTargetsCollapsesConcurrentReaders(t *testing.T) {
	ctx := serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive)
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
		t.Errorf("SnapshotKeys called %d times for 16 concurrent readers, want 1", got)
	}
}

type divergentTargetStore struct{ *serverstore.Fake }

func (s *divergentTargetStore) ListSnapshotTargets(context.Context) ([]serverstore.SnapshotTarget, error) {
	return []serverstore.SnapshotTarget{
		{PURL: "pkg:npm/materialized@1.0.0", Symbol: "materialized.call"},
		{PURL: "pkg:npm/not-built-yet@1.0.0", Symbol: "future.call"},
	}, nil
}

// The builder's source inventory can lead the materialized table between
// passes. A future target is work to do, not a public page or a hot package.
func TestWebInventoryUsesOnlyMaterializedSnapshotKeys(t *testing.T) {
	store := &divergentTargetStore{Fake: serverstore.NewFake()}
	if err := store.PutSnapshot(t.Context(), "pkg:npm/materialized@1.0.0", "materialized.call", `{}`); err != nil {
		t.Fatal(err)
	}
	w := &webStore{s: store}

	ctx := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)
	hits, err := w.rankedPackages(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Name != "materialized" {
		t.Fatalf("web package inventory = %+v; an unbuilt target must not become a page", hits)
	}
}

type laneSnapshotTargets struct {
	*serverstore.Fake
	mu                sync.Mutex
	calls             int
	backgroundStarted chan struct{}
	backgroundRelease chan struct{}
}

func (s *laneSnapshotTargets) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		close(s.backgroundStarted)
		select {
		case <-s.backgroundRelease:
			return []serverstore.SnapshotTarget{{PURL: "pkg:npm/background@1.0.0"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []serverstore.SnapshotTarget{{PURL: "pkg:npm/interactive@2.0.0"}}, nil
}

func (s *laneSnapshotTargets) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Unclassified work is background by contract. A hero cube reaches this
// adapter through PackageVersions, so that indirect path must bypass the
// foreground target-cache mutex just as the direct hot-package refresh does.
func TestBackgroundPackageVersionsDoesNotOwnInteractiveTargetCache(t *testing.T) {
	store := &laneSnapshotTargets{
		Fake:              serverstore.NewFake(),
		backgroundStarted: make(chan struct{}),
		backgroundRelease: make(chan struct{}),
	}
	w := &webStore{s: store}
	released := false
	defer func() {
		if !released {
			close(store.backgroundRelease)
		}
	}()
	backgroundDone := make(chan error, 1)
	go func() {
		_, err := w.PackageVersions(context.Background(), "npm", "background")
		backgroundDone <- err
	}()
	select {
	case <-store.backgroundStarted:
	case <-time.After(time.Second):
		t.Fatal("background target read did not start")
	}

	type searchResult struct {
		rows []web.PackageHit
		err  error
	}
	interactive := serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive)
	result := make(chan searchResult, 1)
	go func() {
		rows, err := w.SearchPackages(interactive, "interactive", 10)
		result <- searchResult{rows: rows, err: err}
	}()
	select {
	case got := <-result:
		if got.err != nil || len(got.rows) != 1 || got.rows[0].Name != "interactive" {
			t.Fatalf("interactive search during background read = %+v, err=%v", got.rows, got.err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("interactive target cache waited on an unclassified background read")
	}
	if got := store.count(); got != 2 {
		t.Fatalf("SnapshotKeys calls before release = %d, want independent background and interactive lanes", got)
	}

	close(store.backgroundRelease)
	released = true
	select {
	case err := <-backgroundDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("background PackageVersions did not finish after release")
	}
}
