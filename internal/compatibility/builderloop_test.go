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
	runBuilderLoopWith(t.Context(), normalInterval, 0, func(ctx context.Context) error {
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
	runBuilderLoopWith(t.Context(), interval, 0, func(context.Context) error {
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

// TestBuilderStalledPassIsBoundedAndRetried is the regression for the
// production freeze recorded in #174: /v1/stats.generatedAt stopped at
// 2026-09-09T17:32:58Z and did not move for the following ~20 hours while
// the process kept serving HTTP.
//
// A pass has three unbounded axes. ClassBackground has no statement ceiling
// and no acquisition wait budget (PoolPolicy.statementTimeout/wait both
// return zero for it), and RunOnce was handed the process-lifetime context.
// So one wedged query does not fail the pass -- it suspends it. The retry
// and deferral machinery below is correct and never runs, because run()
// never returns to it. There is no error, no log line and no next pass.
//
// The blocking run here is that query: it returns only when something
// cancels it. Unbounded, the loop never reaches a second attempt.
func TestBuilderStalledPassIsBoundedAndRetried(t *testing.T) {
	const passTimeout = 50 * time.Millisecond
	var attempts atomic.Int64
	var lastErr atomic.Value
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBuilderLoopWith(t.Context(), time.Minute, passTimeout,
			func(ctx context.Context) error {
				attempts.Add(1)
				<-ctx.Done()
				lastErr.Store(ctx.Err())
				return ctx.Err()
			},
			func(_ context.Context, _ time.Duration) bool {
				return attempts.Load() < 2
			},
			func(time.Duration) time.Duration { return 0 })
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("loop never bounded a stalled pass: still inside run after %d attempt(s)", attempts.Load())
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2: a bounded stall must reach the existing retry branch", got)
	}
	if err, _ := lastErr.Load().(error); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled pass ended with %v, want context.DeadlineExceeded", err)
	}
}

// TestBuilderPassDeadlineIsNotShutdown separates the two cancellations. The
// pass deadline must fail one pass; only the caller's context ending stops
// the loop. Sharing one context would turn a single slow pass into a
// silently dead builder -- the same freeze from the other direction.
func TestBuilderPassDeadlineIsNotShutdown(t *testing.T) {
	var attempts atomic.Int64
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBuilderLoopWith(ctx, time.Minute, 20*time.Millisecond,
			func(passCtx context.Context) error {
				if attempts.Add(1) == 3 {
					cancel()
				}
				<-passCtx.Done()
				return passCtx.Err()
			},
			func(waitCtx context.Context, _ time.Duration) bool {
				return waitCtx.Err() == nil
			},
			func(time.Duration) time.Duration { return 0 })
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("loop did not stop after its caller's context was cancelled")
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3: two deadlines survived, the third cancellation stops the loop", got)
	}
}

// TestBuilderPassDeadlineLeavesAHealthyPassAlone guards the truncation risk.
// A bound that cuts a healthy pass short would replace a frozen builder with
// one that can never finish. It proves a pass that takes non-trivial time
// completes normally under a sufficient ceiling without cancellation, while
// an insufficient ceiling bounds it.
func TestBuilderPassDeadlineLeavesAHealthyPassAlone(t *testing.T) {
	var attempts, cancelled atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBuilderLoopWith(t.Context(), time.Minute, time.Hour,
			func(ctx context.Context) error {
				attempts.Add(1)
				// Healthy pass doing non-trivial work within its ceiling.
				select {
				case <-time.After(10 * time.Millisecond):
				case <-ctx.Done():
					cancelled.Add(1)
					return ctx.Err()
				}
				return nil
			},
			func(_ context.Context, delay time.Duration) bool {
				if delay != time.Minute {
					t.Errorf("delay after a completed pass = %s, want the normal interval", delay)
				}
				return attempts.Load() < 3
			}, nil)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy pass loop hung or exceeded test timeout")
	}

	if got := cancelled.Load(); got != 0 {
		t.Fatalf("%d healthy pass(es) were truncated by the ceiling", got)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

// TestBuilderExpiredPassIsNeverRecordedAsSuccess covers the pass that returns
// nil under an expired deadline. Success is what resets the retry series and
// advances lastRun, so a partial pass reported as a complete one would move
// the resume stamp past work that never ran.
func TestBuilderExpiredPassIsNeverRecordedAsSuccess(t *testing.T) {
	var attempts atomic.Int64
	var delays []time.Duration
	done := make(chan struct{})
	go func() {
		defer close(done)
		runBuilderLoopWith(t.Context(), 7*time.Minute, 10*time.Millisecond,
			func(ctx context.Context) error {
				attempts.Add(1)
				<-ctx.Done()
				return nil // finished, it says, on a context that had already expired
			},
			func(_ context.Context, delay time.Duration) bool {
				delays = append(delays, delay)
				return attempts.Load() < 2
			},
			func(time.Duration) time.Duration { return 0 })
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop hung on expired pass instead of retrying")
	}

	if len(delays) == 0 {
		t.Fatal("loop never completed a pass")
	}
	if delays[0] == 7*time.Minute {
		t.Fatal("an expired pass was scheduled as a success: it reset the retry series")
	}
	if delays[0] != time.Second {
		t.Fatalf("first delay = %s, want the 1s first background retry", delays[0])
	}
}
