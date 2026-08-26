package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

type blockingCoverageStore struct {
	*serverstore.Fake
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
	err     error
}

func (s *blockingCoverageStore) FarmCoverage(ctx context.Context) ([]serverstore.FarmAxisCoverage, error) {
	s.calls.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		if s.err != nil {
			return nil, s.err
		}
		return []serverstore.FarmAxisCoverage{{OS: "linux", Ecosystem: "npm", Observed: 3, Measured: 2, Proven: 1}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestCoverageFailureServesLastGoodAndUsesRetryCooldown(t *testing.T) {
	store := &blockingCoverageStore{
		Fake: serverstore.NewFake(), started: make(chan struct{}, 1),
		release: make(chan struct{}), err: errors.New("database unavailable"),
	}
	close(store.release)
	w := &webStore{
		s:            store,
		coverageRows: []web.CoverageRow{{OS: "windows", Ecosystem: "golang", Proven: 7}},
		coverageAt:   time.Now().Add(-2 * coverageTTL),
	}
	rows, err := w.Coverage(t.Context())
	if err != nil || len(rows) != 1 || rows[0].Proven != 7 {
		t.Fatalf("stale last-good coverage = %+v, err=%v", rows, err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		w.coverageMu.Lock()
		refreshing, retryAt := w.coverageRefreshing, w.coverageRetryAt
		w.coverageMu.Unlock()
		if !refreshing && retryAt.After(time.Now()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed refresh did not enter retry cooldown")
		}
		time.Sleep(time.Millisecond)
	}
	for range 10 {
		rows, err = w.Coverage(t.Context())
		if err != nil || len(rows) != 1 || rows[0].Proven != 7 {
			t.Fatalf("last-good coverage during cooldown = %+v, err=%v", rows, err)
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("failed coverage refreshes during cooldown = %d, want 1", got)
	}
}

func TestCoverageColdRefreshDoesNotBlockLandingAndCollapsesReaders(t *testing.T) {
	store := &blockingCoverageStore{
		Fake: serverstore.NewFake(), started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	w := &webStore{s: store}
	started := time.Now()
	rows, err := w.Coverage(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("cold coverage = %+v, err=%v; want an honest empty snapshot", rows, err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("cold coverage blocked the render path for %v", elapsed)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("background coverage refresh did not start")
	}
	if _, err := w.Coverage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("concurrent coverage refreshes = %d, want 1", got)
	}

	close(store.release)
	deadline := time.Now().Add(time.Second)
	for {
		rows, err = w.Coverage(t.Context())
		if err == nil && len(rows) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refreshed coverage = %+v, err=%v", rows, err)
		}
		time.Sleep(time.Millisecond)
	}
	if rows[0].OS != "linux" || rows[0].Ecosystem != "npm" || rows[0].Proven != 1 {
		t.Fatalf("coverage snapshot = %+v", rows)
	}
}
