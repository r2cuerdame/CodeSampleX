package compatibility

// Leader's control flow (acquire/hold/renew/yield) is exercised here against
// serverstore.Fake, which implements the exact same builder-lease semantics
// pg.go's lease.go does (mutual exclusion, fencing, TTL expiry) -- see
// internal/serverstore/fake.go. What only real PostgreSQL can prove --
// that two independent connections genuinely agree on one row under
// concurrent CAS -- is covered separately in
// internal/serverstore/lease_pg_test.go.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// clockstep is a manually-advanced clock shared between a Leader and the
// Fake store behind it, so a lease's TTL can be made to expire without the
// test actually waiting on a wall clock.
type clockstep struct {
	mu  sync.Mutex
	now time.Time
}

func newClockstep(start time.Time) *clockstep { return &clockstep{now: start} }
func (c *clockstep) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *clockstep) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testLeaseConfig(owner string) LeaseConfig {
	return LeaseConfig{
		Name:       "test-lease",
		Owner:      owner,
		TTL:        time.Minute,
		RenewEvery: 5 * time.Millisecond,
		RetryEvery: 5 * time.Millisecond,
	}
}

func TestLeaderRunsWhileItHoldsTheLeaseAndReleasesOnShutdown(t *testing.T) {
	clock := newClockstep(time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC))
	store := serverstore.NewFake()
	store.NowFn = clock.Now

	l := &Leader{Store: store, Cfg: testLeaseConfig("owner-a"), Now: clock.Now}

	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int64
	done := make(chan struct{})
	go func() {
		l.Run(ctx, func(leaderCtx context.Context) {
			runs.Add(1)
			<-leaderCtx.Done()
		})
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runs.Load() == 0 {
		t.Fatal("runWhileLeader was never called even though nobody else holds the lease")
	}
	if !l.Status().Held {
		t.Fatal("Status().Held is false while the Leader is running under the lease")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx was cancelled")
	}
	if l.Status().Held {
		t.Fatal("Status().Held is still true after Run returned")
	}
	if _, ok, _ := store.GetBuilderLease(context.Background(), "test-lease"); ok {
		t.Fatal("lease row still exists after a clean shutdown released it")
	}
}

func TestLeaderDoesNotRunWhileAnotherOwnerHoldsAnUnexpiredLease(t *testing.T) {
	clock := newClockstep(time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC))
	store := serverstore.NewFake()
	store.NowFn = clock.Now
	if _, err := store.AcquireBuilderLease(context.Background(), "test-lease", "owner-other", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	l := &Leader{Store: store, Cfg: testLeaseConfig("owner-a"), Now: clock.Now}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int64
	done := make(chan struct{})
	go func() {
		l.Run(ctx, func(context.Context) { runs.Add(1) })
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	if runs.Load() != 0 {
		t.Fatalf("runWhileLeader ran %d times while another owner held the unexpired lease", runs.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx was cancelled")
	}
}

// A lease taken over out from under a running Leader must cancel that
// Leader's runWhileLeader context -- the fencing token is what makes this
// safe: the stolen lease's fence no longer matches what the deposed Leader
// is renewing with.
func TestLeaderYieldsWhenItsLeaseIsTakenOver(t *testing.T) {
	clock := newClockstep(time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC))
	store := serverstore.NewFake()
	store.NowFn = clock.Now

	cfg := testLeaseConfig("owner-a")
	cfg.TTL = 30 * time.Millisecond
	cfg.RenewEvery = 500 * time.Millisecond // long enough that the test's takeover wins the race
	l := &Leader{Store: store, Cfg: cfg, Now: clock.Now}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	yielded := make(chan struct{})
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		l.Run(ctx, func(leaderCtx context.Context) {
			close(started)
			<-leaderCtx.Done()
			close(yielded)
			<-ctx.Done() // Run will try to reacquire; stop it from looping forever in this test
		})
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("runWhileLeader never started")
	}

	// Expire the lease from the store's point of view and take it over as a
	// different owner, simulating a second process winning it after a crash.
	clock.Advance(time.Minute)
	if _, err := store.AcquireBuilderLease(context.Background(), "test-lease", "owner-b", time.Minute); err != nil {
		t.Fatalf("takeover acquire: %v", err)
	}

	select {
	case <-yielded:
	case <-time.After(2 * time.Second):
		t.Fatal("the deposed Leader's runWhileLeader context was never cancelled after takeover")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx was cancelled")
	}
}
