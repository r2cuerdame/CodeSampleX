package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

type blockingHotPackagesStore struct {
	*serverstore.Fake
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
	err     error
}

type splitSnapshotKeysStore struct {
	*serverstore.Fake
	calls             atomic.Int64
	backgroundStarted chan struct{}
	backgroundRelease chan struct{}
}

func (s *splitSnapshotKeysStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	if s.calls.Add(1) == 1 {
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

func (s *blockingHotPackagesStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
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
		return []serverstore.SnapshotTarget{{PURL: "pkg:npm/axios@1.0.0", Symbol: "axios.get"}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newBlockingHotPackagesStore() *blockingHotPackagesStore {
	return &blockingHotPackagesStore{
		Fake: serverstore.NewFake(), started: make(chan struct{}, 1), release: make(chan struct{}),
	}
}

func waitForHotPackages(t *testing.T, w *webStore) []web.PackageHit {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		rows, err := w.HotPackages(t.Context(), 12)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatal("hot-package refresh did not publish its completed snapshot")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHotPackagesColdRefreshDoesNotBlockAndCollapsesConcurrentReaders(t *testing.T) {
	store := newBlockingHotPackagesStore()
	w := &webStore{s: store}

	started := time.Now()
	rows, err := w.HotPackages(t.Context(), 12)
	if err != nil || len(rows) != 0 {
		t.Fatalf("cold hot packages = %+v, err=%v; want an honest empty snapshot", rows, err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("cold hot packages blocked the render path for %v", elapsed)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("background hot-package refresh did not start")
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, callErr := w.HotPackages(t.Context(), 12); callErr != nil {
				t.Error(callErr)
			}
		}()
	}
	wg.Wait()
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("materialized-inventory refreshes for concurrent readers = %d, want 1", got)
	}

	close(store.release)
	rows = waitForHotPackages(t, w)
	if len(rows) != 1 || rows[0].Ecosystem != "npm" || rows[0].Name != "axios" {
		t.Fatalf("refreshed hot packages = %+v", rows)
	}
}

func TestHotPackagesFailureServesLastGoodAndUsesRetryCooldown(t *testing.T) {
	store := newBlockingHotPackagesStore()
	store.err = errors.New("database unavailable")
	close(store.release)
	w := &webStore{
		s: store, hotRows: []web.PackageHit{{Ecosystem: "golang", Name: "pgx"}},
		hotAt: time.Now().Add(-2 * hotPackagesTTL),
	}

	rows, err := w.HotPackages(t.Context(), 12)
	if err != nil || len(rows) != 1 || rows[0].Name != "pgx" {
		t.Fatalf("stale last-good hot packages = %+v, err=%v", rows, err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		w.hotMu.Lock()
		refreshing, retryAt := w.hotRefreshing, w.hotRetryAt
		w.hotMu.Unlock()
		if !refreshing && retryAt.After(time.Now()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed hot-package refresh did not enter retry cooldown")
		}
		time.Sleep(time.Millisecond)
	}
	for range 10 {
		rows, err = w.HotPackages(t.Context(), 12)
		if err != nil || len(rows) != 1 || rows[0].Name != "pgx" {
			t.Fatalf("last-good hot packages during cooldown = %+v, err=%v", rows, err)
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("failed refreshes during cooldown = %d, want 1", got)
	}
}

func TestHotPackagesBackgroundDoesNotOccupyInteractiveTargetCache(t *testing.T) {
	store := &splitSnapshotKeysStore{
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

	rows, err := w.HotPackages(t.Context(), 12)
	if err != nil || len(rows) != 0 {
		t.Fatalf("cold hot packages = %+v, err=%v", rows, err)
	}
	select {
	case <-store.backgroundStarted:
	case <-time.After(time.Second):
		t.Fatal("background package refresh did not start")
	}

	type searchResult struct {
		rows []web.PackageHit
		err  error
	}
	interactive := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)
	result := make(chan searchResult, 1)
	go func() {
		rows, searchErr := w.SearchPackages(interactive, "interactive", 10)
		result <- searchResult{rows: rows, err: searchErr}
	}()
	select {
	case got := <-result:
		if got.err != nil || len(got.rows) != 1 || got.rows[0].Name != "interactive" {
			t.Fatalf("interactive search during refresh = %+v, err=%v", got.rows, got.err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("interactive target read waited for the blocked background refresh")
	}
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("SnapshotKeys calls before release = %d, want independent background and foreground reads", got)
	}

	close(store.backgroundRelease)
	released = true
	rows = waitForHotPackages(t, w)
	if len(rows) != 1 || rows[0].Name != "background" {
		t.Fatalf("completed background refresh = %+v", rows)
	}
}

func TestColdLandingReturnsBeforeWholeCorpusHotPackages(t *testing.T) {
	store := newBlockingHotPackagesStore()
	mux := BuildMux(serverstore.ServerConfig{PublicURL: "https://codesamplex.dev"}, store)
	req := httptest.NewRequest(http.MethodGet, "https://codesamplex.dev/", nil)
	rec := httptest.NewRecorder()

	started := time.Now()
	mux.ServeHTTP(rec, req)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("cold landing blocked on the corpus for %v", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("cold landing status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `<link rel="canonical" href="https://codesamplex.dev/">`) {
		t.Fatal("cold landing omitted its canonical marker")
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("landing did not schedule its hot-package refresh")
	}
	close(store.release)
}
