package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type blockingPrewarmStore struct {
	*serverstore.Fake
	release chan struct{}
	seen    chan string
	targets atomic.Int64

	mu       sync.Mutex
	classes  map[string][]serverstore.QueryClass
	noBudget bool
	noLimit  bool
}

func newBlockingPrewarmStore() *blockingPrewarmStore {
	return &blockingPrewarmStore{
		Fake:    serverstore.NewFake(),
		release: make(chan struct{}),
		seen:    make(chan string, 8),
		classes: make(map[string][]serverstore.QueryClass),
	}
}

func (s *blockingPrewarmStore) block(ctx context.Context, name string) error {
	s.mu.Lock()
	s.classes[name] = append(s.classes[name], serverstore.QueryClassOf(ctx))
	if serverstore.BudgetOf(ctx) == nil {
		s.noBudget = true
	}
	if _, ok := ctx.Deadline(); !ok {
		s.noLimit = true
	}
	s.mu.Unlock()
	select {
	case s.seen <- name:
	default:
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *blockingPrewarmStore) GetLatestStats(ctx context.Context) (string, bool, error) {
	if err := s.block(ctx, "stats"); err != nil {
		return "", false, err
	}
	return `{}`, true, nil
}

func (s *blockingPrewarmStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	s.targets.Add(1)
	if err := s.block(ctx, "targets"); err != nil {
		return nil, err
	}
	return []serverstore.SnapshotTarget{{PURL: "pkg:npm/prewarmed@1.0.0"}}, nil
}

func (s *blockingPrewarmStore) SnapshotUpdatedAt(ctx context.Context) (map[string]time.Time, error) {
	if err := s.block(ctx, "updated"); err != nil {
		return nil, err
	}
	return map[string]time.Time{"pkg:npm/prewarmed@1.0.0": time.Now()}, nil
}

func (s *blockingPrewarmStore) ListSamplesPageWithTotal(ctx context.Context, limit, offset int) ([]serverstore.SampleRow, int, error) {
	if limit != 24 || offset != 0 {
		return nil, 0, context.Canceled
	}
	if err := s.block(ctx, "samples"); err != nil {
		return nil, 0, err
	}
	return []serverstore.SampleRow{samplePageRow("sha256:prewarmed")}, 1, nil
}

func TestBuildMuxPrewarmsPublicCachesWithoutDelayingStartup(t *testing.T) {
	store := newBlockingPrewarmStore()
	returned := make(chan struct{})
	go func() {
		_ = BuildMux(serverstore.ServerConfig{}, store)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(200 * time.Millisecond):
		close(store.release)
		t.Fatal("BuildMux waited for blocked prewarm reads")
	}

	want := map[string]int{"stats": 1, "targets": 2, "samples": 1}
	deadline := time.After(time.Second)
	for len(want) > 0 {
		select {
		case name := <-store.seen:
			if remaining := want[name]; remaining > 1 {
				want[name] = remaining - 1
			} else {
				delete(want, name)
			}
		case <-deadline:
			close(store.release)
			t.Fatalf("prewarm did not trigger blocked cache reads: missing %v", want)
		}
	}

	store.mu.Lock()
	for name, classes := range store.classes {
		for _, class := range classes {
			if class != serverstore.ClassBackground {
				store.mu.Unlock()
				close(store.release)
				t.Fatalf("%s prewarm class = %v, want background", name, class)
			}
		}
	}
	if store.noBudget || store.noLimit {
		noBudget, noLimit := store.noBudget, store.noLimit
		store.mu.Unlock()
		close(store.release)
		t.Fatalf("prewarm query contexts: missing budget=%v missing deadline=%v", noBudget, noLimit)
	}
	store.mu.Unlock()

	close(store.release)
	select {
	case name := <-store.seen:
		if name != "updated" {
			t.Fatalf("first prewarm read after target release = %q, want updated", name)
		}
	case <-time.After(time.Second):
		t.Fatal("canonical compatibility prewarm did not load its ordering input")
	}
}
