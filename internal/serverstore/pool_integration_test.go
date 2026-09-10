package serverstore

// R2C-58: what the pool must do when one query class goes slow.
//
// The failure these pin is measured, not imagined. On 2026-08-22 ten
// concurrent /wanted requests took all eight connections for 42-49 seconds
// each; the ninth and tenth never got one and became 502s at 63 seconds, and
// /healthz -- which the container healthcheck believes -- could not reach the
// database at all. Running these tests against the pool as it was before this
// change fails the first one in exactly that way: "context deadline exceeded"
// after the probe's full 3 seconds, while the slow reads run to completion.
//
// Every test here uses a policy with small budgets. The shipped numbers are
// asserted separately in pool_test.go, so this file measures behaviour and
// not patience.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testPoolPolicy is the shipped shape with the clock turned down: same
// reserve, same class caps, budgets in hundreds of milliseconds.
func testPoolPolicy() PoolPolicy {
	pol := DefaultPoolPolicy()
	pol.ReadTimeout = 700 * time.Millisecond
	pol.ReadWait = 400 * time.Millisecond
	pol.ProbeTimeout = 700 * time.Millisecond
	pol.ProbeWait = 2 * time.Second
	return pol
}

// occupy fills a class's whole share of the pool with a query that would run
// for a minute, and returns once every one of them is actually holding a
// connection. Waiting for the signal rather than sleeping is what makes the
// assertions that follow about the pool and not about scheduling luck.
type occupation struct {
	errs    chan error
	elapsed chan time.Duration
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func occupy(t *testing.T, pg *PG, class QueryClass, n int) *occupation {
	t.Helper()
	o := &occupation{
		errs:    make(chan error, n),
		elapsed: make(chan time.Duration, n),
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	holding := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		o.wg.Add(1)
		go func() {
			defer o.wg.Done()
			ctx := WithQueryClass(rootCtx, class)
			c, err := pg.pool.acquire(ctx)
			if err != nil {
				holding <- struct{}{}
				o.errs <- err
				o.elapsed <- 0
				return
			}
			defer pg.pool.release(c)
			holding <- struct{}{}
			start := time.Now()
			_, execErr := c.conn.Exec(ctx, "SELECT pg_sleep(60)")
			o.elapsed <- time.Since(start)
			o.errs <- execErr
		}()
	}
	for i := 0; i < n; i++ {
		<-holding
	}
	t.Cleanup(func() {
		o.cancel()
		o.wg.Wait()
	})
	return o
}

// TestIntegrationSlowReadsCannotStarveTheHealthProbe is the R2C-58
// reproduction. Every connection a page read is allowed to hold is held by a
// query that will not finish; the health probe must still get an answer.
func TestIntegrationSlowReadsCannotStarveTheHealthProbe(t *testing.T) {
	pol := testPoolPolicy()
	pg := openTestPGWithPolicy(t, pol)
	occupy(t, pg, ClassInteractive, pol.InteractiveConns)

	probeCtx, cancel := context.WithTimeout(
		WithQueryClass(context.Background(), ClassProbe), 3*time.Second)
	defer cancel()
	start := time.Now()
	var one int
	err := pg.withConn(probeCtx, func(c *pgx.Conn) error {
		return c.QueryRow(probeCtx, "SELECT 1").Scan(&one)
	})
	waited := time.Since(start)
	if err != nil {
		t.Fatalf("health probe could not reach the database after %v: %v", waited.Round(time.Millisecond), err)
	}
	if one != 1 {
		t.Fatalf("health probe read %d, not 1", one)
	}
	// It must not merely eventually answer: it must not have queued behind
	// the slow class at all.
	if waited > time.Second {
		t.Fatalf("health probe answered only after %v, so it was queued behind the slow class", waited.Round(time.Millisecond))
	}
}

func TestIntegrationStatementTimeoutSetupHonorsTheCallerDeadline(t *testing.T) {
	pg := openTestPGWithPolicy(t, testPoolPolicy())
	granted := time.Duration(-1)
	c, err := pg.pool.checkout(context.Background(), time.Now(), &granted)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.pool.discard(c)
	// Model an idle connection last used by an unbounded class so the probe
	// must issue SET statement_timeout before it can use the connection.
	c.stmtTimeout = 0
	caller, cancel := context.WithCancel(WithQueryClass(context.Background(), ClassProbe))
	cancel()

	started := time.Now()
	err = pg.pool.applyStatementTimeout(caller, c, ClassProbe)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("statement-timeout setup returned %v, want caller cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("statement-timeout setup ignored its caller for %v", elapsed)
	}
}

// The other half of the same guarantee: ingest is not a page read, and a
// storm of page reads must not stop the server from accepting evidence.
func TestIntegrationSlowReadsLeaveBackgroundWorkAConnection(t *testing.T) {
	pol := testPoolPolicy()
	pg := openTestPGWithPolicy(t, pol)
	occupy(t, pg, ClassInteractive, pol.InteractiveConns)

	ctx, cancel := context.WithTimeout(
		WithQueryClass(context.Background(), ClassBackground), 5*time.Second)
	defer cancel()
	var one int
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT 1").Scan(&one)
	}); err != nil {
		t.Fatalf("background work could not reach the database while reads were slow: %v", err)
	}
}

// The reserve must work in the production direction too: the restart builder
// is background work, and filling its whole allowance cannot consume the
// interactive connection a public request needs. This isolates in-process
// checkout starvation from PostgreSQL CPU/I/O contention, which admission
// tokens cannot reserve.
func TestIntegrationBackgroundWorkLeavesInteractiveCapacity(t *testing.T) {
	pol := testPoolPolicy()
	pg := openTestPGWithPolicy(t, pol)
	occupation := occupy(t, pg, ClassBackground, pol.BackgroundConns)
	defer occupation.cancel()

	budget := NewQueryBudget(ClassInteractive)
	ctx, cancel := context.WithTimeout(WithQueryBudget(context.Background(), budget), time.Second)
	defer cancel()
	start := time.Now()
	var one int
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT 1").Scan(&one)
	}); err != nil {
		t.Fatalf("interactive query could not reach PostgreSQL while background allowance was full: %v", err)
	}
	if one != 1 {
		t.Fatalf("interactive query read %d, want 1", one)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("interactive query waited %v behind background work", elapsed)
	}
	if busy, timeouts, _ := budget.Pressure(); busy != 0 || timeouts != 0 {
		t.Fatalf("interactive reserve reported busy=%d timeouts=%d", busy, timeouts)
	}
}

// A user-facing read that outlives its ceiling is cancelled by PostgreSQL,
// which is what returns the connection to the pool instead of burning it.
func TestIntegrationSlowReadIsCancelledByItsStatementCeiling(t *testing.T) {
	pol := testPoolPolicy()
	pg := openTestPGWithPolicy(t, pol)

	ctx := WithQueryClass(context.Background(), ClassInteractive)
	start := time.Now()
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, execErr := c.Exec(ctx, "SELECT pg_sleep(60)")
		return execErr
	})
	elapsed := time.Since(start)
	if !IsQueryTimeout(err) {
		t.Fatalf("a 60s read under a %v ceiling returned %v after %v, not a statement timeout",
			pol.ReadTimeout, err, elapsed.Round(time.Millisecond))
	}
	if elapsed > 4*pol.ReadTimeout {
		t.Fatalf("the ceiling is %v but the read held its connection for %v", pol.ReadTimeout, elapsed.Round(time.Millisecond))
	}
	// The connection has to be reusable afterwards. A cancellation driven
	// from the Go side would have closed it, and the pool would be paying a
	// reconnect for every timed-out request precisely when it is shortest.
	//
	// This probe is the NEXT request, so it carries the next request's
	// budget: what is being asked is whether the pool still has a usable
	// connection, not whether the request that just burned a ceiling may
	// have another go. A budget that has already been timed out is refused
	// by design -- TestIntegrationStatementTimeoutStopsFollowUpReads pins
	// that -- and reusing this one here would be asking both questions at
	// once and reading the answer as if it were only the first.
	next := WithQueryClass(context.Background(), ClassInteractive)
	var one int
	if err := pg.withConn(next, func(c *pgx.Conn) error {
		return c.QueryRow(next, "SELECT 1").Scan(&one)
	}); err != nil {
		t.Fatalf("the pool could not serve a read after a timeout: %v", err)
	}
	if got := pg.PoolStats(); classStat(t, got, "interactive").Timeouts != 1 {
		t.Fatalf("the timeout was not counted: %+v", classStat(t, got, "interactive"))
	}
}

// The read amplification behind the 2026-09-10 incident, end to end against
// a real PostgreSQL that really cancels the statement.
//
// One page makes eight or more sequential store reads and only the first is
// fatal, so before this was pinned a saturated database was asked all eight
// times and each one was allowed to spend a whole ceiling. The visitor got
// nothing for 8 x ReadTimeout and left; the database got eight more expensive
// queries while it was already the thing that was too slow. A request that
// has been told "not now" once has been told about the database, not about
// the query, so the answer cannot improve within that same request.
func TestIntegrationStatementTimeoutStopsFollowUpReads(t *testing.T) {
	pol := testPoolPolicy()
	pg := openTestPGWithPolicy(t, pol)

	budget := NewQueryBudget(ClassInteractive)
	ctx := WithQueryBudget(context.Background(), budget)

	// The page's first read outlives the ceiling and PostgreSQL kills it.
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, execErr := c.Exec(ctx, "SELECT pg_sleep(60)")
		return execErr
	})
	if !IsQueryTimeout(err) {
		t.Fatalf("first read returned %v, want a statement timeout", err)
	}

	// Every later read on the same page must be refused without asking the
	// database anything, and must be refused as backpressure so the route
	// answers 503 with a Retry-After rather than 404 or 500.
	start := time.Now()
	for i := 0; i < 7; i++ {
		var one int
		followUp := pg.withConn(ctx, func(c *pgx.Conn) error {
			return c.QueryRow(ctx, "SELECT 1").Scan(&one)
		})
		if !IsPoolBusy(followUp) {
			t.Fatalf("follow-up read %d returned %v, want ErrPoolBusy", i+1, followUp)
		}
	}
	// Seven refusals that each reached PostgreSQL would cost seven ceilings.
	if elapsed := time.Since(start); elapsed > pol.ReadTimeout {
		t.Fatalf("seven follow-up reads spent %v, want no database work at all",
			elapsed.Round(time.Millisecond))
	}
	if got := budget.Suppressed(); got != 7 {
		t.Fatalf("suppressed follow-ups = %d, want 7", got)
	}
	// Exactly one statement was ever cancelled: the first one.
	if got := classStat(t, pg.PoolStats(), "interactive").Timeouts; got != 1 {
		t.Fatalf("statement timeouts = %d, want 1", got)
	}

	// The next request is unaffected -- this bounds one request, and does not
	// put the server into a mode.
	next := WithQueryClass(context.Background(), ClassInteractive)
	var one int
	if err := pg.withConn(next, func(c *pgx.Conn) error {
		return c.QueryRow(next, "SELECT 1").Scan(&one)
	}); err != nil {
		t.Fatalf("a fresh request after a timed-out one was refused: %v", err)
	}
	if one != 1 {
		t.Fatalf("fresh request read %d, want 1", one)
	}
}

// Background work asked for no ceiling and must not have been given one: the
// aggregation pass and a 500-batch ingest legitimately outlive any number a
// page read would tolerate.
func TestIntegrationBackgroundWorkHasNoStatementCeiling(t *testing.T) {
	pol := testPoolPolicy()
	pg := openTestPGWithPolicy(t, pol)

	ctx := WithQueryClass(context.Background(), ClassBackground)
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		// Comfortably past ReadTimeout, short enough to keep the suite fast.
		_, execErr := c.Exec(ctx, "SELECT pg_sleep(2)")
		return execErr
	})
	if err != nil {
		t.Fatalf("background work was cut short by a ceiling it never asked for: %v", err)
	}
}

// Once the read share is gone, the next reader is told so. Waiting is what
// produced the 63-second 502s: a refusal at 400ms is a page the visitor can
// retry and a line the operator can read.
func TestIntegrationSaturatedReadsAreRefusedNotQueuedForever(t *testing.T) {
	pol := testPoolPolicy()
	pg := openTestPGWithPolicy(t, pol)
	occupy(t, pg, ClassInteractive, pol.InteractiveConns)

	budget := NewQueryBudget(ClassInteractive)
	ctx := WithQueryBudget(context.Background(), budget)
	start := time.Now()
	var one int
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT 1").Scan(&one)
	})
	elapsed := time.Since(start)
	if err == nil {
		// Legitimate only if a slow query happened to finish first, which
		// under pg_sleep(60) it cannot have.
		t.Fatalf("a read succeeded while every read connection was held for a minute")
	}
	if !IsPoolBusy(err) {
		t.Fatalf("a saturated pool returned %v; an operator cannot tell that apart from a dead database", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("saturation surfaced as a context deadline: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("the refusal took %v; the wait budget is %v", elapsed.Round(time.Millisecond), pol.ReadWait)
	}
	if busy, _, waited := budget.Pressure(); busy != 1 || waited < pol.ReadWait {
		t.Fatalf("the request's own record of the refusal is wrong: busy=%d waited=%v", busy, waited)
	}
	if got := classStat(t, pg.PoolStats(), "interactive"); got.Busy != 1 {
		t.Fatalf("the refusal was not counted: %+v", got)
	}
}

// The numbers an operator needs during an incident: who is holding the pool,
// how long anyone had to wait for it, and what was refused.
func TestIntegrationPoolStatsReportWhoIsHoldingTheDatabase(t *testing.T) {
	pol := testPoolPolicy()
	pg := openTestPGWithPolicy(t, pol)
	occupy(t, pg, ClassInteractive, pol.InteractiveConns)

	stats := pg.PoolStats()
	if !stats.Enabled || stats.MaxConns != pol.MaxConns {
		t.Fatalf("policy not reported: %+v", stats)
	}
	if stats.InUse < pol.InteractiveConns {
		t.Fatalf("%d connections are held by slow reads but InUse is %d", pol.InteractiveConns, stats.InUse)
	}
	inter := classStat(t, stats, "interactive")
	if inter.InUse != pol.InteractiveConns {
		t.Fatalf("interactive InUse is %d, want %d", inter.InUse, pol.InteractiveConns)
	}
	if inter.Limit != pol.InteractiveConns {
		t.Fatalf("interactive Limit is %d, want %d", inter.Limit, pol.InteractiveConns)
	}
	// A probe now has to wait for nothing, so its wait must stay at zero
	// while the read class is at its ceiling: that zero is the evidence the
	// reserve is real.
	probeCtx := WithQueryClass(context.Background(), ClassProbe)
	var one int
	if err := pg.withConn(probeCtx, func(c *pgx.Conn) error {
		return c.QueryRow(probeCtx, "SELECT 1").Scan(&one)
	}); err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if probe := classStat(t, pg.PoolStats(), "probe"); probe.Waited != 0 {
		t.Fatalf("the probe waited %d times for a connection it is supposed to have reserved", probe.Waited)
	}
}

// Rollback has to be one switch, and it has to put back what was there. With
// the guard off the pool is one shared cap with no ceiling and no refusal --
// the shape R2C-55 ran on.
func TestIntegrationDisabledGuardRestoresTheOldPool(t *testing.T) {
	pol := testPoolPolicy()
	pol.Enabled = false
	pg := openTestPGWithPolicy(t, pol)

	ctx := WithQueryClass(context.Background(), ClassInteractive)
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, execErr := c.Exec(ctx, "SELECT pg_sleep(2)")
		return execErr
	})
	if err != nil {
		t.Fatalf("the disabled guard still cut a read short: %v", err)
	}
	if stats := pg.PoolStats(); stats.Enabled {
		t.Fatalf("PoolStats claims the guard is on: %+v", stats)
	}
}

func classStat(t *testing.T, s PoolStats, class string) ClassPoolStats {
	t.Helper()
	for _, c := range s.Classes {
		if c.Class == class {
			return c
		}
	}
	names := make([]string, 0, len(s.Classes))
	for _, c := range s.Classes {
		names = append(names, c.Class)
	}
	t.Fatalf("no stats for class %q, only %s", class, strings.Join(names, ", "))
	return ClassPoolStats{}
}
