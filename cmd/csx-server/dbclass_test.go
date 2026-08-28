package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

	p.report(serverstore.ClassInteractive, "/wanted", 0, 1, 90*time.Millisecond)
	p.report(serverstore.ClassInteractive, "/records", 3, 0, 3*time.Second)
	if len(lines) != 1 {
		t.Fatalf("the second line in the same second was not throttled: %v", lines)
	}
	// A different class is a different problem and is never throttled away
	// by the noisy one.
	p.report(serverstore.ClassBackground, "/v1/evidence/batches", 1, 0, time.Second)
	if len(lines) != 2 {
		t.Fatalf("a second class was throttled by the first: %v", lines)
	}
	clock = clock.Add(budgetPressureWindow + time.Millisecond)
	p.report(serverstore.ClassInteractive, "/records", 3, 0, 3*time.Second)
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
	p.report(serverstore.ClassInteractive, "/wanted", 1, 0, time.Second)
	if strings.Contains(lines[3], "?") {
		t.Errorf("the log line carries a query string: %q", lines[3])
	}
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
