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
	"errors"
	"fmt"
	"strings"
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

// ------------------------------------------------- the governor's pause --

func TestLeaderPauseAndResumeRoundTrip(t *testing.T) {
	clock := newClockstep(time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC))
	store := serverstore.NewFake()
	store.NowFn = clock.Now
	ctx := context.Background()

	// The governor is not the lease holder: it pauses from csx-server, a
	// process that has never acquired this lease and holds no fencing token.
	governor := &Leader{Store: store, Cfg: testLeaseConfig("csx-server-governor"), Now: clock.Now}
	builder := &Leader{Store: store, Cfg: testLeaseConfig("builder-a"), Now: clock.Now}
	if _, err := store.AcquireBuilderLease(ctx, "test-lease", "builder-a", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	if paused, err := builder.IsPaused(ctx); err != nil || paused {
		t.Fatalf("IsPaused before any pause = %v, %v; want false, nil", paused, err)
	}
	if err := governor.Pause(ctx); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if paused, err := builder.IsPaused(ctx); err != nil || !paused {
		t.Fatalf("the lease holder does not see the governor's pause: %v, %v", paused, err)
	}
	if !builder.Status().Paused {
		t.Fatal("Status().Paused is false after IsPaused read a live pause")
	}
	if err := governor.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if paused, err := builder.IsPaused(ctx); err != nil || paused {
		t.Fatalf("IsPaused after Resume = %v, %v; want false, nil", paused, err)
	}
	if builder.Status().Paused {
		t.Fatal("Status().Paused is still true after the pause was cleared")
	}
}

// A pause is a deadline, not a latch. The governor that asked for it
// refreshes it every few seconds; one that dies stops refreshing, and the
// Builder must go back to work on its own rather than stay paused until an
// operator notices.
func TestLeaderPauseExpiresWhenNobodyRefreshesIt(t *testing.T) {
	clock := newClockstep(time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC))
	store := serverstore.NewFake()
	store.NowFn = clock.Now
	ctx := context.Background()

	cfg := testLeaseConfig("csx-server-governor")
	cfg.PauseTTL = 30 * time.Second
	governor := &Leader{Store: store, Cfg: cfg, Now: clock.Now}
	if err := governor.Pause(ctx); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if paused, _ := governor.IsPaused(ctx); !paused {
		t.Fatal("IsPaused is false immediately after Pause")
	}

	clock.Advance(29 * time.Second)
	if paused, _ := governor.IsPaused(ctx); !paused {
		t.Fatal("the pause lapsed before its TTL")
	}
	clock.Advance(2 * time.Second)
	if paused, _ := governor.IsPaused(ctx); paused {
		t.Fatal("the pause outlived its TTL with nobody refreshing it; a dead governor would wedge the Builder")
	}
}

// PauseGate is what both Builder topologies assign to Builder.Paused, so it
// has to answer the flag, log the transition rather than the poll, and keep
// the pipeline working when it cannot read the flag at all.
func TestLeaderPauseGateLogsTransitionsAndFailsOpen(t *testing.T) {
	clock := newClockstep(time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC))
	store := serverstore.NewFake()
	store.NowFn = clock.Now

	var lines []string
	l := &Leader{
		Store: store,
		Cfg:   testLeaseConfig("builder-a"),
		Now:   clock.Now,
		Logf:  func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	gate := l.PauseGate()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if gate(ctx) {
			t.Fatalf("poll %d reported paused with nothing paused", i)
		}
	}
	if len(lines) != 0 {
		t.Fatalf("an unpaused Builder wrote %d lines: %v", len(lines), lines)
	}

	if err := l.Pause(ctx); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	for i := 0; i < 4; i++ {
		if !gate(ctx) {
			t.Fatalf("poll %d did not see the pause", i)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "paused by the resource governor") {
		t.Fatalf("four polls under one pause wrote %d lines: %v", len(lines), lines)
	}

	if err := l.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	for i := 0; i < 3; i++ {
		if gate(ctx) {
			t.Fatalf("poll %d still reported paused after the resume", i)
		}
	}
	if len(lines) != 2 || !strings.Contains(lines[1], "resumed") {
		t.Fatalf("lines after the resume = %v, want exactly one more naming the resume", lines)
	}

	// A flag it cannot read is not a reason to stop working -- and it says so
	// once, not on every poll.
	store.BuilderLeasePausedErr = errors.New("connection refused")
	for i := 0; i < 3; i++ {
		if gate(ctx) {
			t.Fatalf("poll %d stopped the Builder because the pause flag was unreadable", i)
		}
	}
	if len(lines) != 3 || !strings.Contains(lines[2], "cannot read the builder pause flag") {
		t.Fatalf("lines after three failed reads = %v, want exactly one more", lines)
	}
}

// The guarantee CSX-451 exists for, under CSX-454's new flag: pausing must
// not make a lease unreclaimable. A Builder that is paused and then dies
// leaves the same expired row a running one would, and the next process
// takes it over on the lease's own TTL -- the pause changes nothing about
// owner, fence or expiry.
func TestLeaderPausedLeaseStillExpiresAndIsReclaimed(t *testing.T) {
	clock := newClockstep(time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC))
	store := serverstore.NewFake()
	store.NowFn = clock.Now
	ctx := context.Background()

	held, err := store.AcquireBuilderLease(ctx, "test-lease", "builder-a", time.Minute)
	if err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	govCfg := testLeaseConfig("csx-server-governor")
	// Long enough that the lease's own TTL, not the pause's, is what this
	// test advances past.
	govCfg.PauseTTL = time.Hour
	governor := &Leader{Store: store, Cfg: govCfg, Now: clock.Now}
	if err := governor.Pause(ctx); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	// builder-a is paused and now dies: it never releases and never renews.
	if _, err := store.AcquireBuilderLease(ctx, "test-lease", "builder-b", time.Minute); !errors.Is(err, serverstore.ErrLeaseHeld) {
		t.Fatalf("a paused but unexpired lease was taken over by a second owner: err=%v", err)
	}
	clock.Advance(2 * time.Minute)
	took, err := store.AcquireBuilderLease(ctx, "test-lease", "builder-b", time.Minute)
	if err != nil {
		t.Fatalf("a paused lease was not reclaimable after its TTL passed: %v", err)
	}
	if took.Owner != "builder-b" || took.Fence <= held.Fence {
		t.Fatalf("reclaim of a paused lease = %+v; want owner builder-b and a fence above %d", took, held.Fence)
	}

	// And the successor is still told to stay paused: the pause belongs to
	// the work, not to the process that happened to be doing it.
	if paused, err := governor.IsPaused(ctx); err != nil || !paused {
		t.Fatalf("IsPaused after takeover = %v, %v; want true, nil", paused, err)
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
