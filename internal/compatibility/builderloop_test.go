package compatibility

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestBuilderRetriesAreBoundedThenDeferred(t *testing.T) {
	const normalInterval = 5 * time.Minute
	var attempts int
	var waits []time.Duration
	var classes []serverstore.QueryClass
	runBuilderLoopWith(t.Context(), normalInterval, func(ctx context.Context) error {
		attempts++
		b := serverstore.BudgetOf(ctx)
		classes = append(classes, b.Class())
		return errors.New("scripted database pressure")
	}, func(_ context.Context, delay time.Duration) bool {
		waits = append(waits, delay)
		return len(waits) < retrypolicy.MaxRetries+1
	}, func(time.Duration) time.Duration { return 0 })

	if attempts != 1+retrypolicy.MaxRetries {
		t.Fatalf("attempts = %d, want initial + %d retries", attempts, retrypolicy.MaxRetries)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, normalInterval}
	if len(waits) != len(want) {
		t.Fatalf("wait count = %d, want %d: %v", len(waits), len(want), waits)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("wait %d = %s, want %s", i, waits[i], want[i])
		}
		if classes[i] != serverstore.ClassBackground {
			t.Fatalf("attempt %d class = %s, want background", i+1, classes[i])
		}
	}
}

func TestBuilderNeverImmediatelyRequeuesAfterCompletion(t *testing.T) {
	const interval = 3 * time.Minute
	attempts := 0
	waits := 0
	runBuilderLoopWith(t.Context(), interval, func(context.Context) error {
		attempts++
		return nil
	}, func(_ context.Context, delay time.Duration) bool {
		waits++
		if delay != interval {
			t.Fatalf("successful pass delay = %s, want %s", delay, interval)
		}
		return waits < 3
	}, nil)
	if attempts != 3 || waits != 3 {
		t.Fatalf("attempts/waits = %d/%d, want 3/3", attempts, waits)
	}
}

type lateFailBuilderStore struct {
	serverstore.Store
	calls         atomic.Int64
	secondStarted chan struct{}
	releaseSecond chan struct{}
}

func (s *lateFailBuilderStore) ListSnapshotTargets(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	call := s.calls.Add(1)
	if call == 1 {
		return nil, nil
	}
	if call == 2 {
		close(s.secondStarted)
		select {
		case <-s.releaseSecond:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("database unavailable")
}

func TestBuilderLateFailureDoesNotStartACatchUpPass(t *testing.T) {
	const interval = 40 * time.Millisecond
	store := &lateFailBuilderStore{
		Store:         serverstore.NewFake(),
		secondStarted: make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
	b := &Builder{Store: store}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		b.RunLoop(ctx, interval)
		close(done)
	}()

	select {
	case <-store.secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second builder attempt never started")
	}
	// A ticker accumulates a ready tick while a pass is running. Completion-
	// spaced scheduling must still wait after this late failure.
	time.Sleep(interval + 10*time.Millisecond)
	close(store.releaseSecond)
	time.Sleep(interval / 2)
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("attempts immediately after late failure = %d, want 2", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("builder loop did not stop")
	}
}
