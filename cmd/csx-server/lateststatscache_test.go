package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type blockingLatestStatsStore struct {
	*serverstore.Fake
	calls       atomic.Int64
	started     chan struct{}
	release     chan struct{}
	json        string
	err         error
	budget      atomic.Pointer[serverstore.QueryBudget]
	budgetNanos atomic.Int64
}

func newBlockingLatestStatsStore(statsJSON string) *blockingLatestStatsStore {
	return &blockingLatestStatsStore{
		Fake:    serverstore.NewFake(),
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
		json:    statsJSON,
	}
}

func (s *blockingLatestStatsStore) GetLatestStats(ctx context.Context) (string, bool, error) {
	s.calls.Add(1)
	s.budget.Store(serverstore.BudgetOf(ctx))
	if deadline, ok := ctx.Deadline(); ok {
		s.budgetNanos.Store(time.Until(deadline).Nanoseconds())
	} else {
		s.budgetNanos.Store(-1)
	}
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		if s.err != nil {
			return "", false, s.err
		}
		return s.json, true, nil
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}

func waitForLatestStats(t *testing.T, w *webStore, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if js, ok := w.LatestStatsJSON(t.Context()); ok && js == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("latest-stats refresh did not publish %q", want)
}

func TestLatestStatsColdCallReturnsUnavailableWithoutWaiting(t *testing.T) {
	store := newBlockingLatestStatsStore(`{"packages":42}`)
	w := &webStore{s: store}

	started := time.Now()
	js, ok := w.LatestStatsJSON(t.Context())
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("cold latest stats blocked the render path for %v", elapsed)
	}
	if ok || js != "" {
		t.Fatalf("cold latest stats = %q, %v; want honest unavailable counters", js, ok)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("cold latest stats did not schedule a background refresh")
	}
	if budget := store.budget.Load(); budget == nil || budget.Class() != serverstore.ClassBackground {
		t.Fatalf("latest-stats refresh budget = %+v, want a background query budget", budget)
	}
	if budget := time.Duration(store.budgetNanos.Load()); budget <= 0 || budget > latestStatsRefreshTimeout {
		t.Fatalf("latest-stats refresh deadline = %v, want a positive budget capped at %v", budget, latestStatsRefreshTimeout)
	}

	close(store.release)
	waitForLatestStats(t, w, `{"packages":42}`)
}

func TestLatestStatsConcurrentCallersCoalesceOneRefresh(t *testing.T) {
	store := newBlockingLatestStatsStore(`{"packages":42}`)
	w := &webStore{s: store}

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if js, ok := w.LatestStatsJSON(t.Context()); ok || js != "" {
				t.Errorf("cold latest stats = %q, %v; want unavailable", js, ok)
			}
		}()
	}
	wg.Wait()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("latest-stats refresh did not start")
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("latest-stats refreshes for concurrent callers = %d, want 1", got)
	}

	close(store.release)
	waitForLatestStats(t, w, `{"packages":42}`)
}

func TestLatestStatsWarmCallsReturnCachedSnapshot(t *testing.T) {
	const want = `{"packages":42}`
	store := newBlockingLatestStatsStore(want)
	w := &webStore{s: store}
	w.LatestStatsJSON(t.Context())
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("latest-stats refresh did not start")
	}
	close(store.release)
	waitForLatestStats(t, w, want)

	for range 20 {
		if got, ok := w.LatestStatsJSON(t.Context()); !ok || got != want {
			t.Fatalf("warm latest stats = %q, %v; want %q, true", got, ok, want)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("store reads inside latest-stats TTL = %d, want 1", got)
	}
}

func TestLatestStatsStaleRefreshDoesNotBlock(t *testing.T) {
	const stale = `{"packages":41}`
	store := newBlockingLatestStatsStore(`{"packages":42}`)
	w := &webStore{
		s:         store,
		statsAt:   time.Now().Add(-latestStatsTTL - time.Second),
		statsJSON: stale,
		statsOK:   true,
	}

	started := time.Now()
	js, ok := w.LatestStatsJSON(t.Context())
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("stale latest stats blocked the render path for %v", elapsed)
	}
	if !ok || js != stale {
		t.Fatalf("stale latest stats = %q, %v; want %q, true", js, ok, stale)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("stale latest stats did not schedule a refresh")
	}
	for range 20 {
		if got, cached := w.LatestStatsJSON(t.Context()); !cached || got != stale {
			t.Fatalf("latest stats while refresh is running = %q, %v; want stale snapshot", got, cached)
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("stale callers started %d latest-stats refreshes, want 1", got)
	}

	close(store.release)
	waitForLatestStats(t, w, `{"packages":42}`)
}

func TestLatestStatsFailureKeepsLastGoodAndBacksOff(t *testing.T) {
	const stale = `{"packages":41}`
	store := newBlockingLatestStatsStore("")
	store.err = errors.New("database unavailable")
	close(store.release)
	w := &webStore{
		s:         store,
		statsAt:   time.Now().Add(-latestStatsTTL - time.Second),
		statsJSON: stale,
		statsOK:   true,
	}

	if got, ok := w.LatestStatsJSON(t.Context()); !ok || got != stale {
		t.Fatalf("stale latest stats = %q, %v; want last-good snapshot", got, ok)
	}
	waitUntil(t, func() bool {
		w.statsMu.Lock()
		defer w.statsMu.Unlock()
		return !w.statsRefreshing && w.statsRetryAt.After(time.Now())
	}, "failed latest-stats refresh did not enter retry backoff")

	for range 20 {
		if got, ok := w.LatestStatsJSON(t.Context()); !ok || got != stale {
			t.Fatalf("latest stats during retry backoff = %q, %v; want last-good snapshot", got, ok)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("latest-stats calls during retry backoff = %d, want 1", got)
	}
}
