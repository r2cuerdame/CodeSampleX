package compatibility

// Leader turns the builder_lease primitive (internal/serverstore/lease.go)
// into the standalone Builder's leader-election loop (CSX-451).
//
// Splitting the Builder out of csx-server means a deploy can run two Builder
// processes side by side for a while -- systemd starting the new unit before
// the old one has exited, or a manual restart racing a crash-recovery
// restart. Leader is what keeps that overlap from becoming a second process
// materializing the same shards at the same time: only the process holding
// the lease calls runWhileLeader, and a process that loses the lease (failed
// renew) has its runWhileLeader context cancelled so a wedged pass stops
// participating rather than keeps writing after it should have yielded.
//
// Losing the database, not losing the lease, is the common case a Leader
// spends its life in: acquire fails, so it waits RetryEvery and tries again,
// forever, until ctx ends. That loop is also how a fresh process recovers a
// lease an old one held and never released -- the lease's own TTL is what
// makes the old row eligible to be taken, not anything this type does.

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// DefaultLeaseName is the one lease the compatibility Builder contends for.
// A single named lease is enough: this repo runs one aggregation pipeline,
// not several independently schedulable ones.
const DefaultLeaseName = "compatibility-builder"

// Defaults chosen so a crashed owner is reclaimed inside a minute without
// making a healthy owner renew so often that a single slow PostgreSQL round
// trip looks like lease loss. RenewEvery at a third of TTL gives two missed
// renews of slack before expiry -- one bad round trip must not cost the
// lease.
const (
	DefaultLeaseTTL           = 45 * time.Second
	DefaultLeaseRenewInterval = 15 * time.Second
	DefaultLeaseRetryInterval = 10 * time.Second

	// DefaultPauseTTL bounds how long one Pause call holds without being
	// renewed (CSX-454). csx-server's governor re-asserts the pause every
	// few seconds while it still means it, so this is not how long a pause
	// lasts -- it is how long a pause outlives the process that asked for
	// it. A minute is many governor intervals of slack against a slow
	// database, and a bounded, self-clearing stall against a governor that
	// died mid-incident.
	DefaultPauseTTL = time.Minute
)

// LeaseConfig names the lease and the timings around it. Owner must be
// unique per process (cmd/csx-builder derives it from hostname + pid); two
// Leaders sharing an Owner string would treat each other's lease as their
// own and could both believe they were leader after a takeover.
type LeaseConfig struct {
	Name       string
	Owner      string
	TTL        time.Duration
	RenewEvery time.Duration
	RetryEvery time.Duration
	// PauseTTL is how long one Pause call holds before it clears itself;
	// see DefaultPauseTTL. It has nothing to do with TTL above: TTL is how
	// long the lease survives a dead owner, PauseTTL is how long a pause
	// survives a dead governor.
	PauseTTL time.Duration
}

func (c LeaseConfig) normalize() LeaseConfig {
	if c.Name == "" {
		c.Name = DefaultLeaseName
	}
	if c.TTL <= 0 {
		c.TTL = DefaultLeaseTTL
	}
	if c.RenewEvery <= 0 {
		c.RenewEvery = DefaultLeaseRenewInterval
	}
	if c.RetryEvery <= 0 {
		c.RetryEvery = DefaultLeaseRetryInterval
	}
	if c.PauseTTL <= 0 {
		c.PauseTTL = DefaultPauseTTL
	}
	return c
}

// LeaderStatus is a read-only snapshot for status reporting -- the
// standalone Builder's /progress endpoint and any future admin panel.
type LeaderStatus struct {
	Held        bool
	Lease       serverstore.BuilderLeaseState
	LastAttempt time.Time
	LastError   string
	// Paused is what the last IsPaused call read (CSX-454). It is a cache
	// for reporting only -- /progress must not open a database connection
	// on every poll -- and is false until something has actually asked.
	Paused bool
}

// Leader runs runWhileLeader at most once across every process contending
// for the same named lease on the same Store.
type Leader struct {
	Store serverstore.Store
	Cfg   LeaseConfig
	// Now is a test seam; nil means time.Now.
	Now func() time.Time
	// Logf is a test/observability seam; nil means log.Printf.
	Logf func(format string, args ...any)

	mu     sync.Mutex
	status LeaderStatus
}

func (l *Leader) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now().UTC()
}

func (l *Leader) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (l *Leader) setStatus(fn func(*LeaderStatus)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn(&l.status)
}

// Status returns the Leader's current state. Safe to call concurrently from
// an HTTP handler while Run is active in another goroutine.
func (l *Leader) Status() LeaderStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// Pause tells the Builder under this lease to stop starting passes
// (CSX-454). It is the resource governor's lever, called from csx-server --
// a process that never holds this lease -- so it deliberately presents no
// fencing token: the point is to shed the Builder's load from outside the
// Builder.
//
// The pause it writes expires after Cfg.PauseTTL unless another Pause
// refreshes it. A caller that means to keep something paused must therefore
// keep saying so, and one that dies stops being obeyed. Pause is idempotent:
// calling it every few seconds for an hour is the intended usage, not a
// special case.
//
// It does not touch the lease itself. A paused lease is renewed, expires and
// is reclaimed exactly as an unpaused one is, so a Builder that crashes
// while paused is still recovered by TTL (CSX-451).
func (l *Leader) Pause(ctx context.Context) error {
	cfg := l.Cfg.normalize()
	if err := l.Store.PauseBuilderLease(ctx, cfg.Name, cfg.PauseTTL); err != nil {
		return err
	}
	l.setStatus(func(s *LeaderStatus) { s.Paused = true })
	return nil
}

// Resume clears the pause now rather than waiting out its deadline. It is
// idempotent and succeeds whether or not anything was paused.
func (l *Leader) Resume(ctx context.Context) error {
	cfg := l.Cfg.normalize()
	if err := l.Store.ResumeBuilderLease(ctx, cfg.Name); err != nil {
		return err
	}
	l.setStatus(func(s *LeaderStatus) { s.Paused = false })
	return nil
}

// IsPaused reports whether this lease is paused as of now. Any process may
// ask; it takes nothing and changes nothing. The Builder polls it between
// passes, which is also what keeps Status().Paused current for /progress.
func (l *Leader) IsPaused(ctx context.Context) (bool, error) {
	cfg := l.Cfg.normalize()
	paused, err := l.Store.BuilderLeasePaused(ctx, cfg.Name)
	if err != nil {
		return false, err
	}
	l.setStatus(func(s *LeaderStatus) { s.Paused = paused })
	return paused, nil
}

// PauseGate returns the function to assign to Builder.Paused: it answers
// each pass's "may I start?" from this lease's pause flag, and writes one
// line per transition rather than one per poll.
//
// The rate limiting is the rule cmd/csx-server/dbclass.go's
// budgetPressureWindow already established: during an incident the event
// worth a line is the state changing, and a line every poll for the length
// of a pause measures how long the pause lasted -- which the two transition
// lines already say exactly.
//
// A flag it cannot read means "not paused". Treating a database this process
// cannot reach as a reason to stop working would turn a transient read error
// into a stalled pipeline, and a Builder that truly cannot reach PostgreSQL
// fails its next pass on its own merits with a far better error than this
// one. Both Builder topologies use this, so the in-process Builder
// (CSX_BUILDER_MODE=inprocess) obeys the governor exactly as the standalone
// process does -- a pause that only reached one of them would do nothing at
// all on a deployment running the other.
func (l *Leader) PauseGate() func(context.Context) bool {
	var (
		wasPaused bool
		lastErr   string
	)
	return func(ctx context.Context) bool {
		paused, err := l.IsPaused(ctx)
		if err != nil {
			if msg := err.Error(); msg != lastErr {
				lastErr = msg
				l.logf("compatibility: cannot read the builder pause flag: %v; continuing to run passes", err)
			}
			return false
		}
		lastErr = ""
		if paused != wasPaused {
			wasPaused = paused
			if paused {
				l.logf("compatibility: builder paused by the resource governor; skipping passes until it clears")
			} else {
				l.logf("compatibility: builder resumed; the governor cleared the pause")
			}
		}
		return paused
	}
}

// Run blocks until ctx is cancelled. Whenever it holds the lease, it calls
// runWhileLeader exactly once with a context that is cancelled the moment
// the lease is lost or ctx itself ends, whichever happens first.
// runWhileLeader must return once its context is cancelled; Builder.RunLoop
// already does.
func (l *Leader) Run(ctx context.Context, runWhileLeader func(context.Context)) {
	cfg := l.Cfg.normalize()
	for ctx.Err() == nil {
		st, err := l.Store.AcquireBuilderLease(ctx, cfg.Name, cfg.Owner, cfg.TTL)
		l.setStatus(func(s *LeaderStatus) {
			s.LastAttempt = l.now()
			if err != nil {
				s.Held = false
				s.LastError = err.Error()
				return
			}
			s.Held = true
			s.Lease = st
			s.LastError = ""
		})
		if err != nil {
			if !errors.Is(err, serverstore.ErrLeaseHeld) {
				l.logf("compatibility: builder lease acquire failed: %v; retrying in %s", err, cfg.RetryEvery)
			}
			if !waitBuilderDelay(ctx, cfg.RetryEvery) {
				return
			}
			continue
		}
		l.logf("compatibility: builder lease acquired name=%s owner=%s fence=%d", cfg.Name, st.Owner, st.Fence)
		l.holdAndRun(ctx, cfg, st, runWhileLeader)
	}
}

// holdAndRun runs runWhileLeader for as long as the lease can be renewed,
// then releases it (best-effort -- see ReleaseBuilderLease) before
// returning control to Run.
func (l *Leader) holdAndRun(ctx context.Context, cfg LeaseConfig, st serverstore.BuilderLeaseState, runWhileLeader func(context.Context)) {
	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		l.renewLoop(leaderCtx, cancel, cfg, st.Fence)
	}()

	runWhileLeader(leaderCtx)
	cancel()
	<-renewDone

	l.setStatus(func(s *LeaderStatus) { s.Held = false })
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer releaseCancel()
	if err := l.Store.ReleaseBuilderLease(releaseCtx, cfg.Name, cfg.Owner, st.Fence); err != nil && !errors.Is(err, serverstore.ErrLeaseLost) {
		l.logf("compatibility: builder lease release failed: %v", err)
	}
}

// renewLoop keeps one acquired lease alive until leaderCtx ends or a renew
// is refused. A refusal it did not ask for (leaderCtx already done) is a
// shutdown, not a loss, and returns quietly; any other refusal is a real
// loss and cancels leaderCtx so runWhileLeader stops.
func (l *Leader) renewLoop(leaderCtx context.Context, cancel context.CancelFunc, cfg LeaseConfig, fence int64) {
	for {
		if !waitBuilderDelay(leaderCtx, cfg.RenewEvery) {
			return
		}
		renewed, err := l.Store.RenewBuilderLease(leaderCtx, cfg.Name, cfg.Owner, fence, cfg.TTL)
		if err != nil {
			if leaderCtx.Err() != nil {
				return
			}
			l.setStatus(func(s *LeaderStatus) { s.Held = false; s.LastError = err.Error() })
			l.logf("compatibility: builder lease renew failed: %v; yielding leadership", err)
			cancel()
			return
		}
		l.setStatus(func(s *LeaderStatus) { s.Lease = renewed })
	}
}
