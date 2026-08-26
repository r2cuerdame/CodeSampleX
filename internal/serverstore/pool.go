package serverstore

// The database pool and the budgets that keep one slow query from taking the
// site down with it.
//
// R2C-55 measured what the previous pool did under load: a single /wanted
// query that took 8s held one of eight connections for those 8s, ten
// concurrent visitors held all eight, and the ninth and tenth waited until
// the HTTP WriteTimeout turned them into 502s at 63 seconds. Nothing in that
// chain was broken -- every part did exactly what it was written to do. What
// was missing was a ceiling: no statement ever had to finish, and no class of
// work was guaranteed a connection.
//
// Two mechanisms, in this order:
//
//  1. statement_timeout, applied per class on checkout. PostgreSQL cancels
//     the statement itself and hands back SQLSTATE 57014, which leaves the
//     connection usable and returns it to the pool. Cancelling from the Go
//     side instead would break the connection and pay a reconnect exactly
//     when the pool is already short.
//  2. Per-class admission. Every class holds a bounded share of the pool, so
//     the arithmetic guarantees a floor for the others: with the shipped
//     policy the health probe always has a connection, a page read always
//     has two, and ingest always has one, no matter what the other classes
//     are doing.
//
// Deliberately absent: any bound on work that did not ask for one. The zero
// QueryClass is ClassBackground and behaves exactly as this package did
// before -- migrations, the aggregation builder, the CLI and every test keep
// their old semantics, and only a caller that classifies itself gets a
// ceiling. A blanket timeout would have been one line and would have started
// killing the hour-long jobs this server is also for.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// QueryClass says what a database connection is being used FOR. It travels
// in the context because the store's methods are shared: the same
// ListWanted serves a visitor's page view and an operator's dashboard, and
// only the caller knows which one is waiting.
type QueryClass uint8

const (
	// ClassBackground is the zero value: work nobody is watching a spinner
	// for. Ingest, migrations, the aggregation builder, the CLI, tests. No
	// statement ceiling -- some of this legitimately takes minutes.
	ClassBackground QueryClass = iota
	// ClassInteractive is a query a person or an agent is blocked on: a
	// website page, a public API read, a search. These get a ceiling, and
	// they are the class that must never be able to take the pool.
	ClassInteractive
	// ClassProbe is the liveness check and nothing else. It runs one
	// trivial statement and is the last class that may be starved, because
	// starving it takes the container down on top of the incident.
	ClassProbe
)

func (c QueryClass) String() string {
	switch c {
	case ClassInteractive:
		return "interactive"
	case ClassProbe:
		return "probe"
	default:
		return "background"
	}
}

// ErrPoolBusy is returned when a caller's class had no connection to give
// within its wait budget. It is deliberately its own error and not a
// context deadline: "the database is saturated" and "this request ran out
// of time" want different HTTP statuses and very different operator
// responses, and telling them apart afterwards from a wrapped
// context.DeadlineExceeded is guesswork.
var ErrPoolBusy = errors.New("serverstore: no database connection available within the wait budget")

// IsPoolBusy reports whether err is the pool refusing to make a caller wait
// any longer.
func IsPoolBusy(err error) bool { return errors.Is(err, ErrPoolBusy) }

// IsQueryTimeout reports whether PostgreSQL cancelled the statement because
// it outlived its statement_timeout (SQLSTATE 57014, "canceling statement
// due to statement timeout"). A cancel requested by a client carries the
// same SQLSTATE with a different message, so the message is part of the
// test: an operator reading "query timeout" must be reading about a
// ceiling this code set, not about a client that hung up.
func IsQueryTimeout(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
		return false
	}
	return pgErr.Message == "canceling statement due to statement timeout"
}

// IsQueryCanceled reports the wider condition: the statement did not finish
// because something cancelled it, whether a timeout or a cancel request.
func IsQueryCanceled(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "57014"
}

// PoolPolicy is the whole defense line, as numbers an operator can change
// without a deploy.
//
// The shares are not a partition: they overlap on purpose, so spare capacity
// stays usable by whoever needs it and only the floor is reserved. What each
// class is guaranteed is (pool - probe reserve) minus the sum of the OTHER
// classes' caps, which is why the caps deliberately add up to more than the
// pool.
type PoolPolicy struct {
	// Enabled is the rollback switch (CSX_DB_POOL_GUARD=off). Disabled, the
	// pool behaves exactly as it did before R2C-58: one shared cap, no
	// statement ceiling, no wait budget.
	Enabled bool
	// MaxConns is the total number of PostgreSQL connections this process
	// may hold.
	MaxConns int
	// ProbeReserve is how many connections no class but ClassProbe may
	// occupy.
	ProbeReserve int
	// InteractiveConns and BackgroundConns cap their classes.
	InteractiveConns int
	BackgroundConns  int
	// ReadTimeout is statement_timeout for ClassInteractive; 0 means none.
	ReadTimeout time.Duration
	// ReadWait is how long ClassInteractive waits for a connection before
	// ErrPoolBusy; 0 means "as long as the context allows".
	ReadWait time.Duration
	// ProbeTimeout and ProbeWait are the same two numbers for ClassProbe.
	ProbeTimeout time.Duration
	ProbeWait    time.Duration
}

// defaultMaxConns caps concurrent PostgreSQL connections per process.
const defaultMaxConns = 8

// DefaultPoolPolicy is the shipped, deliberately conservative setting.
//
// ReadTimeout is 8s and not 1s on purpose. The point is a ceiling, not a
// tight SLA: the slowest page this site is known to have served under load
// took 9.3s, ordinary pages take 30-170ms, and a ceiling set near the
// ordinary case would turn a slow morning into an outage of its own. 8s is
// far below the 60s WriteTimeout that produced the 502s and far above
// anything healthy.
func DefaultPoolPolicy() PoolPolicy {
	return PoolPolicy{
		Enabled:          true,
		MaxConns:         defaultMaxConns,
		ProbeReserve:     1,
		InteractiveConns: 6,
		BackgroundConns:  5,
		ReadTimeout:      8 * time.Second,
		ReadWait:         3 * time.Second,
		// The probe's two budgets add up to the 3s deadline handleHealthz
		// already puts on its own context, so the cancellation comes from
		// PostgreSQL and the connection survives it. Letting the Go-side
		// deadline win instead would break one connection every time the
		// database is slow -- during the exact minute the pool is shortest.
		ProbeTimeout: 2 * time.Second,
		ProbeWait:    time.Second,
	}
}

// normalize clamps a policy into something the pool can honour. A
// misconfigured share must not be able to deadlock the server, so every
// bound is forced back into range rather than rejected.
func (p PoolPolicy) normalize() PoolPolicy {
	if p.MaxConns < 1 {
		p.MaxConns = defaultMaxConns
	}
	if !p.Enabled {
		return PoolPolicy{MaxConns: p.MaxConns}
	}
	if p.ProbeReserve < 0 {
		p.ProbeReserve = 0
	}
	// The reserve can never take the last general connection: a pool that
	// only probes may enter serves nothing.
	if p.ProbeReserve > p.MaxConns-1 {
		p.ProbeReserve = p.MaxConns - 1
	}
	general := p.MaxConns - p.ProbeReserve
	clamp := func(n int) int {
		if n < 1 || n > general {
			return general
		}
		return n
	}
	p.InteractiveConns = clamp(p.InteractiveConns)
	p.BackgroundConns = clamp(p.BackgroundConns)
	for _, d := range []*time.Duration{&p.ReadTimeout, &p.ReadWait, &p.ProbeTimeout, &p.ProbeWait} {
		if *d < 0 {
			*d = 0
		}
	}
	return p
}

// general is how many connections every class except ClassProbe shares.
func (p PoolPolicy) general() int { return p.MaxConns - p.ProbeReserve }

func (p PoolPolicy) statementTimeout(c QueryClass) time.Duration {
	if !p.Enabled {
		return 0
	}
	switch c {
	case ClassInteractive:
		return p.ReadTimeout
	case ClassProbe:
		return p.ProbeTimeout
	default:
		return 0
	}
}

func (p PoolPolicy) wait(c QueryClass) time.Duration {
	if !p.Enabled {
		return 0
	}
	switch c {
	case ClassInteractive:
		return p.ReadWait
	case ClassProbe:
		return p.ProbeWait
	default:
		return 0
	}
}

// ------------------------------------------------------------- budgets --

// QueryBudget is one caller's record of what the pool cost it: the class it
// asked as, how long it waited, and whether it was refused or cancelled.
//
// It exists so that this package stays silent. serverstore has never
// written a log line and should not start: it does not know the request
// path, and a store that logs is a store that logs in tests. The HTTP layer
// creates one budget per request, reads it afterwards and writes the one
// line an operator needs.
type QueryBudget struct {
	class    QueryClass
	waitedNS atomic.Int64
	busy     atomic.Int64
	timeouts atomic.Int64
}

// NewQueryBudget returns a budget for one unit of work in one class. A
// class this package does not know is background: the counters are a fixed
// array indexed by class, and an out-of-range value would take the server
// down from the observability code rather than from the work.
func NewQueryBudget(class QueryClass) *QueryBudget {
	if class > ClassProbe {
		class = ClassBackground
	}
	return &QueryBudget{class: class}
}

// Class reports the class this budget was opened as.
func (b *QueryBudget) Class() QueryClass {
	if b == nil {
		return ClassBackground
	}
	return b.class
}

// Pressure reports what this unit of work ran into: how many acquisitions
// were refused, how many statements PostgreSQL cancelled on a ceiling, and
// the total time spent waiting for a connection.
func (b *QueryBudget) Pressure() (busy, timeouts int64, waited time.Duration) {
	if b == nil {
		return 0, 0, 0
	}
	return b.busy.Load(), b.timeouts.Load(), time.Duration(b.waitedNS.Load())
}

type queryBudgetKey struct{}

// WithQueryBudget attaches a budget to ctx. Every store call made under
// that context acquires connections in its class and reports into it.
func WithQueryBudget(ctx context.Context, b *QueryBudget) context.Context {
	if b == nil {
		return ctx
	}
	return context.WithValue(ctx, queryBudgetKey{}, b)
}

// WithQueryClass is WithQueryBudget for a caller that wants the class but
// has nowhere to read the counters back -- a background loop, a test.
func WithQueryClass(ctx context.Context, class QueryClass) context.Context {
	return WithQueryBudget(ctx, NewQueryBudget(class))
}

// BudgetOf returns the budget attached to ctx, or nil.
func BudgetOf(ctx context.Context) *QueryBudget {
	b, _ := ctx.Value(queryBudgetKey{}).(*QueryBudget)
	return b
}

// QueryClassOf reports the class ctx asks as; unclassified is background.
func QueryClassOf(ctx context.Context) QueryClass { return BudgetOf(ctx).Class() }

// ---------------------------------------------------------------- stats --

// ClassPoolStats is one class's share of the pool and what it has cost.
type ClassPoolStats struct {
	Class     string
	Limit     int    // connections this class may hold at once
	InUse     int    // held right now
	Acquired  uint64 // successful acquisitions
	Waited    uint64 // acquisitions that could not be served immediately
	WaitTotal time.Duration
	WaitMax   time.Duration
	Busy      uint64 // refused: ErrPoolBusy
	Timeouts  uint64 // statements PostgreSQL cancelled on this class's ceiling
}

// PoolStats is the pool as an operator needs to see it: how much of it is
// in use, who is using it, how long they waited and what was refused.
type PoolStats struct {
	Enabled  bool
	MaxConns int
	Open     int // connections this process currently holds
	InUse    int
	Idle     int
	Classes  []ClassPoolStats
}

type classCounters struct {
	inUse     atomic.Int64
	acquired  atomic.Uint64
	waited    atomic.Uint64
	waitNS    atomic.Uint64
	waitMaxNS atomic.Uint64
	busy      atomic.Uint64
	timeouts  atomic.Uint64
}

func (c *classCounters) observeWait(d time.Duration) {
	c.acquired.Add(1)
	if d <= 0 {
		return
	}
	c.waited.Add(1)
	c.waitNS.Add(uint64(d))
	for {
		cur := c.waitMaxNS.Load()
		if uint64(d) <= cur || c.waitMaxNS.CompareAndSwap(cur, uint64(d)) {
			return
		}
	}
}

// ----------------------------------------------------------------- pool --

// pooledConn is a connection plus the two things the pool has to remember
// about it: the statement ceiling currently set on the session, so the same
// SET is not paid on every checkout, and the admission tokens this checkout
// is holding, so releasing gives back exactly what acquiring took.
type pooledConn struct {
	conn        *pgx.Conn
	stmtTimeout time.Duration // -1 until the session has been told once
	class       QueryClass
	gates       []chan struct{}
}

// connPool is a minimal *pgx.Conn pool: a semaphore bounds total open
// connections, a buffered channel holds idle ones, and per-class gates
// bound what any one kind of work can take.
type connPool struct {
	cfg    *pgx.ConnConfig
	pol    PoolPolicy
	idle   chan *pooledConn
	sem    chan struct{} // one token per open connection
	closed atomic.Bool

	general chan struct{} // every class except ClassProbe
	inter   chan struct{} // ClassInteractive only
	back    chan struct{} // ClassBackground only

	stats [3]classCounters
}

func newConnPool(cfg *pgx.ConnConfig, pol PoolPolicy) *connPool {
	pol = pol.normalize()
	p := &connPool{
		cfg:  cfg,
		pol:  pol,
		idle: make(chan *pooledConn, pol.MaxConns),
		sem:  make(chan struct{}, pol.MaxConns),
	}
	if pol.Enabled {
		p.general = make(chan struct{}, pol.general())
		p.inter = make(chan struct{}, pol.InteractiveConns)
		p.back = make(chan struct{}, pol.BackgroundConns)
	}
	return p
}

// gatesFor is the admission order for a class, outermost first. Probes pass
// none of them: their ceiling is the reserve the other classes cannot enter
// plus a 3s statement timeout, which is a tighter bound than any queue.
func (p *connPool) gatesFor(class QueryClass) []chan struct{} {
	if !p.pol.Enabled {
		return nil
	}
	switch class {
	case ClassProbe:
		return nil
	case ClassInteractive:
		return []chan struct{}{p.inter, p.general}
	default:
		return []chan struct{}{p.back, p.general}
	}
}

func (p *connPool) acquire(ctx context.Context) (*pooledConn, error) {
	if p.closed.Load() {
		return nil, errors.New("serverstore: store is closed")
	}
	budget := BudgetOf(ctx)
	class := budget.Class()
	counters := &p.stats[class]

	waitCtx, cancel := p.waitContext(ctx, class)
	defer cancel()

	start := time.Now()
	var held []chan struct{}
	fail := func(err error) (*pooledConn, error) {
		for i := len(held) - 1; i >= 0; i-- {
			<-held[i]
		}
		p.charge(budget, counters, time.Since(start), -1)
		if errors.Is(err, ErrPoolBusy) {
			counters.busy.Add(1)
			if budget != nil {
				budget.busy.Add(1)
			}
		}
		return nil, err
	}
	for _, g := range p.gatesFor(class) {
		select {
		case g <- struct{}{}:
			held = append(held, g)
			continue
		default:
		}
		select {
		case g <- struct{}{}:
			held = append(held, g)
		case <-waitCtx.Done():
			return fail(p.waitErr(ctx, class))
		}
	}

	granted := time.Duration(-1)
	c, err := p.checkout(waitCtx, start, &granted)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return fail(p.waitErr(ctx, class))
		}
		return fail(err)
	}
	c.class = class
	c.gates = held
	if err := p.applyStatementTimeout(ctx, c, class); err != nil {
		p.discard(c)
		return fail(err)
	}
	p.charge(budget, counters, time.Since(start), granted)
	counters.inUse.Add(1)
	return c, nil
}

// waitContext bounds how long a class is willing to queue. Background work
// keeps the caller's context and nothing more, which is what it had before.
func (p *connPool) waitContext(ctx context.Context, class QueryClass) (context.Context, context.CancelFunc) {
	d := p.pol.wait(class)
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// waitErr tells the two reasons a wait ended apart. The caller's own
// context winning is the caller's business; our budget winning is
// saturation, and saturation is the answer an operator is looking for.
func (p *connPool) waitErr(ctx context.Context, class QueryClass) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w (class %s, waited %s)", ErrPoolBusy, class, p.pol.wait(class))
}

// charge records what this acquisition cost. total is the caller's whole
// wall clock and goes on the request's own budget; queued is the part spent
// waiting for a connection to become available, and only that part is a
// wait. Dialing a new connection and setting its ceiling are work the pool
// does on the caller's behalf, not contention, and counting them would make
// a healthy idle server look permanently queued. queued < 0 means the
// acquisition never got that far.
func (p *connPool) charge(budget *QueryBudget, counters *classCounters, total, queued time.Duration) {
	if budget != nil {
		budget.waitedNS.Add(int64(total))
	}
	if queued < 0 {
		return
	}
	// Sub-millisecond acquisitions are the normal case and are not
	// "waiting"; counting them would bury the ones that were.
	if queued < time.Millisecond {
		queued = 0
	}
	counters.observeWait(queued)
}

// checkout hands out an idle connection or opens a new one under the cap.
// It reports through queued how long the caller spent waiting for one to be
// available, measured to the moment the pool committed a slot -- see charge.
func (p *connPool) checkout(ctx context.Context, start time.Time, queued *time.Duration) (*pooledConn, error) {
	select {
	case c := <-p.idle:
		*queued = time.Since(start)
		return p.ensureAlive(ctx, c)
	default:
	}
	select {
	case c := <-p.idle:
		*queued = time.Since(start)
		return p.ensureAlive(ctx, c)
	case p.sem <- struct{}{}:
		*queued = time.Since(start)
		conn, err := pgx.ConnectConfig(ctx, p.cfg)
		if err != nil {
			<-p.sem
			return nil, fmt.Errorf("serverstore: connect: %w", err)
		}
		return &pooledConn{conn: conn, stmtTimeout: -1}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ensureAlive replaces a dead idle connection, keeping its sem token.
func (p *connPool) ensureAlive(ctx context.Context, c *pooledConn) (*pooledConn, error) {
	if !c.conn.IsClosed() {
		return c, nil
	}
	fresh, err := pgx.ConnectConfig(ctx, p.cfg)
	if err != nil {
		<-p.sem
		return nil, fmt.Errorf("serverstore: reconnect: %w", err)
	}
	c.conn = fresh
	c.stmtTimeout = -1
	return c, nil
}

// applyStatementTimeout puts this class's ceiling on the session, and only
// when it is not already there. The SET is a round trip; paying it on every
// checkout would tax the fast pages this change exists to protect, and in
// practice the pool settles into per-class connections and pays it almost
// never.
func (p *connPool) applyStatementTimeout(ctx context.Context, c *pooledConn, class QueryClass) error {
	want := p.pol.statementTimeout(class)
	if c.stmtTimeout == want {
		return nil
	}
	// Bounded on its own: a SET that cannot complete is a connection that
	// must not be handed out, not one more thing to wait on.
	setCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := c.conn.Exec(setCtx, "SET statement_timeout = "+strconv.FormatInt(want.Milliseconds(), 10)); err != nil {
		return fmt.Errorf("serverstore: set statement_timeout: %w", err)
	}
	c.stmtTimeout = want
	return nil
}

// discard closes a connection the pool must not keep and gives back its
// slot. Gate tokens belong to the checkout and are released by the caller.
func (p *connPool) discard(c *pooledConn) {
	_ = c.conn.Close(context.Background())
	<-p.sem
}

func (p *connPool) release(c *pooledConn) {
	if c == nil {
		return
	}
	gates, class := c.gates, c.class
	c.gates, c.class = nil, ClassBackground
	p.stats[class].inUse.Add(-1)
	defer func() {
		for i := len(gates) - 1; i >= 0; i-- {
			<-gates[i]
		}
	}()
	if p.closed.Load() || c.conn.IsClosed() {
		_ = c.conn.Close(context.Background())
		<-p.sem
		return
	}
	p.idle <- c // never blocks: cap(idle) == cap(sem)
}

// observeQueryError counts what the ceiling actually caught, for the class
// that was holding the connection.
func (p *connPool) observeQueryError(ctx context.Context, err error) {
	if err == nil || !IsQueryCanceled(err) {
		return
	}
	budget := BudgetOf(ctx)
	p.stats[budget.Class()].timeouts.Add(1)
	if budget != nil {
		budget.timeouts.Add(1)
	}
}

func (p *connPool) close() {
	if p.closed.Swap(true) {
		return
	}
	for {
		select {
		case c := <-p.idle:
			_ = c.conn.Close(context.Background())
			<-p.sem
		default:
			return
		}
	}
}

func (p *connPool) stat() PoolStats {
	open := len(p.sem)
	idle := len(p.idle)
	s := PoolStats{
		Enabled:  p.pol.Enabled,
		MaxConns: p.pol.MaxConns,
		Open:     open,
		Idle:     idle,
		InUse:    max(open-idle, 0),
	}
	limits := map[QueryClass]int{
		ClassBackground:  p.pol.BackgroundConns,
		ClassInteractive: p.pol.InteractiveConns,
		ClassProbe:       p.pol.MaxConns,
	}
	for _, class := range []QueryClass{ClassInteractive, ClassBackground, ClassProbe} {
		c := &p.stats[class]
		limit := p.pol.MaxConns
		if p.pol.Enabled {
			limit = limits[class]
		}
		s.Classes = append(s.Classes, ClassPoolStats{
			Class:     class.String(),
			Limit:     limit,
			InUse:     int(c.inUse.Load()),
			Acquired:  c.acquired.Load(),
			Waited:    c.waited.Load(),
			WaitTotal: time.Duration(c.waitNS.Load()),
			WaitMax:   time.Duration(c.waitMaxNS.Load()),
			Busy:      c.busy.Load(),
			Timeouts:  c.timeouts.Load(),
		})
	}
	return s
}
