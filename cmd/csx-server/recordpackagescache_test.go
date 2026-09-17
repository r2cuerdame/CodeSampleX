package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

type blockingRecordPackagesStore struct {
	*serverstore.Fake
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
	once    sync.Once
	targets []serverstore.SnapshotTarget
	budget  atomic.Pointer[serverstore.QueryBudget]
}

func newBlockingRecordPackagesStore(targets []serverstore.SnapshotTarget) *blockingRecordPackagesStore {
	return &blockingRecordPackagesStore{
		Fake:    serverstore.NewFake(),
		started: make(chan struct{}),
		release: make(chan struct{}),
		targets: targets,
	}
}

func (s *blockingRecordPackagesStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	s.calls.Add(1)
	s.budget.Store(serverstore.BudgetOf(ctx))
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return append([]serverstore.SnapshotTarget(nil), s.targets...), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestCanonicalRecordPackagesColdReadersCoalesce(t *testing.T) {
	store := newBlockingRecordPackagesStore([]serverstore.SnapshotTarget{
		{PURL: "pkg:npm/axios@1.0.0", Symbol: "axios.get"},
	})
	w := &webStore{s: store}
	interactive := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)

	const readers = 24
	results := make(chan error, readers)
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, total, err := w.RecordPackages(interactive, web.RecordFilter{}, 0, 40)
			if err == nil && (total != 1 || len(rows) != 1 || rows[0].Name != "axios") {
				t.Errorf("canonical result = %+v, total=%d; want axios", rows, total)
			}
			results <- err
		}()
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("cold canonical ranking did not reach the store")
	}
	time.Sleep(25 * time.Millisecond)
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("SnapshotKeys calls while cold readers wait = %d, want 1", got)
	}
	close(store.release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("SnapshotKeys calls for %d cold readers = %d, want 1", readers, got)
	}
	if budget := store.budget.Load(); budget == nil || budget.Class() != serverstore.ClassInteractive {
		t.Fatalf("cold canonical budget = %+v, want interactive", budget)
	}
}

func TestCanonicalRecordPackagesServeStaleWhileOneBackgroundRefreshRuns(t *testing.T) {
	store := newBlockingRecordPackagesStore([]serverstore.SnapshotTarget{
		{PURL: "pkg:npm/new@2.0.0", Symbol: "new.run"},
	})
	w := &webStore{
		s:             store,
		recordAt:      time.Now().Add(-recordPackagesCacheTTL - time.Second),
		recordRows:    []web.PackageHit{{Ecosystem: "npm", Name: "old", LatestVersion: "1.0.0"}},
		targetsAt:     time.Now().Add(-recordSnapshotCacheTTL - time.Second),
		targetsRows:   []serverstore.SnapshotTarget{{PURL: "pkg:npm/old@1.0.0"}},
		updatedAtRead: time.Now().Add(-recordSnapshotCacheTTL - time.Second),
		updatedAt:     map[string]time.Time{"pkg:npm/old@1.0.0": time.Now().Add(-time.Hour)},
	}
	w.targetsIndex = buildTargetIndex(w.targetsRows)
	interactive := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)

	started := time.Now()
	rows, total, err := w.RecordPackages(interactive, web.RecordFilter{}, 0, 40)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("stale canonical ranking blocked for %v", elapsed)
	}
	if total != 1 || len(rows) != 1 || rows[0].Name != "old" {
		t.Fatalf("stale canonical result = %+v, total=%d; want old", rows, total)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("stale canonical ranking did not start a refresh")
	}

	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, gotTotal, gotErr := w.RecordPackages(interactive, web.RecordFilter{}, 0, 40)
			if gotErr != nil || gotTotal != 1 || len(got) != 1 || got[0].Name != "old" {
				t.Errorf("result during refresh = %+v, total=%d, err=%v; want stale old", got, gotTotal, gotErr)
			}
		}()
	}
	wg.Wait()
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("refreshes for concurrent stale readers = %d, want 1", got)
	}
	if budget := store.budget.Load(); budget == nil || budget.Class() != serverstore.ClassBackground {
		t.Fatalf("canonical refresh budget = %+v, want background", budget)
	}

	close(store.release)
	waitUntil(t, func() bool {
		got, gotTotal, gotErr := w.RecordPackages(interactive, web.RecordFilter{}, 0, 40)
		return gotErr == nil && gotTotal == 1 && len(got) == 1 && got[0].Name == "new"
	}, "completed canonical refresh was not published")
}

type filteredRecordPackagesStore struct {
	*serverstore.Fake
	targetCalls   atomic.Int64
	snapshotCalls atomic.Int64
}

func (s *filteredRecordPackagesStore) SnapshotKeys(context.Context) ([]serverstore.SnapshotTarget, error) {
	s.targetCalls.Add(1)
	return []serverstore.SnapshotTarget{{PURL: "pkg:npm/live@2.0.0", Symbol: "live.run"}}, nil
}

func (s *filteredRecordPackagesStore) ListSnapshots(context.Context) ([]serverstore.SnapshotRow, error) {
	s.snapshotCalls.Add(1)
	return []serverstore.SnapshotRow{{
		PURL:   "pkg:npm/live@2.0.0",
		Symbol: "live.run",
		SnapshotJSON: `{"rows":[{"envBucket":{"schemaVersion":1,"ecosystem":"npm","os":"linux","runtime":"node"},` +
			`"byStage":{"CONTRACT":{"pass":1,"fail":0}}}]}`,
	}}, nil
}

func TestFilteredRecordPackagesBypassCanonicalCache(t *testing.T) {
	tests := []struct {
		name          string
		filter        web.RecordFilter
		wantTargets   int64
		wantSnapshots int64
	}{
		{name: "query", filter: web.RecordFilter{Query: "live"}, wantTargets: 1},
		{name: "ecosystem", filter: web.RecordFilter{Ecosystem: "npm"}, wantTargets: 1},
		{name: "os", filter: web.RecordFilter{OS: "linux"}, wantSnapshots: 1},
		{name: "runtime", filter: web.RecordFilter{Runtime: "node"}, wantSnapshots: 1},
		{name: "basis", filter: web.RecordFilter{Basis: "verified"}, wantSnapshots: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &filteredRecordPackagesStore{Fake: serverstore.NewFake()}
			w := &webStore{
				s:          store,
				recordAt:   time.Now(),
				recordRows: []web.PackageHit{{Ecosystem: "npm", Name: "cached-only"}},
			}

			rows, total, err := w.RecordPackages(t.Context(), tt.filter, 0, 40)
			if err != nil {
				t.Fatal(err)
			}
			if total != 1 || len(rows) != 1 || rows[0].Name != "live" {
				t.Fatalf("filtered result = %+v, total=%d; canonical cache leaked into filtered path", rows, total)
			}
			if got := store.targetCalls.Load(); got != tt.wantTargets {
				t.Fatalf("SnapshotKeys calls = %d, want %d", got, tt.wantTargets)
			}
			if got := store.snapshotCalls.Load(); got != tt.wantSnapshots {
				t.Fatalf("ListSnapshots calls = %d, want %d", got, tt.wantSnapshots)
			}
		})
	}
}

type orderedRecordPackagesStore struct {
	*serverstore.Fake
}

func (s *orderedRecordPackagesStore) SnapshotKeys(context.Context) ([]serverstore.SnapshotTarget, error) {
	return []serverstore.SnapshotTarget{
		{PURL: "pkg:npm/older@1.0.0"},
		{PURL: "pkg:npm/newer@1.0.0"},
	}, nil
}

func (s *orderedRecordPackagesStore) SnapshotUpdatedAt(context.Context) (map[string]time.Time, error) {
	return map[string]time.Time{
		"pkg:npm/older@1.0.0": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		"pkg:npm/newer@1.0.0": time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func TestCanonicalRecordPackagesColdLoadIncludesNewestOrdering(t *testing.T) {
	w := &webStore{s: &orderedRecordPackagesStore{Fake: serverstore.NewFake()}}
	ctx := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)
	rows, total, err := w.RecordPackages(ctx, web.RecordFilter{}, 0, 40)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 || rows[0].Name != "newer" || rows[0].UpdatedAt != "2026-02-01" {
		t.Fatalf("canonical ranking = %+v, total=%d; want newest package and timestamp first", rows, total)
	}
}

func TestCanonicalRecordPackagesCanceledCallerSeesCancellationNotWarmCache(t *testing.T) {
	store := newBlockingRecordPackagesStore(nil)
	w := &webStore{
		s:          store,
		recordAt:   time.Now(),
		recordRows: []web.PackageHit{{Ecosystem: "npm", Name: "warm", LatestVersion: "1.0.0"}},
	}
	interactive := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)
	canceled, cancel := context.WithCancel(interactive)
	cancel()
	rows, total, err := w.RecordPackages(canceled, web.RecordFilter{}, 0, 40)
	if !errors.Is(err, context.Canceled) || rows != nil || total != 0 {
		t.Fatalf("canceled RecordPackages = %+v, total=%d, err=%v; want context.Canceled", rows, total, err)
	}
	if got := store.calls.Load(); got != 0 {
		t.Fatalf("store calls = %d, want 0 (canceled caller must not load)", got)
	}
}
