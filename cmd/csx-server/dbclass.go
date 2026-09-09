package main

// Which database budget a request is allowed to spend, and the one log line
// an operator gets when it could not.
//
// The rule is "bounded unless named otherwise". Everything the public can
// reach is a read someone is waiting on and gets the interactive ceiling;
// the routes that legitimately take minutes -- evidence ingest, sample
// upload, authoring work, the operator dashboard -- are listed here by name
// and keep the unbounded behaviour they had before. Getting that backwards
// would be the dangerous direction: an unlisted read is merely capped at
// eight seconds, while an unlisted long job would start dying at eight
// seconds, and a list of long jobs is short and knowable while a list of
// every read is neither.
//
// Method is not the test. POST /v1/search and POST /v2/search are the
// primary read path for every MCP client in the network -- an agent is
// blocked on them exactly as a browser is blocked on /wanted -- so
// classifying by verb would have left the busiest reads unbounded and put a
// ceiling on nothing that mattered.

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// longRunningPrefixes are the paths whose work is expected to outlive any
// page-view ceiling: they write, they upload artifacts, or they aggregate on
// an operator's behalf.
var longRunningPrefixes = []string{
	"/v1/evidence/",
	"/v1/samples",           // POST upload; the GET reads are bounded below
	"/v1/authoring/",        // draft submission and work leases
	"/v1/verifications",     // receipt ingest
	"/v1/wanted/batches",    // bulk ask ingest
	"/v1/verification/jobs", // the fleet's own queue, polled continuously
	"/admin",                // the operator dashboard aggregates on purpose
	// /sitemap.xml and /sitemaps/*: at most one request per freshness
	// window rebuilds the whole indexable corpus on a crawler's behalf —
	// an aggregate by design, like /admin — and every other request serves
	// from memory without touching the database at all.
	"/sitemap",
}

// dbClassFor decides what a request may ask of the database.
func dbClassFor(r *http.Request) serverstore.QueryClass {
	path := r.URL.Path
	if path == "/healthz" {
		return serverstore.ClassProbe
	}
	// A GET of a sample or its artifact is a read a visitor is waiting on;
	// only the upload is long. They share a prefix, so the verb decides
	// between those two and nowhere else.
	if strings.HasPrefix(path, "/v1/samples") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return serverstore.ClassInteractive
	}
	for _, p := range longRunningPrefixes {
		if strings.HasPrefix(path, p) {
			return serverstore.ClassBackground
		}
	}
	return serverstore.ClassInteractive
}

// budgetPressureWindow rate-limits the pressure log. During an incident every
// request in flight hits the same wall at the same moment; one line per
// second per class says everything ten thousand lines would, and leaves the
// disk for the access log.
const budgetPressureWindow = time.Second

// withDBBudget gives every request its own budget, in its own class, and
// writes one line when the request ran into the pool.
//
// The line is written here and not in serverstore on purpose: the store does
// not know the request path, and a store that logs is a store that logs in
// every test that touches it.
func withDBBudget(next http.Handler) http.Handler {
	throttle := newPressureLog()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		budget := serverstore.NewQueryBudget(dbClassFor(r))
		ctx, refusals := withRequestPressure(serverstore.WithQueryBudget(r.Context(), budget))
		next.ServeHTTP(w, r.WithContext(ctx))
		busy, timeouts, waited := budget.Pressure()
		admission, deferred := refusals.admissionRefused.Load(), refusals.deferredRefused.Load()
		// A request refused above the pool never touches the budget, so
		// before #174 it left no trace at all: the canonical v0.1.153
		// observation read "pressure 0" while the site served 503s that the
		// cache-miss admission gate and the deferred lanes had produced.
		if busy == 0 && timeouts == 0 && admission == 0 && deferred == 0 {
			return
		}
		// Path only, never the query string: what someone searched for is
		// theirs, and the route is what identifies the problem anyway.
		throttle.report(budget.Class(), r.URL.Path, busy, timeouts, admission, deferred, waited)
	})
}

// pressureTotals is a readable snapshot of the counters below. It is a value,
// so nothing that reads it can accidentally keep counting.
type pressureTotals struct {
	poolBusy         int64
	queryTimeout     int64
	admissionRefused int64
	deferredRefused  int64
}

func (t pressureTotals) add(o pressureTotals) pressureTotals {
	return pressureTotals{
		poolBusy:         t.poolBusy + o.poolBusy,
		queryTimeout:     t.queryTimeout + o.queryTimeout,
		admissionRefused: t.admissionRefused + o.admissionRefused,
		deferredRefused:  t.deferredRefused + o.deferredRefused,
	}
}

// pressureCounters are cumulative, process-lifetime counts of every refusal
// this server produced, one set per query class.
//
// They exist because the log line below is capped at one per second per class:
// during an incident the line count measures how long the incident lasted, not
// how much traffic it refused, and the post-deploy observation counted lines.
// Four outcomes are separated on purpose, because the operator response to
// each is different:
//
//   - poolBusy: the pool refused an acquisition. The database is saturated.
//   - queryTimeout: a statement outlived its ceiling and was killed. One
//     query is too slow; the pool may be perfectly healthy.
//   - admissionRefused: the cache-miss admission gate refused a caller before
//     it ever reached the pool. The pool was never asked.
//   - deferredRefused: a cache lane is in its post-failure backoff and refused
//     without asking either.
//
// The last two are the ones that were invisible: they synthesise ErrPoolBusy,
// so a visitor sees a 503 and no counter anywhere moved.
type pressureCounters struct {
	poolBusy         [3]atomic.Int64
	queryTimeout     [3]atomic.Int64
	admissionRefused [3]atomic.Int64
	deferredRefused  [3]atomic.Int64
}

// dbPressure is process-wide on purpose. The refusals that never reach the
// pool are produced deep inside the web store, far from the request
// middleware that owns the log line, and some of them are produced by
// background refreshes that have no request at all.
var dbPressure pressureCounters

func (c *pressureCounters) observeBudget(class serverstore.QueryClass, busy, timeouts int64) {
	if busy > 0 {
		c.poolBusy[class].Add(busy)
	}
	if timeouts > 0 {
		c.queryTimeout[class].Add(timeouts)
	}
}

func (c *pressureCounters) observeAdmissionRefusal(class serverstore.QueryClass) {
	c.admissionRefused[class].Add(1)
}

func (c *pressureCounters) observeDeferredRefusal(class serverstore.QueryClass) {
	c.deferredRefused[class].Add(1)
}

func (c *pressureCounters) classTotals(class serverstore.QueryClass) pressureTotals {
	return pressureTotals{
		poolBusy:         c.poolBusy[class].Load(),
		queryTimeout:     c.queryTimeout[class].Load(),
		admissionRefused: c.admissionRefused[class].Load(),
		deferredRefused:  c.deferredRefused[class].Load(),
	}
}

// totals sums the classes. Every addend only ever grows, so a reader that
// races a writer sees a number between the true value before and after -- and
// the collector reduces with max, which that is safe for.
func (c *pressureCounters) totals() pressureTotals {
	var out pressureTotals
	for _, class := range []serverstore.QueryClass{
		serverstore.ClassBackground, serverstore.ClassInteractive, serverstore.ClassProbe,
	} {
		out = out.add(c.classTotals(class))
	}
	return out
}

// requestPressure is the part of the same accounting that belongs to one
// request, so the middleware can name the route that was refused. The
// process-wide counters cannot do that: they are shared.
type requestPressure struct {
	admissionRefused atomic.Int64
	deferredRefused  atomic.Int64
}

type requestPressureKey struct{}

func withRequestPressure(ctx context.Context) (context.Context, *requestPressure) {
	refusals := &requestPressure{}
	return context.WithValue(ctx, requestPressureKey{}, refusals), refusals
}

func requestPressureOf(ctx context.Context) *requestPressure {
	refusals, _ := ctx.Value(requestPressureKey{}).(*requestPressure)
	return refusals
}

// noteAdmissionRefusal and noteDeferredRefusal are what the web store calls at
// the moment it synthesises ErrPoolBusy without going near a connection. Both
// always advance the process counter; the per-request one only exists when
// there is a request, which a background cache refresh does not have.
func noteAdmissionRefusal(ctx context.Context) {
	dbPressure.observeAdmissionRefusal(serverstore.QueryClassOf(ctx))
	if refusals := requestPressureOf(ctx); refusals != nil {
		refusals.admissionRefused.Add(1)
	}
}

func noteDeferredRefusal(ctx context.Context) {
	dbPressure.observeDeferredRefusal(serverstore.QueryClassOf(ctx))
	if refusals := requestPressureOf(ctx); refusals != nil {
		refusals.deferredRefused.Add(1)
	}
}

type pressureLog struct {
	mu       sync.Mutex
	now      func() time.Time
	last     [3]time.Time
	out      func(format string, v ...any)
	counters *pressureCounters
}

func newPressureLog() *pressureLog {
	return &pressureLog{now: time.Now, out: log.Printf, counters: &dbPressure}
}

func (p *pressureLog) counterSet() *pressureCounters {
	if p.counters != nil {
		return p.counters
	}
	return &dbPressure
}

func (p *pressureLog) report(
	class serverstore.QueryClass,
	path string,
	busy, timeouts, admission, deferred int64,
	waited time.Duration,
) {
	// Counted before the throttle, never after: the suppressed reports are
	// exactly the ones a line count loses.
	counters := p.counterSet()
	counters.observeBudget(class, busy, timeouts)

	now := p.now()
	p.mu.Lock()
	if last := p.last[class]; !last.IsZero() && now.Sub(last) < budgetPressureWindow {
		p.mu.Unlock()
		return
	}
	p.last[class] = now
	p.mu.Unlock()

	// The totals ride along so that one line per second still carries an
	// exact count of everything the other lines would have said. The names
	// are suffixed rather than prefixed so that a reader matching the
	// per-request `pool_busy=` token cannot also match the total.
	totals := counters.totals()
	p.out("csx-server: db pressure path=%s class=%s cause=%s pool_busy=%d query_timeout=%d"+
		" admission_refused=%d deferred_refused=%d waited=%s"+
		" pool_busy_total=%d query_timeout_total=%d admission_refused_total=%d deferred_refused_total=%d",
		path, class, pressureCause(busy, timeouts, admission, deferred),
		busy, timeouts, admission, deferred, waited.Round(time.Millisecond),
		totals.poolBusy, totals.queryTimeout, totals.admissionRefused, totals.deferredRefused)
}

// pressureCause names every outcome the request actually hit, in the order an
// operator triages them: the pool first, then the statement ceiling, then the
// two refusals that never reached either.
func pressureCause(busy, timeouts, admission, deferred int64) string {
	causes := make([]string, 0, 4)
	for _, c := range []struct {
		count int64
		name  string
	}{
		{busy, "pool_busy"},
		{timeouts, "query_timeout"},
		{admission, "admission_refused"},
		{deferred, "deferred_refused"},
	} {
		if c.count > 0 {
			causes = append(causes, c.name)
		}
	}
	if len(causes) == 0 {
		return "none"
	}
	return strings.Join(causes, "+")
}
