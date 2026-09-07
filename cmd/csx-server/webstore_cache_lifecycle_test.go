package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestExpiredCorpusSnapshotRemainsPositiveUntilReplaced(t *testing.T) {
	fake := serverstore.NewFake()
	purl := "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"
	raw := `{"rows":[{"count":7}]}`
	if err := fake.PutSnapshot(t.Context(), purl, "Batch", raw); err != nil {
		t.Fatal(err)
	}
	w := &webStore{s: fake}
	if _, err := w.cachedSnapshots(t.Context()); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * packageDetailCacheTTL)
	w.snapshotAt = old
	w.snapshotJSON.Store(purl+"|Batch", cachedSnapshotJSON{at: old, json: raw, ok: true})
	for range 2 {
		got, ok, err := w.SnapshotJSONWithError(t.Context(), purl, "Batch")
		if err != nil || !ok || got != raw {
			t.Fatalf("expired positive = %q, %t, %v; want the last complete corpus row", got, ok, err)
		}
	}
	// A newer complete corpus that removed the coordinate remains authoritative.
	if err := fake.DeleteSnapshots(t.Context(), []serverstore.SnapshotTarget{{PURL: purl, Symbol: "Batch"}}); err != nil {
		t.Fatal(err)
	}
	w.refreshSnapshots(false)
	if got, ok, err := w.SnapshotJSONWithError(t.Context(), purl, "Batch"); err != nil || ok || got != "" {
		t.Fatalf("retired snapshot resurrected: %q, %t, %v", got, ok, err)
	}
}

type cancelAwareCacheStore struct{ *serverstore.Fake }

func (s *cancelAwareCacheStore) ListSnapshots(ctx context.Context) ([]serverstore.SnapshotRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Fake.ListSnapshots(ctx)
}
func (s *cancelAwareCacheStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Fake.SnapshotKeys(ctx)
}
func (s *cancelAwareCacheStore) CompletenessGaps(ctx context.Context, q string, offset, limit int) ([]serverstore.CompletenessGap, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return s.Fake.CompletenessGaps(ctx, q, offset, limit)
}

func TestCanceledColdCacheReadersDoNotDeferHealthyVisitors(t *testing.T) {
	for _, kind := range []string{"snapshots", "targets", "gaps"} {
		for _, expired := range []bool{false, true} {
			name := kind + "/canceled"
			if expired {
				name = kind + "/deadline"
			}
			t.Run(name, func(t *testing.T) {
				w := &webStore{s: &cancelAwareCacheStore{Fake: serverstore.NewFake()}}
				read := func(ctx context.Context) error {
					switch kind {
					case "snapshots":
						_, err := w.cachedSnapshots(ctx)
						return err
					case "targets":
						_, err := w.cachedTargetIndex(ctx)
						return err
					default:
						_, err := w.cachedGaps(ctx)
						return err
					}
				}
				for range 1 + retrypolicy.MaxRetries {
					base := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)
					ctx, cancel := context.WithCancel(base)
					want := context.Canceled
					if expired {
						cancel()
						ctx, cancel = context.WithDeadline(base, time.Now().Add(-time.Second))
						want = context.DeadlineExceeded
					} else {
						cancel()
					}
					err := read(ctx)
					cancel()
					if !errors.Is(err, want) {
						t.Fatalf("request result = %v, want %v without shared pressure", err, want)
					}
				}
				if err := read(serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)); err != nil {
					t.Fatalf("healthy visitor inherited canceled readers' backoff: %v", err)
				}
			})
		}
	}
}

func TestSnapshotLeaderDeadlineDoesNotFailSharedWaiter(t *testing.T) {
	fake := serverstore.NewFake()
	purl := "pkg:golang/go.opentelemetry.io/otel@v1.45.0"
	raw := `{"rows":[]}`
	if err := fake.PutSnapshot(t.Context(), purl, "Tracer", raw); err != nil {
		t.Fatal(err)
	}
	store := &cancelDetachedBulkSnapshotStore{Fake: fake, started: make(chan struct{}), release: make(chan struct{})}
	w := &webStore{s: store}
	ctx, cancel := context.WithTimeout(serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive), 100*time.Millisecond)
	defer cancel()
	leader := make(chan error, 1)
	go func() { _, _, err := w.SnapshotJSONWithError(ctx, purl, ""); leader <- err }()
	<-store.started
	waiter := make(chan error, 1)
	go func() {
		got, ok, err := w.SnapshotJSONWithError(serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive), purl, "Tracer")
		if err == nil && (!ok || got != raw) {
			err = errors.New("shared waiter lost the snapshot")
		}
		waiter <- err
	}()
	if err := <-leader; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("leader = %v", err)
	}
	select {
	case err := <-waiter:
		t.Fatalf("waiter inherited leader deadline: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(store.release)
	if err := <-waiter; err != nil {
		t.Fatal(err)
	}
	if got := store.bulkCalls.Load(); got != 1 {
		t.Fatalf("bulk reads=%d, want one shared read", got)
	}
	stateAny, _ := w.purlSnapshotLoads.Load(purl)
	state := stateAny.(*snapshotLoadState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.interactive.retry.State() != retrypolicy.Ready || !state.interactive.retryAt.IsZero() {
		t.Fatal("leader deadline poisoned shared retry state")
	}
}

type retryMetricCacheStore struct {
	*serverstore.PG
	fail bool
}

func (s *retryMetricCacheStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	rows, err := s.PG.SnapshotKeys(ctx)
	if err == nil && s.fail {
		return nil, errors.New("snapshot refresh failed after database read")
	}
	return rows, err
}

func TestIntegrationCacheRetriesAreCountedAndResetAfterDeferral(t *testing.T) {
	_, _, pg := openTestServer(t, testServerPoolPolicy())
	store := &retryMetricCacheStore{PG: pg, fail: true}
	w := &webStore{s: store}
	background := func() serverstore.ClassPoolStats {
		for _, c := range pg.PoolStats().Classes {
			if c.Class == "background" {
				return c
			}
		}
		t.Fatal("background counters missing")
		return serverstore.ClassPoolStats{}
	}
	refresh := func() {
		_, _ = w.HotPackages(t.Context(), 12)
		deadline := time.Now().Add(3 * time.Second)
		for {
			w.hotMu.Lock()
			running := w.hotRefreshing
			w.hotMu.Unlock()
			if !running {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("refresh did not complete")
			}
			time.Sleep(time.Millisecond)
		}
	}
	before := background()
	for attempt := 0; attempt < 1+retrypolicy.MaxRetries; attempt++ {
		refresh()
		got := background()
		if got.First-before.First != 1 || got.Retries-before.Retries != uint64(attempt) {
			t.Fatalf("attempt %d first=%d retries=%d, want first=1 retries=%d", attempt+1, got.First-before.First, got.Retries-before.Retries, attempt)
		}
		if attempt < retrypolicy.MaxRetries {
			w.hotMu.Lock()
			w.hotRetryAt = time.Now().Add(-time.Second)
			w.hotMu.Unlock()
		}
	}
	exhausted := background()
	refresh()
	if got := background(); got.Attempts != exhausted.Attempts {
		t.Fatal("terminal deferred cache queried the database")
	}
	store.fail = false
	w.hotMu.Lock()
	w.hotRetryAt = time.Now().Add(-time.Second)
	w.hotMu.Unlock()
	refresh()
	if got := background(); got.First-before.First != 2 || got.Retries-before.Retries != retrypolicy.MaxRetries {
		t.Fatalf("deferred recovery did not start a fresh budget: %+v", got)
	}
	w.hotMu.Lock()
	w.hotAt = time.Now().Add(-2 * hotPackagesTTL)
	w.hotMu.Unlock()
	refresh()
	if got := background(); got.First-before.First != 3 || got.Retries-before.Retries != retrypolicy.MaxRetries {
		t.Fatalf("successful refresh did not reset retry accounting: %+v", got)
	}
}

func TestSharedSnapshotLoadRetainsItsOwnBoundAndTrafficClass(t *testing.T) {
	caller, cancelCaller := context.WithTimeout(serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive), time.Millisecond)
	defer cancelCaller()
	started := time.Now()
	load, cancelLoad := snapshotLoadContext(caller)
	defer cancelLoad()
	deadline, ok := load.Deadline()
	if !ok || deadline.Before(started.Add(recordSnapshotRefreshTimeout-time.Second)) || deadline.After(time.Now().Add(recordSnapshotRefreshTimeout)) {
		t.Fatalf("shared read deadline = %s, want its own bounded refresh lifetime", deadline)
	}
	cancelCaller()
	if load.Err() != nil {
		t.Fatalf("caller cancellation ended shared read: %v", load.Err())
	}
	if serverstore.QueryClassOf(load) != serverstore.ClassInteractive {
		t.Fatal("shared read lost interactive admission priority")
	}
	cancelLoad()
	if !errors.Is(load.Err(), context.Canceled) {
		t.Fatal("shared read's own cancellation was ignored")
	}
}
