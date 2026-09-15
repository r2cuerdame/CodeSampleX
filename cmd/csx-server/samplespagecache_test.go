package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type blockingSamplesPageStore struct {
	*serverstore.Fake
	calls   atomic.Int64
	started chan struct{}

	mu      sync.Mutex
	release chan struct{}
	rows    []serverstore.SampleRow
	total   int
	classes []serverstore.QueryClass
}

func (s *blockingSamplesPageStore) ListSamplesPageWithTotal(ctx context.Context, limit, offset int) ([]serverstore.SampleRow, int, error) {
	s.calls.Add(1)
	s.mu.Lock()
	release := s.release
	rows := append([]serverstore.SampleRow(nil), s.rows...)
	total := s.total
	s.classes = append(s.classes, serverstore.QueryClassOf(ctx))
	s.mu.Unlock()
	select {
	case s.started <- struct{}{}:
	default:
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
	return rows, total, nil
}

func samplePageRow(id string) serverstore.SampleRow {
	return serverstore.SampleRow{
		SampleID:     id,
		ManifestJSON: `{"goal":"cached collection","packages":["pkg:npm/axios@1.0.0"]}`,
		Status:       "PUBLISHED",
		CreatedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestSamplesPageColdReadersCoalesceAndReuseFreshPage(t *testing.T) {
	release := make(chan struct{})
	store := &blockingSamplesPageStore{
		Fake: serverstore.NewFake(), started: make(chan struct{}, 1), release: release,
		rows: []serverstore.SampleRow{samplePageRow("sha256:cold")}, total: 31,
	}
	w := &webStore{s: store}
	interactive := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)

	const readers = 16
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, total, err := w.SamplesPage(interactive, 0, 24)
			if err != nil || len(rows) != 1 || rows[0].SampleID != "sha256:cold" || total != 31 {
				t.Errorf("SamplesPage = %+v, total=%d, err=%v", rows, total, err)
			}
		}()
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("cold sample-page read did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("cold combined reads = %d, want 1", got)
	}
	close(release)
	wg.Wait()

	if _, _, err := w.SamplesPage(interactive, 0, 24); err != nil {
		t.Fatal(err)
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("fresh-cache combined reads = %d, want 1", got)
	}
}

func TestSamplesPageServesStaleWhileOneDetachedRefreshRuns(t *testing.T) {
	store := &blockingSamplesPageStore{
		Fake: serverstore.NewFake(), started: make(chan struct{}, 1),
		rows: []serverstore.SampleRow{samplePageRow("sha256:old")}, total: 30,
	}
	w := &webStore{s: store}
	interactive := serverstore.WithQueryClass(t.Context(), serverstore.ClassInteractive)
	if _, _, err := w.SamplesPage(interactive, 0, 24); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.started:
	default:
	}

	release := make(chan struct{})
	store.mu.Lock()
	store.rows = []serverstore.SampleRow{samplePageRow("sha256:new")}
	store.total = 32
	store.release = release
	store.mu.Unlock()
	w.samplesMu.Lock()
	w.samplesPages["0|24"].at = time.Now().Add(-samplesPageCacheTTL - time.Second)
	w.samplesMu.Unlock()

	started := time.Now()
	rows, total, err := w.SamplesPage(interactive, 0, 24)
	if err != nil || len(rows) != 1 || rows[0].SampleID != "sha256:old" || total != 30 {
		t.Fatalf("stale SamplesPage = %+v, total=%d, err=%v", rows, total, err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("stale SamplesPage blocked for %v", elapsed)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("sample-page refresh did not start")
	}

	for range 20 {
		rows, total, err = w.SamplesPage(interactive, 0, 24)
		if err != nil || len(rows) != 1 || rows[0].SampleID != "sha256:old" || total != 30 {
			t.Fatalf("concurrent stale SamplesPage = %+v, total=%d, err=%v", rows, total, err)
		}
	}
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("combined reads with blocked stale refresh = %d, want 2", got)
	}

	close(release)
	waitUntil(t, func() bool {
		rows, total, err := w.SamplesPage(interactive, 0, 24)
		return err == nil && len(rows) == 1 && rows[0].SampleID == "sha256:new" && total == 32
	}, "completed sample-page refresh was not published")
	store.mu.Lock()
	refreshClass := store.classes[len(store.classes)-1]
	store.mu.Unlock()
	if refreshClass != serverstore.ClassBackground {
		t.Fatalf("refresh query class = %v, want background", refreshClass)
	}
}

func TestSamplesPageCacheDoesNotChangeSearchSamples(t *testing.T) {
	ctx := t.Context()
	fake := serverstore.NewFake()
	if err := fake.SaveSample(ctx, samplePageRow("sha256:needle")); err != nil {
		t.Fatal(err)
	}
	store := &perfCountingStore{Fake: fake}
	w := &webStore{s: store}
	if _, _, err := w.SamplesPage(ctx, 0, 24); err != nil {
		t.Fatal(err)
	}

	rows, total, err := w.SearchSamples(ctx, "cached collection", 0, 24)
	if err != nil || len(rows) != 1 || total != 1 {
		t.Fatalf("SearchSamples = %+v, total=%d, err=%v", rows, total, err)
	}
	if got := store.searchSamplesCalls.Load(); got != 1 {
		t.Fatalf("SearchSamplesPage calls = %d, want 1", got)
	}
}
