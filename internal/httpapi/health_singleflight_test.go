package httpapi

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type blockingHealthStore struct {
	serverstore.Store
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func (s *blockingHealthStore) GetLatestStats(ctx context.Context) (string, bool, error) {
	s.calls.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return "", false, nil
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}

func TestConcurrentHealthChecksShareOneDatabaseRead(t *testing.T) {
	store := &blockingHealthStore{
		Store:   serverstore.NewFake(),
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	a := &api{d: Deps{Store: store}}
	const callers = 12
	start := make(chan struct{})
	errs := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			ctx := serverstore.WithQueryClass(t.Context(), serverstore.ClassProbe)
			errs <- a.databaseHealth(ctx)
		}()
	}
	ready.Wait()
	close(start)
	<-store.started
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("concurrent health reads = %d, want 1", got)
	}
	close(store.release)
	for range callers {
		if err := <-errs; err != nil {
			t.Fatalf("shared health read: %v", err)
		}
	}
}
