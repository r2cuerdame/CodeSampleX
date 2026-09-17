package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// timeoutOrBusyStore allows injecting transient database errors into any store read.
type timeoutOrBusyStore struct {
	*fakeStore
	failAttempts atomic.Int32
	injectErr    error
}

func (t *timeoutOrBusyStore) PackageVersions(ctx context.Context, eco, name string) ([]string, error) {
	if t.failAttempts.Add(-1) >= 0 {
		return nil, t.injectErr
	}
	return t.fakeStore.PackageVersions(ctx, eco, name)
}

func (t *timeoutOrBusyStore) PackageSymbols(ctx context.Context, eco, name, ver string) ([]string, error) {
	if t.failAttempts.Add(-1) >= 0 {
		return nil, t.injectErr
	}
	return t.fakeStore.PackageSymbols(ctx, eco, name, ver)
}

func (t *timeoutOrBusyStore) FailureClusters(ctx context.Context, eco, name string) ([]string, int, error) {
	if t.failAttempts.Add(-1) >= 0 {
		return nil, 0, t.injectErr
	}
	return t.fakeStore.FailureClusters(ctx, eco, name)
}

func (t *timeoutOrBusyStore) SeederSamples(ctx context.Context, login string) ([]SampleListItem, error) {
	if t.failAttempts.Add(-1) >= 0 {
		return nil, t.injectErr
	}
	return t.fakeStore.SeederSamples(ctx, login)
}

func (t *timeoutOrBusyStore) RecordPackages(ctx context.Context, f RecordFilter, offset, limit int) ([]PackageHit, int, error) {
	if t.failAttempts.Add(-1) >= 0 {
		return nil, 0, t.injectErr
	}
	return t.fakeStore.RecordPackages(ctx, f, offset, limit)
}

func (t *timeoutOrBusyStore) CompletenessGaps(ctx context.Context, q string, offset, limit int) ([]CompletenessGap, int, error) {
	if t.failAttempts.Add(-1) >= 0 {
		return nil, 0, t.injectErr
	}
	return t.fakeStore.CompletenessGaps(ctx, q, offset, limit)
}

func queryTimeoutErr() error {
	return &pgconn.PgError{
		Code:    "57014",
		Message: "canceling statement due to statement timeout",
	}
}

// 1. Proven absent entity returns 404, incrementing ProvenNotFound.
func TestProvenAbsentEntityReturns404(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	mux, _ := newTestMux(t, nil)

	// Non-existent package
	rec := get(t, mux, "/npm/definitely-not-found-pkg-404")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}

	metrics := GetRouteMetrics()
	if metrics.ProvenNotFound == 0 {
		t.Errorf("ProvenNotFound counter was not incremented for proven absent package")
	}
	if metrics.RetryAttempted != 0 || metrics.RetryExhausted != 0 {
		t.Errorf("absent entity should not trigger retries, got attempted=%d exhausted=%d",
			metrics.RetryAttempted, metrics.RetryExhausted)
	}
}

// 2. First read fails over transport, bounded retry succeeds -> 200 OK
// without false 404. (A statement ceiling is NOT retried -- see
// retry_amplification_test.go -- so the recoverable case is a dropped
// connection.)
func TestTransientStoreTimeoutRetriesAndSucceeds(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	base := newFakeStore()
	store := &timeoutOrBusyStore{
		fakeStore: base,
		injectErr: transportErr(),
	}
	store.failAttempts.Store(1) // fail first attempt, succeed on retry

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	rec := get(t, mux, "/npm/axios")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after retry, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	metrics := GetRouteMetrics()
	if metrics.RetryAttempted == 0 {
		t.Errorf("expected RetryAttempted > 0, got %d", metrics.RetryAttempted)
	}
	if metrics.RetryExhausted != 0 {
		t.Errorf("expected RetryExhausted == 0, got %d", metrics.RetryExhausted)
	}
	if metrics.ProvenNotFound != 0 {
		t.Errorf("proven absence metric must not increment on transient timeout, got %d", metrics.ProvenNotFound)
	}
	if metrics.Final503 != 0 || metrics.Final504 != 0 {
		t.Errorf("expected no final 503/504 on recovered retry, got 503=%d 504=%d", metrics.Final503, metrics.Final504)
	}
}

// 3. Repeated transport faults exhaust the retry budget -> 503/504 with
// Retry-After and Cache-Control, never 404.
func TestRepeatedStoreTimeoutReturns503Never404(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	base := newFakeStore()
	store := &timeoutOrBusyStore{
		fakeStore: base,
		injectErr: transportErr(),
	}
	store.failAttempts.Store(10) // repeated faults exceeding maxReadRetries (2)

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	rec := get(t, mux, "/npm/axios")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable, got %d", rec.Code)
	}

	// Verify headers: Retry-After, Cache-Control (no negative cache poisoning)
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Errorf("expected Retry-After header on 503, got empty")
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-cache") || !strings.Contains(cc, "no-store") {
		t.Errorf("expected Cache-Control preventing negative caching, got %q", cc)
	}

	// Canonical URL stability: must preserve canonical entity mapping
	body := rec.Body.String()
	if !strings.Contains(body, `rel="canonical" href="https://codesamplex.dev/npm/axios"`) {
		t.Errorf("canonical URL stability violated on 503; body did not contain stable canonical tag")
	}

	metrics := GetRouteMetrics()
	if metrics.ProvenNotFound != 0 {
		t.Errorf("ProvenNotFound counter must be 0 under timeout, got %d", metrics.ProvenNotFound)
	}
	if metrics.RetryExhausted == 0 {
		t.Errorf("expected RetryExhausted > 0, got %d", metrics.RetryExhausted)
	}
	if metrics.Final503 == 0 {
		t.Errorf("expected Final503 > 0, got %d", metrics.Final503)
	}
}

// 4. Context deadline exceeded returns 504 Gateway Timeout.
func TestContextDeadlineExceededReturns504(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	base := newFakeStore()
	store := &timeoutOrBusyStore{
		fakeStore: base,
		injectErr: context.DeadlineExceeded,
	}
	store.failAttempts.Store(10)

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	// Create request with already cancelled/timed out context
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/npm/axios", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 Gateway Timeout, got %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Errorf("expected Retry-After header on 504")
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-cache") {
		t.Errorf("expected Cache-Control: no-cache on 504, got %q", cc)
	}

	metrics := GetRouteMetrics()
	if metrics.ProvenNotFound != 0 {
		t.Errorf("ProvenNotFound counter must be 0 on 504, got %d", metrics.ProvenNotFound)
	}
	if metrics.Final504 == 0 {
		t.Errorf("expected Final504 > 0, got %d", metrics.Final504)
	}
}

// 5. Database pool busy/exhaustion returns 503 Service Unavailable, never 404.
func TestStorePoolBusyReturns503Never404(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	base := newFakeStore()
	store := &timeoutOrBusyStore{
		fakeStore: base,
		injectErr: serverstore.ErrPoolBusy,
	}
	store.failAttempts.Store(10)

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	rec := get(t, mux, "/npm/axios")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable for pool busy, got %d", rec.Code)
	}

	metrics := GetRouteMetrics()
	if metrics.ProvenNotFound != 0 {
		t.Errorf("ProvenNotFound counter must be 0 on pool busy, got %d", metrics.ProvenNotFound)
	}
	if metrics.PoolBusy == 0 {
		t.Errorf("expected PoolBusy counter > 0, got %d", metrics.PoolBusy)
	}
}

// 6. Healthy store returns 200 OK unchanged.
func TestHealthyStoreReturns200OK(t *testing.T) {
	ResetRouteMetrics()
	mux, _ := newTestMux(t, nil)

	rec := get(t, mux, "/npm/axios")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on healthy store, got %d", rec.Code)
	}

	metrics := GetRouteMetrics()
	if metrics.ProvenNotFound != 0 || metrics.Final503 != 0 || metrics.Final504 != 0 {
		t.Errorf("unexpected error counters on healthy store: %+v", metrics)
	}
}

// 7. Induced timeout across all detail/search routes asserts timeout -> 404 = 0 and ProvenNotFound == 0.
func TestAllDetailRoutesUnderInducedTimeouts(t *testing.T) {
	routes := []struct {
		name string
		path string
	}{
		{"package_page", "/npm/axios"},
		{"version_page", "/npm/axios/1.12.0"},
		{"symbol_page", "/npm/axios/1.12.0/axios.post"},
		{"seeder_page", "/seeders/alice"},
		{"compatibility_page", "/compatibility"},
		{"gaps_page", "/gaps"},
	}

	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			ResetRouteMetrics()
			setFastRetryForTest(true)
			defer setFastRetryForTest(false)

			base := newFakeStore()
			base.seeders["alice"] = []SampleListItem{{SampleID: "sha256:abc"}}
			store := &timeoutOrBusyStore{
				fakeStore: base,
				injectErr: queryTimeoutErr(),
			}
			store.failAttempts.Store(10)

			mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

			rec := get(t, mux, rt.path)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("CRITICAL DEFECT: route %s returned 404 under store timeout!", rt.path)
			}
			if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusGatewayTimeout {
				t.Fatalf("route %s: expected 503 or 504, got %d", rt.path, rec.Code)
			}

			metrics := GetRouteMetrics()
			if metrics.ProvenNotFound != 0 {
				t.Fatalf("route %s: ProvenNotFound was incremented (%d) under store timeout!", rt.path, metrics.ProvenNotFound)
			}
		})
	}
}

// 8. UI resilience: 503/504 page contains Retry button and bounded retry script, 404 does not.
func TestUIErrorPageRetryButtonAndScript(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	base := newFakeStore()
	store := &timeoutOrBusyStore{
		fakeStore: base,
		injectErr: queryTimeoutErr(),
	}
	store.failAttempts.Store(10)

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	// Transient 503 response
	rec503 := get(t, mux, "/npm/axios")
	body503 := rec503.Body.String()
	if !strings.Contains(body503, `id="error-retry-btn"`) {
		t.Errorf("503 response must contain retry button, body was:\n%s", body503)
	}
	if !strings.Contains(body503, "maxAutoRetries = 1") {
		t.Errorf("503 response must contain bounded client retry script")
	}

	// Permanent 404 response
	muxHealthy, _ := newTestMux(t, nil)
	rec404 := get(t, muxHealthy, "/npm/nonexistent-package-xyz")
	body404 := rec404.Body.String()
	if strings.Contains(body404, `id="error-retry-btn"`) {
		t.Errorf("404 response must NOT contain retry button")
	}
	if strings.Contains(body404, "maxAutoRetries") {
		t.Errorf("404 response must NOT contain auto-retry script")
	}
}

// 9. Bounded retry does not multiply when nested.
func TestNoNestedRetryMultiplication(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)
	var calls atomic.Int32

	ctx := context.Background()
	_, _ = executeWithRetry(ctx, "outer", func(c context.Context) (int, error) {
		// Inside outer, invoke inner with child context c
		_, _ = executeWithRetry(c, "inner", func(innerCtx context.Context) (int, error) {
			calls.Add(1)
			return 0, queryTimeoutErr()
		})
		return 0, queryTimeoutErr()
	})

	// Inner execution was called during outer attempts without multiplying retries
	// Outer runs at most 3 attempts (attempt 0, 1, 2)
	totalCalls := calls.Load()
	if totalCalls > 3 {
		t.Fatalf("nested retry multiplied executions: expected <= 3, got %d", totalCalls)
	}
}
