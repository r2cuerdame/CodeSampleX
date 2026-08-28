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
	"log"
	"net/http"
	"strings"
	"sync"
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
		next.ServeHTTP(w, r.WithContext(serverstore.WithQueryBudget(r.Context(), budget)))
		busy, timeouts, waited := budget.Pressure()
		if busy == 0 && timeouts == 0 {
			return
		}
		// Path only, never the query string: what someone searched for is
		// theirs, and the route is what identifies the problem anyway.
		throttle.report(budget.Class(), r.URL.Path, busy, timeouts, waited)
	})
}

type pressureLog struct {
	mu   sync.Mutex
	now  func() time.Time
	last [3]time.Time
	out  func(format string, v ...any)
}

func newPressureLog() *pressureLog {
	return &pressureLog{now: time.Now, out: log.Printf}
}

func (p *pressureLog) report(class serverstore.QueryClass, path string, busy, timeouts int64, waited time.Duration) {
	now := p.now()
	p.mu.Lock()
	if last := p.last[class]; !last.IsZero() && now.Sub(last) < budgetPressureWindow {
		p.mu.Unlock()
		return
	}
	p.last[class] = now
	p.mu.Unlock()
	cause := "pool_busy"
	if busy == 0 {
		cause = "query_timeout"
	} else if timeouts > 0 {
		cause = "pool_busy+query_timeout"
	}
	p.out("csx-server: db pressure path=%s class=%s cause=%s pool_busy=%d query_timeout=%d waited=%s",
		path, class, cause, busy, timeouts, waited.Round(time.Millisecond))
}
