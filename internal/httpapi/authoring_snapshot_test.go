package httpapi

// A poll must not pay for the whole-corpus read every time.
//
// Measured on production 2026-09-02 (#173). The expansion query reads ~700MB
// from disk per run -- two jsonb-wide tables of ~300MB each against a 320MB
// buffer cache on a 2GB host that also runs the server -- and took 249s
// against a 10s statement timeout AFTER its plan defect was removed. Three
// workers polled every 60s, so the database ran that scan to its timeout
// roughly thirty seconds of every minute (db CPU 396%, load 7.5) and no
// poll ever received expansion work.
//
// No rewrite makes a 600MB read finish in 10s on that box. What the box can
// do is read it ONCE, in the background under a budget sized for it, and
// hand every poll the last completed answer -- the pattern the builder
// already uses for shards.
//
// loadAuthoringCandidates used to refuse to cache "so a later poll sees newly
// verified work". Freshness is real, but the claim that follows a poll is
// authoritative: ClaimAuthoringWork inserts with ON CONFLICT DO NOTHING on
// the coordinate, so a candidate somebody else took since the snapshot
// yields no row and the worker moves to the next. Staleness costs a wasted
// attempt, never a duplicate assignment. Bounded by a TTL, it is the right
// trade.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// snapshotStore is a Fake whose expansion read can be blocked, counted, and
// asked what deadline it was given.
type snapshotStore struct {
	*serverstore.Fake
	mu        sync.Mutex
	rows      []serverstore.WantedRow
	calls     atomic.Int64
	block     chan struct{} // closed to release a blocked read; nil = do not block
	deadlines []time.Duration
}

func newSnapshotStore(rows ...serverstore.WantedRow) *snapshotStore {
	return &snapshotStore{Fake: serverstore.NewFake(), rows: rows}
}

func (s *snapshotStore) read(ctx context.Context) ([]serverstore.WantedRow, error) {
	s.calls.Add(1)
	s.mu.Lock()
	if d, ok := ctx.Deadline(); ok {
		s.deadlines = append(s.deadlines, time.Until(d))
	} else {
		s.deadlines = append(s.deadlines, -1)
	}
	block, rows := s.block, append([]serverstore.WantedRow(nil), s.rows...)
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

func (s *snapshotStore) ListAuthoringExpansionCandidates(ctx context.Context, _ int) ([]serverstore.WantedRow, error) {
	return s.read(ctx)
}

func (s *snapshotStore) ListAuthoringExpansionCandidatesUnhurried(ctx context.Context, _ int) ([]serverstore.WantedRow, error) {
	return s.read(ctx)
}

func (s *snapshotStore) setRows(rows ...serverstore.WantedRow) {
	s.mu.Lock()
	s.rows = rows
	s.mu.Unlock()
}

func expansionRow(name string) serverstore.WantedRow {
	return serverstore.WantedRow{Ecosystem: "npm", Name: name, Version: "1.0.0", Symbol: "run", Kind: "EXPANSION", Score: 5}
}

// Within the TTL, a completed snapshot answers the poll and the store is not
// read again.
func TestAPollWithinTheTTLIsServedFromTheLastSnapshot(t *testing.T) {
	store := newSnapshotStore(expansionRow("first"))
	clock := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	a := &api{d: Deps{Store: store, Now: func() time.Time { return clock }, authoringWorkTimeout: time.Second}}

	first, err := a.loadAuthoringCandidates(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("first poll read the store %d times, want 1", got)
	}

	clock = clock.Add(authoringCandidateTTL / 2)
	store.setRows(expansionRow("second")) // not visible until the TTL lapses
	again, err := a.loadAuthoringCandidates(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	// Asserting an absence: give a refresh that should not exist time to
	// show up. Without this window the assertion raced the goroutine and the
	// test passed with the TTL set to zero.
	time.Sleep(50 * time.Millisecond)
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("a poll inside the TTL read the store again (%d reads); the snapshot exists to prevent that", got)
	}
	if len(again.expansion) != 1 || again.expansion[0].Name != first.expansion[0].Name {
		t.Fatalf("the snapshot changed inside the TTL: %+v then %+v", first.expansion, again.expansion)
	}
}

// Past the TTL the poll is still answered at once, from the old snapshot,
// and exactly one refresh runs behind it. A second stale poll joins that
// refresh rather than starting another: that is the same coalescing the
// synchronous path always had.
func TestAStalePollIsServedAtOnceWhileOneRefreshRunsBehindIt(t *testing.T) {
	store := newSnapshotStore(expansionRow("old"))
	clock := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	a := &api{d: Deps{Store: store, Now: func() time.Time { return clock }, authoringWorkTimeout: time.Second}}
	if _, err := a.loadAuthoringCandidates(context.Background(), store); err != nil {
		t.Fatal(err)
	}

	// Lapse the TTL and make the next read hang, as production's does.
	clock = clock.Add(authoringCandidateTTL + time.Second)
	store.setRows(expansionRow("new"))
	store.mu.Lock()
	store.block = make(chan struct{})
	store.mu.Unlock()

	started := time.Now()
	stale, err := a.loadAuthoringCandidates(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(started); waited > 200*time.Millisecond {
		t.Fatalf("a stale poll waited %v on the refresh; it must be served from the old snapshot at once", waited)
	}
	if len(stale.expansion) != 1 || stale.expansion[0].Name != "old" {
		t.Fatalf("stale poll served %+v, want the old snapshot", stale.expansion)
	}

	// Another stale poll must not start a second scan.
	if _, err := a.loadAuthoringCandidates(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return store.calls.Load() >= 2 }, "the refresh never started")
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("two stale polls started %d refreshes, want exactly 1", got-1)
	}

	// Release the refresh; the next poll sees what it found.
	close(store.block)
	waitFor(t, func() bool {
		snap, err := a.loadAuthoringCandidates(context.Background(), store)
		return err == nil && len(snap.expansion) == 1 && snap.expansion[0].Name == "new"
	}, "the completed refresh never became the served snapshot")
}

// The refresh runs under its own budget, not the poll's. A 12-second poll
// ceiling is right for a request; it is exactly the ceiling that guaranteed
// the 249-second read could never complete, and a refresh bound to it would
// simply fail every five minutes instead of every poll.
func TestTheRefreshIsNotBoundToThePollDeadline(t *testing.T) {
	store := newSnapshotStore(expansionRow("old"))
	clock := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	a := &api{d: Deps{Store: store, Now: func() time.Time { return clock }, authoringWorkTimeout: time.Second}}
	if _, err := a.loadAuthoringCandidates(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(authoringCandidateTTL + time.Second)

	// The stale poll itself is on a short leash, as real polls are.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.loadAuthoringCandidates(ctx, store); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return store.calls.Load() >= 2 }, "the refresh never started")

	store.mu.Lock()
	refresh := store.deadlines[len(store.deadlines)-1]
	store.mu.Unlock()
	if refresh >= 0 && refresh < authoringCandidateRefreshBudget/2 {
		t.Fatalf("the refresh was given %v; it inherited the poll's deadline instead of its own %v budget",
			refresh, authoringCandidateRefreshBudget)
	}
}

// With no snapshot yet there is nothing to serve, and the first poll keeps
// the synchronous, deadline-bound behaviour the older tests pin. This is
// the case a fresh process is in, and it is why the first poll after a
// deploy can still be refused while every later one is not.
func TestTheFirstPollStillWaitsForTheFirstScan(t *testing.T) {
	store := newSnapshotStore(expansionRow("first"))
	store.block = make(chan struct{})
	a := &api{d: Deps{Store: store, authoringWorkTimeout: time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.loadAuthoringCandidates(ctx, store)
	if err == nil {
		t.Fatal("the first poll returned without a completed scan and without an error")
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
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
