package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/hostpressure"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// fakePoolStatsSource hands the handler a fixed serverstore.PoolStats.
type fakePoolStatsSource struct {
	stats serverstore.PoolStats
}

func (f fakePoolStatsSource) PoolStats() serverstore.PoolStats { return f.stats }

// fakeFarmIngestSource hands the handler a fixed LastFarmIngestAt answer.
type fakeFarmIngestSource struct {
	at    time.Time
	found bool
	err   error
}

func (f fakeFarmIngestSource) LastFarmIngestAt(context.Context) (time.Time, bool, error) {
	return f.at, f.found, f.err
}

// fakeHostPressureReader hands the handler a fixed hostpressure.Reading (or
// error) without ever touching /proc, so this test runs identically on
// Windows and Linux.
type fakeHostPressureReader struct {
	reading hostpressure.Reading
	err     error
}

func (f fakeHostPressureReader) Sample() (hostpressure.Reading, error) {
	return f.reading, f.err
}

func samplePoolStats() serverstore.PoolStats {
	return serverstore.PoolStats{
		Enabled:  true,
		MaxConns: 12,
		Open:     9,
		InUse:    3,
		Idle:     6,
		Classes: []serverstore.ClassPoolStats{
			{
				Class: serverstore.ClassInteractive.String(), Limit: 6, InUse: 1,
				Waited: 2, Busy: 1, Timeouts: 0, Retries: 3, Suppressed: 4,
			},
			{
				Class: serverstore.ClassFarmIngest.String(), Limit: 2, InUse: 0,
				Waited: 0, Busy: 0, Timeouts: 0, Retries: 0, Suppressed: 0,
			},
		},
	}
}

func TestOpsMetricsHandlerReturnsDocumentedShape(t *testing.T) {
	ingestAt := time.Date(2026, 9, 16, 11, 59, 40, 0, time.UTC)
	sampledAt := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	h := &OpsMetricsHandler{
		Pool:       fakePoolStatsSource{stats: samplePoolStats()},
		FarmIngest: fakeFarmIngestSource{at: ingestAt, found: true},
		Host:       fakeHostPressureReader{reading: hostpressure.Reading{StealPercent: 0.4, LoadAvg1: 1.2, SampledAt: sampledAt}},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v (body=%s)", err, rec.Body.String())
	}

	pool, ok := got["pool"].(map[string]any)
	if !ok {
		t.Fatalf("response missing object field \"pool\": %s", rec.Body.String())
	}
	if pool["enabled"] != true || pool["maxConns"] != float64(12) || pool["open"] != float64(9) ||
		pool["inUse"] != float64(3) || pool["idle"] != float64(6) {
		t.Fatalf("pool = %#v", pool)
	}
	classes, ok := pool["classes"].([]any)
	if !ok || len(classes) != 2 {
		t.Fatalf("pool.classes = %#v, want 2 entries", pool["classes"])
	}
	interactive, ok := classes[0].(map[string]any)
	if !ok {
		t.Fatalf("pool.classes[0] is not an object: %#v", classes[0])
	}
	for _, field := range []string{"class", "limit", "inUse", "waited", "busy", "timeouts", "retries", "suppressed"} {
		if _, present := interactive[field]; !present {
			t.Fatalf("pool.classes[0] missing field %q: %#v", field, interactive)
		}
	}
	if interactive["class"] != serverstore.ClassInteractive.String() {
		t.Fatalf("pool.classes[0].class = %v", interactive["class"])
	}
	if interactive["waited"] != float64(2) || interactive["busy"] != float64(1) ||
		interactive["retries"] != float64(3) || interactive["suppressed"] != float64(4) {
		t.Fatalf("pool.classes[0] counters = %#v", interactive)
	}

	host, ok := got["host"].(map[string]any)
	if !ok {
		t.Fatalf("response missing object field \"host\": %s", rec.Body.String())
	}
	if host["stealPercent"] != 0.4 || host["loadAvg1"] != 1.2 {
		t.Fatalf("host = %#v", host)
	}
	if host["sampledAt"] != "2026-09-16T12:00:00Z" {
		t.Fatalf("host.sampledAt = %v", host["sampledAt"])
	}
	if _, hasErr := host["error"]; hasErr {
		t.Fatalf("host.error present on a successful sample: %#v", host)
	}

	farmIngest, ok := got["farmIngest"].(map[string]any)
	if !ok {
		t.Fatalf("response missing object field \"farmIngest\": %s", rec.Body.String())
	}
	if farmIngest["lastCommitAt"] != "2026-09-16T11:59:40Z" {
		t.Fatalf("farmIngest.lastCommitAt = %v", farmIngest["lastCommitAt"])
	}
	if farmIngest["lastCommitFound"] != true {
		t.Fatalf("farmIngest.lastCommitFound = %v", farmIngest["lastCommitFound"])
	}
}

// TestOpsMetricsHandlerReportsHostSampleErrorRatherThanZeroValues proves the
// handler does not let a failed hostpressure.Sample() masquerade as a real
// "0% steal" reading: Task 6's governor must be able to tell "no signal"
// apart from "measured healthy" from this same endpoint.
func TestOpsMetricsHandlerReportsHostSampleErrorRatherThanZeroValues(t *testing.T) {
	h := &OpsMetricsHandler{
		Pool:       fakePoolStatsSource{stats: samplePoolStats()},
		FarmIngest: fakeFarmIngestSource{found: false},
		Host:       fakeHostPressureReader{err: hostpressure.ErrUnsupportedPlatform},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	host, ok := got["host"].(map[string]any)
	if !ok {
		t.Fatalf("response missing object field \"host\": %s", rec.Body.String())
	}
	errMsg, _ := host["error"].(string)
	if errMsg == "" {
		t.Fatalf("host.error empty on a failed sample: %#v", host)
	}
	if stealPercent, present := host["stealPercent"]; present && stealPercent != float64(0) {
		t.Fatalf("host.stealPercent = %v on a failed sample, want absent or 0", stealPercent)
	}

	farmIngest, ok := got["farmIngest"].(map[string]any)
	if !ok {
		t.Fatalf("response missing object field \"farmIngest\": %s", rec.Body.String())
	}
	if farmIngest["lastCommitFound"] != false {
		t.Fatalf("farmIngest.lastCommitFound = %v, want false when nothing has ever landed", farmIngest["lastCommitFound"])
	}
	if _, present := farmIngest["lastCommitAt"]; present {
		t.Fatalf("farmIngest.lastCommitAt present when lastCommitFound is false: %#v", farmIngest)
	}
}

func TestOpsMetricsHandlerHandlesNilDependencies(t *testing.T) {
	h := &OpsMetricsHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v (body=%s)", err, rec.Body.String())
	}
	if _, ok := got["pool"]; !ok {
		t.Fatalf("response missing \"pool\" even with no pool source configured: %s", rec.Body.String())
	}
	if _, ok := got["host"]; !ok {
		t.Fatalf("response missing \"host\" even with no sampler configured: %s", rec.Body.String())
	}
	if _, ok := got["farmIngest"]; !ok {
		t.Fatalf("response missing \"farmIngest\" even with no farm ingest source configured: %s", rec.Body.String())
	}
}

func TestOpsMetricsHandlerRejectsNonGet(t *testing.T) {
	h := &OpsMetricsHandler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/ops/pool-metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

var errFarmIngestUnavailable = errors.New("farm ingest read failed")

func TestOpsMetricsHandlerFarmIngestErrorLeavesFoundFalse(t *testing.T) {
	h := &OpsMetricsHandler{
		FarmIngest: fakeFarmIngestSource{found: true, err: errFarmIngestUnavailable},
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	farmIngest := got["farmIngest"].(map[string]any)
	if farmIngest["lastCommitFound"] != false {
		t.Fatalf("farmIngest.lastCommitFound = %v on a read error, want false", farmIngest["lastCommitFound"])
	}
}

type fakeRouteOutcomeSource struct{ outcomes RouteOutcomes }

func (f fakeRouteOutcomeSource) RouteOutcomes() RouteOutcomes { return f.outcomes }

// The route-outcome counters are what lets an operator read
// "timeout -> 404 = 0" off production (#445): proven absence is counted
// apart from every transient classification and from the final 503/504.
// A handler without a source says so, rather than reporting zeros that look
// like a measured clean run.
func TestOpsMetricsHandlerReportsRouteOutcomes(t *testing.T) {
	h := &OpsMetricsHandler{
		Pool: fakePoolStatsSource{stats: samplePoolStats()},
		Host: fakeHostPressureReader{reading: hostpressure.Reading{}},
		Routes: fakeRouteOutcomeSource{outcomes: RouteOutcomes{
			ProvenNotFound: 7, DBQueryTimeout: 3, PoolBusy: 5, RetryAttempted: 1,
			RetrySuppressed: 8, RetryExhausted: 0, Final503: 8, Final504: 1,
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	routes, ok := got["routes"].(map[string]any)
	if !ok {
		t.Fatalf("response missing object field \"routes\": %s", rec.Body.String())
	}
	want := map[string]float64{
		"provenNotFound": 7, "dbQueryTimeout": 3, "poolBusy": 5, "retryAttempted": 1,
		"retrySuppressed": 8, "retryExhausted": 0, "final503": 8, "final504": 1,
	}
	for field, v := range want {
		if routes[field] != v {
			t.Errorf("routes.%s = %v, want %v", field, routes[field], v)
		}
	}
	if routes["measured"] != true {
		t.Errorf("routes.measured = %v, want true", routes["measured"])
	}

	h.Routes = nil
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	routes, _ = got["routes"].(map[string]any)
	if routes == nil || routes["measured"] != false {
		t.Fatalf("routes without a source = %#v, want measured=false", routes)
	}
}
