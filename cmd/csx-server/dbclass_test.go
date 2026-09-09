package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestDBClassForKnownRoutes(t *testing.T) {
	cases := []struct {
		method, path string
		want         serverstore.QueryClass
		why          string
	}{
		{"GET", "/healthz", serverstore.ClassProbe,
			"the health probe is the one request that may never be starved"},
		{"GET", "/wanted", serverstore.ClassInteractive,
			"the page whose slow query took the site down in R2C-55"},
		{"GET", "/", serverstore.ClassInteractive, "the landing page"},
		{"GET", "/ko/", serverstore.ClassInteractive, "a locale landing page"},
		{"GET", "/npm/zod", serverstore.ClassInteractive, "a package page"},
		{"GET", "/records", serverstore.ClassInteractive, "the record inventory"},
		{"POST", "/v1/search", serverstore.ClassInteractive,
			"an agent is blocked on search exactly as a browser is blocked on a page"},
		{"POST", "/v2/search", serverstore.ClassInteractive, "the same, one version on"},
		{"GET", "/v1/wanted", serverstore.ClassInteractive, "the public request board API"},
		{"GET", "/v1/stats", serverstore.ClassInteractive, "a snapshot read"},
		{"GET", "/v1/samples/abc123", serverstore.ClassInteractive,
			"reading a sample is a visitor waiting, not an upload"},
		{"GET", "/v1/samples/abc123/artifact", serverstore.ClassInteractive, "the same"},

		{"POST", "/v1/evidence/batches", serverstore.ClassBackground,
			"one request commits up to 500 batches in a single transaction"},
		{"POST", "/v1/samples", serverstore.ClassBackground, "sample upload writes an artifact"},
		{"POST", "/v1/authoring/drafts", serverstore.ClassBackground, "draft submission"},
		{"POST", "/v1/authoring/work/next", serverstore.ClassBackground, "a work lease"},
		{"POST", "/v1/verifications", serverstore.ClassBackground, "receipt ingest"},
		{"POST", "/v1/wanted/batches", serverstore.ClassBackground, "bulk ask ingest"},
		{"GET", "/v1/verification/jobs", serverstore.ClassBackground,
			"the fleet's own queue, polled continuously and not a page"},
		{"GET", "/admin", serverstore.ClassBackground,
			"the operator dashboard aggregates on purpose"},
		{"GET", "/admin/api/farm", serverstore.ClassBackground, "and so do its panels"},
		{"GET", "/sitemap.xml", serverstore.ClassBackground,
			"one rebuild per freshness window reads the whole indexable corpus; a crawler waits"},
		{"GET", "/sitemaps/samples-1.xml", serverstore.ClassBackground, "a shard of the same snapshot"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := dbClassFor(r); got != tc.want {
			t.Errorf("%s %s classified as %s, want %s (%s)", tc.method, tc.path, got, tc.want, tc.why)
		}
	}
}

// The dangerous direction is an unlisted long job, not an unlisted read: a
// read that nobody classified is merely capped, while a long job that nobody
// classified would start dying at eight seconds. So anything unknown must
// land on the bounded side.
func TestUnknownRoutesAreBoundedRatherThanUnbounded(t *testing.T) {
	for _, path := range []string{"/some/page/added/later", "/v1/something-new", "/features"} {
		if got := dbClassFor(httptest.NewRequest("GET", path, nil)); got != serverstore.ClassInteractive {
			t.Errorf("%s defaulted to %s; an unclassified route must be bounded", path, got)
		}
	}
}

func TestWithDBBudgetGivesEveryRequestItsOwnBudget(t *testing.T) {
	var seen []serverstore.QueryClass
	h := withDBBudget(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, serverstore.QueryClassOf(r.Context()))
	}))
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/wanted", nil),
		httptest.NewRequest("GET", "/healthz", nil),
		httptest.NewRequest("POST", "/v1/evidence/batches", nil),
	} {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	want := []serverstore.QueryClass{serverstore.ClassInteractive, serverstore.ClassProbe, serverstore.ClassBackground}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("request %d reached its handler as %s, want %s", i, seen[i], want[i])
		}
	}
}

// A quiet request writes nothing. During an incident there is one line per
// second per class, naming the cause -- which is the whole point: an operator
// reading the log must be able to tell "the database is saturated" from "this
// one query outlived its ceiling" without opening a database session.
func TestPressureLogNamesTheCauseAndDoesNotFlood(t *testing.T) {
	var lines []string
	clock := time.Unix(0, 0)
	p := &pressureLog{
		now: func() time.Time { return clock },
		out: func(format string, v ...any) { lines = append(lines, fmt.Sprintf(format, v...)) },
	}

	p.report(serverstore.ClassInteractive, "/wanted", 0, 1, 0, 0, 90*time.Millisecond)
	p.report(serverstore.ClassInteractive, "/records", 3, 0, 0, 0, 3*time.Second)
	if len(lines) != 1 {
		t.Fatalf("the second line in the same second was not throttled: %v", lines)
	}
	// A different class is a different problem and is never throttled away
	// by the noisy one.
	p.report(serverstore.ClassBackground, "/v1/evidence/batches", 1, 0, 0, 0, time.Second)
	if len(lines) != 2 {
		t.Fatalf("a second class was throttled by the first: %v", lines)
	}
	clock = clock.Add(budgetPressureWindow + time.Millisecond)
	p.report(serverstore.ClassInteractive, "/records", 3, 0, 0, 0, 3*time.Second)
	if len(lines) != 3 {
		t.Fatalf("the window never reopened: %v", lines)
	}

	if !strings.Contains(lines[0], "cause=query_timeout") || !strings.Contains(lines[0], "path=/wanted") || !strings.Contains(lines[0], "class=interactive") {
		t.Errorf("a timeout line does not name what happened: %q", lines[0])
	}
	if !strings.Contains(lines[2], "cause=pool_busy") || !strings.Contains(lines[2], "pool_busy=3") {
		t.Errorf("a saturation line does not name what happened: %q", lines[2])
	}
	// The query string never reaches the log: what someone searched for is
	// theirs, and the route is what identifies the problem.
	p.now = func() time.Time { return clock.Add(time.Minute) }
	p.report(serverstore.ClassInteractive, "/wanted", 1, 0, 0, 0, time.Second)
	if strings.Contains(lines[3], "?") {
		t.Errorf("the log line carries a query string: %q", lines[3])
	}
}

func TestPressureLogIsSafeForConcurrentRequests(t *testing.T) {
	p := &pressureLog{
		now: func() time.Time { return time.Unix(0, 0) },
		out: func(string, ...any) {},
	}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.report(serverstore.ClassInteractive, "/wanted", 1, 0, 0, 0, time.Second)
		}()
	}
	wg.Wait()
}

// The budget must survive being read after the handler returned, which is
// the only moment the middleware can read it.
func TestBudgetPressureIsReadableAfterTheHandlerReturns(t *testing.T) {
	budget := serverstore.NewQueryBudget(serverstore.ClassInteractive)
	ctx := serverstore.WithQueryBudget(context.Background(), budget)
	if got := serverstore.QueryClassOf(ctx); got != serverstore.ClassInteractive {
		t.Fatalf("the class did not travel: %s", got)
	}
	busy, timeouts, waited := budget.Pressure()
	if busy != 0 || timeouts != 0 || waited != 0 {
		t.Fatalf("an untouched budget reported %d/%d/%v", busy, timeouts, waited)
	}
}

// The pressure line is capped at one per second per class, so during an
// incident the number of lines is how long the incident lasted, not how many
// requests it refused. The v0.1.153 canonical observation counted lines and
// therefore reported zero refusals while the site was serving 503s. Every
// event must reach a counter even when its line is thrown away, and the line
// that does get written must carry the running total.
func TestPressureCountersCountEventsTheRateLimiterSuppresses(t *testing.T) {
	var lines []string
	clock := time.Unix(0, 0)
	counters := &pressureCounters{}
	p := &pressureLog{
		now:      func() time.Time { return clock },
		out:      func(format string, v ...any) { lines = append(lines, fmt.Sprintf(format, v...)) },
		counters: counters,
	}

	for i := 0; i < 10; i++ {
		p.report(serverstore.ClassInteractive, "/records", 2, 1, 0, 0, time.Second)
	}
	if len(lines) != 1 {
		t.Fatalf("the rate limiter wrote %d lines in one second, want 1: %v", len(lines), lines)
	}
	if got := counters.totals(); got.poolBusy != 20 || got.queryTimeout != 10 {
		t.Fatalf("nine suppressed reports were not counted: %+v", got)
	}

	clock = clock.Add(budgetPressureWindow + time.Millisecond)
	p.report(serverstore.ClassInteractive, "/records", 2, 1, 0, 0, time.Second)
	if len(lines) != 2 {
		t.Fatalf("the window never reopened: %v", lines)
	}
	// This is the whole point: an operator reading one line per second still
	// reads an exact count of everything the suppressed lines would have said.
	if !strings.Contains(lines[1], "pool_busy_total=22") || !strings.Contains(lines[1], "query_timeout_total=11") {
		t.Errorf("the published line does not carry the cumulative totals: %q", lines[1])
	}
}

func TestPressureCountersAttributeTimeoutsAndPoolRefusalsSeparately(t *testing.T) {
	counters := &pressureCounters{}
	p := &pressureLog{
		now:      func() time.Time { return time.Unix(0, 0) },
		out:      func(string, ...any) {},
		counters: counters,
	}
	// A statement killed by statement_timeout is charged to timeouts and to
	// nothing else; a refusal at the pool door is charged to pool_busy.
	p.report(serverstore.ClassInteractive, "/wanted", 0, 3, 0, 0, 0)
	p.report(serverstore.ClassBackground, "/v1/evidence/batches", 5, 0, 0, 0, time.Second)

	if got := counters.classTotals(serverstore.ClassInteractive); got.queryTimeout != 3 || got.poolBusy != 0 {
		t.Errorf("interactive timeouts were misattributed: %+v", got)
	}
	if got := counters.classTotals(serverstore.ClassBackground); got.poolBusy != 5 || got.queryTimeout != 0 {
		t.Errorf("background pool refusals were misattributed: %+v", got)
	}
	if got := counters.classTotals(serverstore.ClassProbe); got != (pressureTotals{}) {
		t.Errorf("a class that was never reported carries %+v", got)
	}
}

// A refusal produced above the pool -- the cache-miss admission gate, or a
// lane that is deferred after a failure -- is real refused traffic and must
// be counted, but it is not evidence that the pool itself was saturated.
func TestPackageLoadAdmissionRefusalIsCountedWithoutTouchingThePool(t *testing.T) {
	w := &webStore{} // no store: reaching the database would panic.
	ctx, pressure := withRequestPressure(
		serverstore.WithQueryBudget(context.Background(), serverstore.NewQueryBudget(serverstore.ClassInteractive)))

	held := make(chan struct{}, packageLoadSlotCount)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < packageLoadSlotCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.withPackageLoadSlot(context.Background(), func() error {
				held <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for i := 0; i < packageLoadSlotCount; i++ {
		<-held
	}

	before := dbPressure.totals()
	err := w.withPackageLoadSlot(ctx, func() error {
		t.Error("a refused caller still ran its store read")
		return nil
	})
	after := dbPressure.totals()
	close(release)
	wg.Wait()

	if !errors.Is(err, serverstore.ErrPoolBusy) {
		t.Fatalf("admission gate returned %v, want ErrPoolBusy", err)
	}
	if got := after.admissionRefused - before.admissionRefused; got != 1 {
		t.Errorf("the admission refusal moved the admission counter by %d, want 1", got)
	}
	if got := after.poolBusy - before.poolBusy; got != 0 {
		t.Errorf("a refusal that never reached the pool was charged to pool_busy (+%d)", got)
	}
	if got := pressure.admissionRefused.Load(); got != 1 {
		t.Errorf("the request itself recorded %d admission refusals, want 1", got)
	}
}

func TestDeferredLaneRefusalIsCountedWithoutTouchingThePool(t *testing.T) {
	w := &webStore{} // no store: reaching the database would panic.
	w.snapshotRetryAt = time.Now().Add(time.Hour)
	ctx, pressure := withRequestPressure(
		serverstore.WithQueryBudget(context.Background(), serverstore.NewQueryBudget(serverstore.ClassInteractive)))

	before := dbPressure.totals()
	_, err := w.cachedSnapshots(ctx)
	after := dbPressure.totals()

	if !errors.Is(err, serverstore.ErrPoolBusy) {
		t.Fatalf("deferred lane returned %v, want ErrPoolBusy", err)
	}
	if got := after.deferredRefused - before.deferredRefused; got != 1 {
		t.Errorf("the deferred lane moved the deferred counter by %d, want 1", got)
	}
	if got := after.poolBusy - before.poolBusy; got != 0 {
		t.Errorf("a deferred lane was charged to pool_busy (+%d)", got)
	}
	if got := pressure.deferredRefused.Load(); got != 1 {
		t.Errorf("the request itself recorded %d deferred refusals, want 1", got)
	}
}

// The defect this lane exists to fix: a request refused entirely above the
// pool used to leave no trace at all, so an incident made of nothing but
// synthetic refusals produced an observation that read "pressure 0".
func TestWithDBBudgetLogsRefusalsThatNeverReachedThePool(t *testing.T) {
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetFlags(flags) })

	h := withDBBudget(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noteAdmissionRefusal(r.Context())
		noteDeferredRefusal(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/npm/zod", nil))

	line := buf.String()
	if !strings.Contains(line, "csx-server: db pressure ") {
		t.Fatalf("a request refused above the pool wrote no pressure line: %q", line)
	}
	for _, want := range []string{
		"path=/npm/zod", "class=interactive", "admission_refused=1", "deferred_refused=1",
		"cause=admission_refused+deferred_refused",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("pressure line does not carry %q: %q", want, line)
		}
	}
	if strings.Contains(line, "cause=pool_busy") || strings.Contains(line, "cause=query_timeout") {
		t.Errorf("a refusal above the pool was reported as a pool or statement failure: %q", line)
	}
}
