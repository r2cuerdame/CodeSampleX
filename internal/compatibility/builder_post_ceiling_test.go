package compatibility

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type instrumentedCeilingStore struct {
	*serverstore.Fake
	fullReads   atomic.Int64
	scopedReads atomic.Int64
	blockOnFull atomic.Bool
	fullStarted chan struct{}
	releaseFull chan struct{}
}

func newInstrumentedCeilingStore() *instrumentedCeilingStore {
	return &instrumentedCeilingStore{
		Fake:        serverstore.NewFake(),
		fullStarted: make(chan struct{}, 1),
		releaseFull: make(chan struct{}),
	}
}

func (s *instrumentedCeilingStore) ListSnapshotTargets(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	s.fullReads.Add(1)
	if s.blockOnFull.Load() {
		select {
		case s.fullStarted <- struct{}{}:
		default:
		}
		select {
		case <-s.releaseFull:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Fake.ListSnapshotTargets(ctx)
}

func (s *instrumentedCeilingStore) ChangedSince(ctx context.Context, since time.Time) (serverstore.Changes, error) {
	s.scopedReads.Add(1)
	return s.Fake.ChangedSince(ctx, since)
}

// TestBuilderPostCeilingDoesNotEndlesslyRebuildCorpus proves that when an
// hourly full repair hits its ceiling, the next attempt resumes incrementally
// from lastRun rather than endlessly repeating whole-corpus rebuilds.
func TestBuilderPostCeilingDoesNotEndlesslyRebuildCorpus(t *testing.T) {
	start := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	now := start
	store := newInstrumentedCeilingStore()
	b := &Builder{
		Store: store,
		Now:   func() time.Time { return now },
	}

	// Pass 1: Cold start full rebuild.
	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatalf("pass 1 failed: %v", err)
	}
	if got := store.fullReads.Load(); got != 1 {
		t.Fatalf("pass 1 fullReads = %d, want 1", got)
	}
	if !b.lastRun.Equal(start) {
		t.Fatalf("pass 1 lastRun = %s, want %s", b.lastRun, start)
	}
	if !b.fullRepairAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("pass 1 fullRepairAt = %s, want %s", b.fullRepairAt, start.Add(time.Hour))
	}

	// Pass 2: Ordinary incremental tick at +5m.
	now = start.Add(5 * time.Minute)
	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatalf("pass 2 failed: %v", err)
	}
	if got := store.fullReads.Load(); got != 1 {
		t.Fatalf("pass 2 fullReads = %d, want 1 (no exhaustive read)", got)
	}
	if got := store.scopedReads.Load(); got != 1 {
		t.Fatalf("pass 2 scopedReads = %d, want 1", got)
	}
	if !b.lastRun.Equal(now) {
		t.Fatalf("pass 2 lastRun = %s, want %s", b.lastRun, now)
	}

	// Pass 3: Scheduled full repair at +1h wedges and hits the ceiling.
	now = start.Add(time.Hour)
	store.blockOnFull.Store(true)
	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelTimeout()

	err := b.RunOnce(timeoutCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pass 3 err = %v, want context.DeadlineExceeded", err)
	}
	if got := store.fullReads.Load(); got != 2 {
		t.Fatalf("pass 3 fullReads = %d, want 2", got)
	}
	// lastRun must remain pass 2's completed stamp.
	if !b.lastRun.Equal(start.Add(5 * time.Minute)) {
		t.Fatalf("pass 3 modified lastRun: got %s", b.lastRun)
	}
	// fullRepairAt must be postponed by 1 hour from pass 3's wall clock.
	expectedPostponed := now.Add(time.Hour)
	if !b.fullRepairAt.Equal(expectedPostponed) {
		t.Fatalf("pass 3 fullRepairAt = %s, want %s (postponed by 1h)", b.fullRepairAt, expectedPostponed)
	}

	// Pass 4: Next pass (retry or next interval) at +1h + 1s.
	// Since lastRun is 55m old (well within resumeWindow=24h) and fullRepairAt
	// was postponed, pass 4 must run INCREMENTALLY without taking a full read.
	store.blockOnFull.Store(false)
	now = start.Add(time.Hour + time.Second)
	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatalf("pass 4 failed: %v", err)
	}

	if got := store.fullReads.Load(); got != 2 {
		t.Fatalf("pass 4 fullReads = %d, want 2: pass after ceiling breach must resume incrementally without exhaustive read", got)
	}
	if got := store.scopedReads.Load(); got != 2 {
		t.Fatalf("pass 4 scopedReads = %d, want 2", got)
	}
	if !b.lastRun.Equal(now) {
		t.Fatalf("pass 4 lastRun = %s, want %s", b.lastRun, now)
	}
}

// TestBuilderPeriodicPassTimeoutResumesIncrementally proves that when pass 12
// (triggered by passes % fullPassEvery == 0) hits the ceiling, the retry
// does not endlessly repeat full rebuilds.
func TestBuilderPeriodicPassTimeoutResumesIncrementally(t *testing.T) {
	start := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	now := start
	store := newInstrumentedCeilingStore()
	b := &Builder{
		Store:        store,
		Now:          func() time.Time { return now },
		lastRun:      start.Add(-5 * time.Minute),
		passes:       fullPassEvery, // e.g. 12
		fullRepairAt: start.Add(30 * time.Minute),
	}

	// Pass 12: triggered by passes % fullPassEvery == 0.
	store.blockOnFull.Store(true)
	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelTimeout()

	err := b.RunOnce(timeoutCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pass 12 err = %v, want context.DeadlineExceeded", err)
	}
	if got := store.fullReads.Load(); got != 1 {
		t.Fatalf("pass 12 fullReads = %d, want 1", got)
	}

	// Retry (pass 13) 1 second later: must run INCREMENTALLY.
	store.blockOnFull.Store(false)
	now = start.Add(time.Second)
	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatalf("retry pass failed: %v", err)
	}

	if got := store.fullReads.Load(); got != 1 {
		t.Fatalf("retry fullReads = %d, want 1 (must resume incrementally)", got)
	}
	if got := store.scopedReads.Load(); got != 1 {
		t.Fatalf("retry scopedReads = %d, want 1", got)
	}
	if !b.lastRun.Equal(now) {
		t.Fatalf("retry lastRun = %s, want %s", b.lastRun, now)
	}
}

// TestBuilderColdStartTimeoutStillRetriesFull proves that on cold start
// (lastRun.IsZero()), a timed-out pass retries as a full pass because no
// baseline timestamp exists.
func TestBuilderColdStartTimeoutStillRetriesFull(t *testing.T) {
	start := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	now := start
	store := newInstrumentedCeilingStore()
	b := &Builder{
		Store: store,
		Now:   func() time.Time { return now },
	}

	// Cold start pass times out.
	store.blockOnFull.Store(true)
	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelTimeout()

	err := b.RunOnce(timeoutCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cold start err = %v, want context.DeadlineExceeded", err)
	}
	if got := store.fullReads.Load(); got != 1 {
		t.Fatalf("cold start fullReads = %d, want 1", got)
	}

	// Retry: must still attempt full pass because lastRun is zero.
	store.blockOnFull.Store(false)
	now = start.Add(time.Second)
	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatalf("cold start retry failed: %v", err)
	}
	if got := store.fullReads.Load(); got != 2 {
		t.Fatalf("cold start retry fullReads = %d, want 2 (must retry full)", got)
	}
}

// TestBuilderStalePassTimeoutRetriesFull proves that when lastRun is older
// than the 24-hour resumeWindow, an exhaustive pass is required because no
// safe incremental delta exists.
func TestBuilderStalePassTimeoutRetriesFull(t *testing.T) {
	start := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	now := start
	store := newInstrumentedCeilingStore()
	b := &Builder{
		Store:   store,
		Now:     func() time.Time { return now },
		lastRun: start.Add(-25 * time.Hour), // older than resumeWindow (24h)
		passes:  1,
	}

	// Pass times out.
	store.blockOnFull.Store(true)
	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelTimeout()

	err := b.RunOnce(timeoutCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale pass err = %v, want context.DeadlineExceeded", err)
	}

	// Retry: must attempt full pass because lastRun is > 24h old.
	store.blockOnFull.Store(false)
	now = start.Add(time.Second)
	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatalf("stale retry failed: %v", err)
	}
	if got := store.fullReads.Load(); got != 2 {
		t.Fatalf("stale retry fullReads = %d, want 2 (must retry full after 24h)", got)
	}
}
