package main

import (
	"context"
	"errors"
	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func finishTestRefresh(t *testing.T, g *singleflightGroup[int], key string, fn func(context.Context) (int, error)) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	g.Start(context.Background(), key, func(ctx context.Context) (int, error) { close(entered); <-release; return fn(ctx) })
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not start")
	}
	raw, ok := g.loads.Load(key)
	if !ok {
		t.Fatal("active refresh missing")
	}
	close(release)
	select {
	case <-raw.(*singleflightCall[int]).done:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not finish")
	}
}

func TestStaleRefreshFailureBacksOffAndRecovers(t *testing.T) {
	for _, failure := range []error{serverstore.ErrPoolBusy, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			g := &singleflightGroup[int]{}
			var attempts atomic.Int64
			fail := func(ctx context.Context) (int, error) {
				attempt := attempts.Add(1)
				wantBudget := serverstore.NewQueryBudget(serverstore.ClassBackground)
				if attempt > 1 {
					wantBudget = serverstore.NewRetryQueryBudget(serverstore.ClassBackground)
				}
				if !reflect.DeepEqual(serverstore.BudgetOf(ctx), wantBudget) {
					t.Errorf("attempt %d has incorrect first/retry budget", attempt)
				}
				return 0, failure
			}
			for attempt := 0; attempt <= retrypolicy.MaxRetries; attempt++ {
				finishTestRefresh(t, g, "package", fail)
				g.refreshMu.Lock()
				retry := g.refreshRetry["package"]
				if retry == nil || !retry.next.After(time.Now()) {
					t.Fatal("failed refresh lost retry deadline")
				}
				terminal := retry.series.State() == retrypolicy.FailedDeferred
				if terminal != (attempt == retrypolicy.MaxRetries) {
					t.Fatalf("unexpected terminal state on attempt %d", attempt)
				}
				if terminal && time.Until(retry.next) < 50*time.Second {
					t.Fatal("terminal series did not enter cooldown")
				}
				g.refreshMu.Unlock()
				for range 30 {
					g.Start(context.Background(), "package", fail)
				}
				if attempts.Load() != int64(attempt+1) {
					t.Fatal("visitors retried inside cooldown")
				}
				g.refreshMu.Lock()
				g.refreshRetry["package"].next = time.Now().Add(-time.Second)
				g.refreshMu.Unlock()
			}
			finishTestRefresh(t, g, "package", func(context.Context) (int, error) { return 42, nil })
			g.refreshMu.Lock()
			retry := g.refreshRetry["package"]
			g.refreshMu.Unlock()
			if retry != nil {
				t.Fatal("successful refresh retained failure state")
			}
			if _, ok := g.loads.Load("package"); ok {
				t.Fatal("finished refresh occupied its key")
			}
		})
	}
}

func TestStaleRefreshCoalescesAndKeepsKeysIndependent(t *testing.T) {
	g := &singleflightGroup[int]{}
	entered, release := make(chan struct{}), make(chan struct{})
	g.Start(context.Background(), "busy", func(context.Context) (int, error) { close(entered); <-release; return 1, nil })
	<-entered
	raw, _ := g.loads.Load("busy")
	var duplicates atomic.Int64
	for range 30 {
		g.Start(context.Background(), "busy", func(context.Context) (int, error) { duplicates.Add(1); return 0, errors.New("duplicate") })
	}
	finishTestRefresh(t, g, "other", func(context.Context) (int, error) { return 2, nil })
	close(release)
	<-raw.(*singleflightCall[int]).done
	if duplicates.Load() != 0 {
		t.Fatal("coalesced refresh executed more than once")
	}
}

func TestSingleflightLoaderPanicReleasesWaitersAndAllowsRetry(t *testing.T) {
	g := &singleflightGroup[int]{}
	_, err := g.Do(context.Background(), "panic", func(context.Context) (int, error) { panic("sensitive panic payload") })
	if err == nil || err.Error() != "package detail loader panicked" {
		t.Fatalf("panic returned %v", err)
	}
	if _, ok := g.loads.Load("panic"); ok {
		t.Fatal("panicked loader leaked key")
	}
	value, err := g.Do(context.Background(), "panic", func(context.Context) (int, error) { return 7, nil })
	if err != nil || value != 7 {
		t.Fatalf("retry returned %d, %v", value, err)
	}
	finishTestRefresh(t, g, "refresh-panic", func(context.Context) (int, error) { panic("refresh panic") })
	g.refreshMu.Lock()
	retry := g.refreshRetry["refresh-panic"]
	g.refreshMu.Unlock()
	if retry == nil || !retry.next.After(time.Now()) {
		t.Fatal("panicked refresh did not back off")
	}
}
