package main

// The records page must not reload the whole snapshot table on a visitor's
// request.
//
// Reported 2026-09-02 as "the 기록 menu is slow" and measured on production:
// compatibility_snapshots is 17,255 rows but 149MB of jsonb once serialised,
// and the full read ListSnapshots performs took 36.5s -- every page from
// cache, so CPU on two cores, not disk. The page cached that read for 30
// seconds and refreshed it SYNCHRONOUSLY under a mutex, so any visitor more
// than 30s after the last one paid the whole reload and everyone behind them
// queued on the lock; during a builder pass the read hit its statement
// timeout and the page came back empty (path=/compatibility
// cause=query_timeout). Measured: 7.5s cold, 1.0s and 1.8s warm.
//
// Snapshots change only when the builder writes them, once per tick at most,
// so a 30-second TTL bought nothing but reloads. The house idiom for this is
// HotPackages: hand back what is cached at once and refresh behind the
// request under a background-class context with its own budget. The one
// difference here is the cold start -- a records page must not render empty,
// so with nothing cached yet the first request still waits.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type snapshotCountingStore struct {
	serverstore.Store
	mu        sync.Mutex
	rows      []serverstore.SnapshotRow
	calls     atomic.Int64
	block     chan struct{} // closed to release; nil = do not block
	deadlines []time.Duration
}

func (s *snapshotCountingStore) ListSnapshots(ctx context.Context) ([]serverstore.SnapshotRow, error) {
	s.calls.Add(1)
	s.mu.Lock()
	if d, ok := ctx.Deadline(); ok {
		s.deadlines = append(s.deadlines, time.Until(d))
	} else {
		s.deadlines = append(s.deadlines, -1)
	}
	block, rows := s.block, append([]serverstore.SnapshotRow(nil), s.rows...)
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return rows, nil
}

func snapRow(purl string) serverstore.SnapshotRow {
	return serverstore.SnapshotRow{PURL: purl, Symbol: "", SnapshotJSON: `{"rows":[]}`}
}

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// The TTL is the builder's cadence, not a fraction of it. Anything shorter
// reloads 149MB for no new information.
func TestSnapshotCacheTTLIsAtLeastTheBuilderTick(t *testing.T) {
	const builderTick = 5 * time.Minute
	if recordSnapshotCacheTTL < builderTick {
		t.Fatalf("recordSnapshotCacheTTL = %v; snapshots only change when the builder writes them (every %v), "+
			"and a full reload is 36s of CPU on production", recordSnapshotCacheTTL, builderTick)
	}
}

// Inside the TTL the cached rows answer and the store is not read.
func TestSnapshotsInsideTheTTLAreNotReloaded(t *testing.T) {
	store := &snapshotCountingStore{Store: serverstore.NewFake(), rows: []serverstore.SnapshotRow{snapRow("pkg:npm/a@1")}}
	w := &webStore{s: store}

	if _, err := w.cachedSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.rows = []serverstore.SnapshotRow{snapRow("pkg:npm/b@1")}
	store.mu.Unlock()
	rows, err := w.cachedSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // asserting an absence: let a refresh that should not exist show up
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("the store was read %d times inside the TTL, want 1", got)
	}
	if len(rows) != 1 || rows[0].PURL != "pkg:npm/a@1" {
		t.Fatalf("rows changed inside the TTL: %+v", rows)
	}
}

// Past the TTL the request is answered at once from the old rows while one
// refresh runs behind it; the next request after the refresh sees the new
// rows. A second stale request joins the refresh rather than starting one.
func TestStaleSnapshotsAreServedAtOnceAndRefreshedOnceBehind(t *testing.T) {
	store := &snapshotCountingStore{Store: serverstore.NewFake(), rows: []serverstore.SnapshotRow{snapRow("pkg:npm/old@1")}}
	w := &webStore{s: store}
	if _, err := w.cachedSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Lapse the TTL and make the next read hang, as production's does.
	w.snapshotMu.Lock()
	w.snapshotAt = time.Now().Add(-recordSnapshotCacheTTL - time.Second)
	w.snapshotMu.Unlock()
	store.mu.Lock()
	store.rows = []serverstore.SnapshotRow{snapRow("pkg:npm/new@1")}
	store.block = make(chan struct{})
	store.mu.Unlock()

	started := time.Now()
	rows, err := w.cachedSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(started); waited > 200*time.Millisecond {
		t.Fatalf("a stale request waited %v on the reload; it must be served from the old rows at once", waited)
	}
	if len(rows) != 1 || rows[0].PURL != "pkg:npm/old@1" {
		t.Fatalf("stale request served %+v, want the old rows", rows)
	}
	if _, err := w.cachedSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return store.calls.Load() >= 2 }, "the refresh never started")
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("two stale requests started %d refreshes, want exactly 1", got-1)
	}

	close(store.block)
	waitUntil(t, func() bool {
		rows, err := w.cachedSnapshots(context.Background())
		return err == nil && len(rows) == 1 && rows[0].PURL == "pkg:npm/new@1"
	}, "the completed refresh never became the served rows")
}

// The refresh has its own budget. A request-bound context would cancel the
// 36-second read the moment the visitor's request finished.
func TestTheSnapshotRefreshIsNotBoundToTheRequest(t *testing.T) {
	store := &snapshotCountingStore{Store: serverstore.NewFake(), rows: []serverstore.SnapshotRow{snapRow("pkg:npm/old@1")}}
	w := &webStore{s: store}
	if _, err := w.cachedSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.snapshotMu.Lock()
	w.snapshotAt = time.Now().Add(-recordSnapshotCacheTTL - time.Second)
	w.snapshotMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := w.cachedSnapshots(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return store.calls.Load() >= 2 }, "the refresh never started")
	store.mu.Lock()
	refresh := store.deadlines[len(store.deadlines)-1]
	store.mu.Unlock()
	if refresh >= 0 && refresh < recordSnapshotRefreshTimeout/2 {
		t.Fatalf("the refresh was given %v; it inherited the request's deadline instead of its own %v budget",
			refresh, recordSnapshotRefreshTimeout)
	}
}

// With nothing cached there is nothing to serve, so the first request still
// waits for the read -- and still honours its own deadline.
func TestTheColdStartStillWaitsForTheFirstRead(t *testing.T) {
	store := &snapshotCountingStore{Store: serverstore.NewFake(), rows: []serverstore.SnapshotRow{snapRow("pkg:npm/a@1")}, block: make(chan struct{})}
	w := &webStore{s: store}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := w.cachedSnapshots(ctx); err == nil {
		t.Fatal("the cold first request returned without rows and without an error")
	}
}
