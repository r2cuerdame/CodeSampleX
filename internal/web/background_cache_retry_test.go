package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type retryTestClock struct{ nanos atomic.Int64 }

func newRetryTestClock() *retryTestClock {
	c := &retryTestClock{}
	c.set(time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC))
	return c
}

func (c *retryTestClock) now() time.Time    { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *retryTestClock) set(now time.Time) { c.nanos.Store(now.UnixNano()) }

type retrySnapshot struct {
	calls      int64
	refreshing bool
	retryAt    time.Time
	state      retrypolicy.State
	budget     *serverstore.QueryBudget
}

func awaitRetrySnapshot(t *testing.T, wantCalls int64, snapshot func() retrySnapshot) retrySnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		got := snapshot()
		if got.calls == wantCalls && !got.refreshing {
			return got
		}
		if got.calls > wantCalls {
			t.Fatalf("background refresh calls = %d, want %d", got.calls, wantCalls)
		}
		if time.Now().After(deadline) {
			t.Fatalf("background refresh did not finish: got %+v, want calls=%d", got, wantCalls)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertBoundedCacheRetry(t *testing.T, clock *retryTestClock, ttl time.Duration,
	trigger func(), snapshot func() retrySnapshot) {
	t.Helper()
	waits := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, ttl}
	var previousBudget *serverstore.QueryBudget
	for attempt, wantWait := range waits {
		trigger()
		got := awaitRetrySnapshot(t, int64(attempt+1), snapshot)
		wantBudget := serverstore.NewQueryBudget(serverstore.ClassBackground)
		if attempt > 0 {
			wantBudget = serverstore.NewRetryQueryBudget(serverstore.ClassBackground)
		}
		if got.budget == previousBudget || !reflect.DeepEqual(got.budget, wantBudget) {
			t.Fatalf("attempt %d lacks a fresh background %s budget", attempt+1, map[bool]string{false: "initial", true: "retry"}[attempt > 0])
		}
		previousBudget = got.budget
		if delay := got.retryAt.Sub(clock.now()); delay != wantWait {
			t.Fatalf("failure %d delay = %s, want %s", attempt+1, delay, wantWait)
		}
		wantState := retrypolicy.Waiting
		if attempt == len(waits)-1 {
			wantState = retrypolicy.FailedDeferred
		}
		if got.state != wantState {
			t.Fatalf("failure %d state = %d, want %d", attempt+1, got.state, wantState)
		}

		// A burst of requests inside the wait window must not restart work.
		for range 20 {
			trigger()
		}
		time.Sleep(10 * time.Millisecond)
		if after := snapshot(); after.calls != int64(attempt+1) {
			t.Fatalf("failure %d restarted back-to-back: calls=%d", attempt+1, after.calls)
		}
		if attempt+1 < len(waits) {
			clock.set(got.retryAt.Add(time.Nanosecond))
		}
	}

	// Exhaustion stays terminal for the normal cache TTL, then opens a fresh
	// series whose next failure starts again at one second.
	deferred := snapshot()
	clock.set(deferred.retryAt.Add(time.Nanosecond))
	trigger()
	reset := awaitRetrySnapshot(t, 7, snapshot)
	if reset.budget == previousBudget || !reflect.DeepEqual(reset.budget, serverstore.NewQueryBudget(serverstore.ClassBackground)) {
		t.Fatal("post-deferral initial scan did not receive a fresh non-retry background budget")
	}
	if reset.state != retrypolicy.Waiting || reset.retryAt.Sub(clock.now()) != time.Second {
		t.Fatalf("fresh series after deferred TTL = %+v, now=%s", reset, clock.now())
	}
}

type failingAssetRetryStore struct {
	*fakeStore
	calls  atomic.Int64
	budget atomic.Pointer[serverstore.QueryBudget]
}

func (s *failingAssetRetryStore) PackageAssets(ctx context.Context) ([]PackageAsset, error) {
	s.budget.Store(serverstore.BudgetOf(ctx))
	s.calls.Add(1)
	return nil, errors.New("scripted asset refresh failure")
}

func TestPackageAssetRefreshRetriesFiveTimesThenDefersForTTL(t *testing.T) {
	clock := newRetryTestClock()
	store := &failingAssetRetryStore{fakeStore: newFakeStore()}
	s := &site{d: Deps{Store: store}, backgroundNow: clock.now,
		backgroundJitter: func(time.Duration) time.Duration { return 0 }}
	assertBoundedCacheRetry(t, clock, assetTTL, func() { s.packageAssets() }, func() retrySnapshot {
		s.assets.mu.Lock()
		defer s.assets.mu.Unlock()
		return retrySnapshot{store.calls.Load(), s.assets.refreshing, s.assets.retryAt, s.assets.retry.State(), store.budget.Load()}
	})
}

type failingDerivedRetryStore struct {
	*fakeStore
	calls  atomic.Int64
	budget atomic.Pointer[serverstore.QueryBudget]
}

func (s *failingDerivedRetryStore) DerivedFindings(ctx context.Context) ([]DerivedFinding, error) {
	s.budget.Store(serverstore.BudgetOf(ctx))
	s.calls.Add(1)
	return nil, errors.New("scripted findings refresh failure")
}

func TestDerivedFindingsRefreshRetriesFiveTimesThenDefersForTTL(t *testing.T) {
	clock := newRetryTestClock()
	store := &failingDerivedRetryStore{fakeStore: newFakeStore()}
	s := &site{d: Deps{Store: store}, backgroundNow: clock.now,
		backgroundJitter: func(time.Duration) time.Duration { return 0 }}
	r := httptest.NewRequest("GET", "/findings", nil)
	assertBoundedCacheRetry(t, clock, derivedTTL, func() { s.derivedFindings(r) }, func() retrySnapshot {
		s.derivedMu.Lock()
		defer s.derivedMu.Unlock()
		return retrySnapshot{store.calls.Load(), s.derivedRefreshing, s.derivedRetryAt, s.derivedRetry.State(), store.budget.Load()}
	})
}

func TestHandFindingsRefreshRetriesFiveTimesThenDefersForTTL(t *testing.T) {
	clock := newRetryTestClock()
	store := &panickingHandRetryStore{fakeStore: newFakeStore()}
	s := &site{d: Deps{Store: store}, backgroundNow: clock.now,
		backgroundJitter: func(time.Duration) time.Duration { return 0 }}
	r := httptest.NewRequest("GET", "/findings", nil)
	assertBoundedCacheRetry(t, clock, derivedTTL, func() { s.handFindings(r) }, func() retrySnapshot {
		s.handMu.Lock()
		defer s.handMu.Unlock()
		return retrySnapshot{store.manifestCalls.Load(), s.handRefreshing, s.handRetryAt, s.handRetry.State(), store.budget.Load()}
	})
}

type failingHeroRetryStore struct {
	*fakeStore
	calls  atomic.Int64
	budget atomic.Pointer[serverstore.QueryBudget]
}

func (s *failingHeroRetryStore) PackageVersions(ctx context.Context, _, _ string) ([]string, error) {
	s.budget.Store(serverstore.BudgetOf(ctx))
	s.calls.Add(1)
	return nil, errors.New("scripted hero refresh failure")
}

func TestHeroWarmRetriesFiveTimesThenDefersForMatrixTTL(t *testing.T) {
	clock := newRetryTestClock()
	store := &failingHeroRetryStore{fakeStore: newFakeStore()}
	s := &site{d: Deps{Store: store}, backgroundNow: clock.now,
		backgroundJitter: func(time.Duration) time.Duration { return 0 }}
	r := httptest.NewRequest("GET", "/", nil)
	hits := []PackageHit{{Ecosystem: "npm", Name: "broken"}}
	key := "en" + heroMemoKey("")
	assertBoundedCacheRetry(t, clock, heroMatrixTTL, func() { s.heroMatrix(r, "en", hits) }, func() retrySnapshot {
		s.heroMu.Lock()
		defer s.heroMu.Unlock()
		series := s.heroRetry[key]
		return retrySnapshot{store.calls.Load(), s.heroLoading[key], s.heroRetryAt[key], series.State(), store.budget.Load()}
	})
}

type panickingHandRetryStore struct {
	*fakeStore
	manifestCalls atomic.Int64
	budget        atomic.Pointer[serverstore.QueryBudget]
}

func (s *panickingHandRetryStore) SampleManifest(ctx context.Context, _ string) (string, bool) {
	s.budget.Store(serverstore.BudgetOf(ctx))
	s.manifestCalls.Add(1)
	panic("scripted hand findings refresh failure")
}
