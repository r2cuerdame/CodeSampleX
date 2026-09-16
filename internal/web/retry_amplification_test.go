package web

// Retry must not amplify load (#445, v0.1.195). Production served every
// refused package read three times: the pool said "busy", the cache-miss
// admission gate said "busy", the statement ceiling said "cancelled", and the
// read wrapper asked again twice each time, with a dozen reads per page. The
// interactive lanes never drained, the builder queued behind them never
// finished, and the site stayed at 503 for hours on a 2-vCPU host.
//
// These tests pin the contract the fix restores: a saturation signal is
// final for the request that received it, a statement ceiling is final, only
// a transport fault earns another attempt, and a whole page -- however many
// reads it makes -- spends at most maxReadRetries extra attempts.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// countingFailStore fails every read it intercepts with injectErr until
// failAttempts is exhausted, and counts every attempt it saw. attempts is the
// number the retry policy is judged by.
type countingFailStore struct {
	*fakeStore
	failAttempts atomic.Int32
	attempts     atomic.Int32
	injectErr    error
}

func (c *countingFailStore) fail() bool {
	c.attempts.Add(1)
	return c.failAttempts.Add(-1) >= 0
}

func (c *countingFailStore) PackageVersions(ctx context.Context, eco, name string) ([]string, error) {
	if c.fail() {
		return nil, c.injectErr
	}
	return c.fakeStore.PackageVersions(ctx, eco, name)
}

func (c *countingFailStore) PackageSymbols(ctx context.Context, eco, name, ver string) ([]string, error) {
	if c.fail() {
		return nil, c.injectErr
	}
	return c.fakeStore.PackageSymbols(ctx, eco, name, ver)
}

func (c *countingFailStore) FailureClusters(ctx context.Context, eco, name string) ([]string, int, error) {
	if c.fail() {
		return nil, 0, c.injectErr
	}
	return c.fakeStore.FailureClusters(ctx, eco, name)
}

func (c *countingFailStore) PackageSamples(ctx context.Context, eco, name string, limit int) ([]SampleListItem, error) {
	if c.fail() {
		return nil, c.injectErr
	}
	return c.fakeStore.PackageSamples(ctx, eco, name, limit)
}

func (c *countingFailStore) WantedForPackage(ctx context.Context, eco, name string) ([]WantedRow, error) {
	if c.fail() {
		return nil, c.injectErr
	}
	return c.fakeStore.WantedForPackage(ctx, eco, name)
}

func (c *countingFailStore) Dependencies(ctx context.Context, eco, name string) ([]DependencyEdge, error) {
	if c.fail() {
		return nil, c.injectErr
	}
	return c.fakeStore.Dependencies(ctx, eco, name)
}

func admissionRefusalErr() error {
	return fmt.Errorf("%w (package cache-miss admission)", serverstore.ErrPoolBusy)
}

func followUpSuppressedErr() error {
	return fmt.Errorf("%w (class interactive, follow-up suppressed after earlier backpressure)", serverstore.ErrPoolBusy)
}

func transportErr() error {
	return fmt.Errorf("serverstore: read: %w", io.ErrUnexpectedEOF)
}

// Every way this server says "not now" about its own saturation, and the
// statement ceiling PostgreSQL enforces on its behalf, is attempted exactly
// once per read and rendered 503 -- never 404, never a second attempt.
func TestSaturationSignalsAreNotRetried(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"pool busy", serverstore.ErrPoolBusy},
		{"admission refused", admissionRefusalErr()},
		{"follow-up suppressed", followUpSuppressedErr()},
		{"statement ceiling", queryTimeoutErr()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetRouteMetrics()
			setFastRetryForTest(true)
			defer setFastRetryForTest(false)

			store := &countingFailStore{fakeStore: newFakeStore(), injectErr: tc.err}
			store.failAttempts.Store(1000)
			mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

			started := time.Now()
			rec := get(t, mux, "/npm/axios")
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("want 503, got %d", rec.Code)
			}
			if got := store.attempts.Load(); got != 1 {
				t.Errorf("a saturated read was attempted %d times, want exactly 1", got)
			}
			m := GetRouteMetrics()
			if m.RetryAttempted != 0 {
				t.Errorf("RetryAttempted = %d, want 0: a saturation signal must not be retried", m.RetryAttempted)
			}
			if m.RetrySuppressed == 0 {
				t.Errorf("RetrySuppressed = 0, want > 0: the suppressed retry must stay visible")
			}
			if m.ProvenNotFound != 0 {
				t.Errorf("ProvenNotFound = %d, want 0", m.ProvenNotFound)
			}
			if rec.Header().Get("Retry-After") == "" {
				t.Errorf("503 without Retry-After")
			}
			// The page must not have paid a backoff either: no retry, no wait.
			if took := time.Since(started); took > 500*time.Millisecond {
				t.Errorf("a refused page took %s; a refusal is supposed to be fast", took)
			}
		})
	}
}

// A transport fault -- the connection dropped mid-read -- is the one class
// worth a second attempt: it did no work, and the second attempt usually
// succeeds. Retry, and serve the page.
func TestTransportFaultRetriesOnceAndSucceeds(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	store := &countingFailStore{fakeStore: newFakeStore(), injectErr: transportErr()}
	store.failAttempts.Store(1)
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	rec := get(t, mux, "/npm/axios")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 after one transport retry, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	m := GetRouteMetrics()
	if m.RetryAttempted != 1 {
		t.Errorf("RetryAttempted = %d, want 1", m.RetryAttempted)
	}
	if m.RetryExhausted != 0 || m.Final503 != 0 || m.ProvenNotFound != 0 {
		t.Errorf("unexpected counters after a recovered transport retry: %+v", m)
	}
}

// The retry budget belongs to the request, not to each read. A package page
// makes many store reads; if every one of them may fail over transport and
// every one of them retried twice, one page would cost a dozen extra
// attempts. Across the whole page the extra attempts are bounded by
// maxReadRetries.
func TestRetryBudgetIsPerRequestNotPerRead(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	store := &countingFailStore{fakeStore: newFakeStore(), injectErr: transportErr()}
	store.failAttempts.Store(1000)
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	rec := get(t, mux, "/npm/axios")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 once the budget is spent, got %d", rec.Code)
	}
	m := GetRouteMetrics()
	if m.RetryAttempted > maxReadRetries {
		t.Errorf("RetryAttempted = %d, want <= %d for the whole request", m.RetryAttempted, maxReadRetries)
	}
	if m.RetryExhausted == 0 {
		t.Errorf("RetryExhausted = 0, want > 0 once the request budget is spent")
	}
	// Every read the handler made failed; the handler stops at its first
	// unrecoverable one. attempts = reads made + retries spent.
	if got := store.attempts.Load(); got > 1+maxReadRetries {
		t.Errorf("the page made %d attempts, want <= %d (first read + %d retries)", got, 1+maxReadRetries, maxReadRetries)
	}
}

// The wrapper alone, without a request: the allowance in the context is
// shared by sibling reads, and a read without one gets a private budget
// that still stops.
func TestExecuteWithRetryDrawsOnTheRequestAllowance(t *testing.T) {
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	ctx := withReadRetryAllowance(context.Background())
	var calls atomic.Int32
	failing := func(c context.Context) (int, error) {
		calls.Add(1)
		return 0, transportErr()
	}
	for i := 0; i < 5; i++ {
		_, err := executeWithRetry(ctx, "read", failing)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("read %d: err = %v", i, err)
		}
	}
	// 5 first attempts plus at most maxReadRetries retries shared by all.
	if got := calls.Load(); got != 5+maxReadRetries {
		t.Errorf("five sibling reads made %d attempts, want %d", got, 5+maxReadRetries)
	}

	calls.Store(0)
	_, _ = executeWithRetry(context.Background(), "read", failing)
	if got := calls.Load(); got != 1+maxReadRetries {
		t.Errorf("a read without a request allowance made %d attempts, want %d", got, 1+maxReadRetries)
	}
}

// A saturation signal does not draw on the allowance at all, so a later
// transport fault in the same request can still be retried.
func TestSuppressedRetryDoesNotSpendTheAllowance(t *testing.T) {
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	ctx := withReadRetryAllowance(context.Background())
	for i := 0; i < 10; i++ {
		_, err := executeWithRetry(ctx, "busy", func(c context.Context) (int, error) {
			return 0, serverstore.ErrPoolBusy
		})
		if !errors.Is(err, serverstore.ErrPoolBusy) {
			t.Fatalf("err = %v", err)
		}
	}
	if got := readRetryAllowanceOf(ctx).remaining.Load(); got != maxReadRetries {
		t.Errorf("ten refused reads spent the allowance down to %d, want %d untouched", got, maxReadRetries)
	}
}

// The client half of the same bound: the error page reloads itself at most
// once, and not before the server's Retry-After has passed by a margin.
func TestErrorPageAutoRetryIsSingleAndDelayed(t *testing.T) {
	ResetRouteMetrics()
	setFastRetryForTest(true)
	defer setFastRetryForTest(false)

	store := &countingFailStore{fakeStore: newFakeStore(), injectErr: serverstore.ErrPoolBusy}
	store.failAttempts.Store(1000)
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })

	body := get(t, mux, "/npm/axios").Body.String()
	for _, want := range []string{"maxAutoRetries = 1", "baseDelayMs = 6000", `id="error-retry-btn"`} {
		if !strings.Contains(body, want) {
			t.Errorf("503 page lacks %q", want)
		}
	}
	if strings.Contains(body, "maxAutoRetries = 2") || strings.Contains(body, "}, 2000);") {
		t.Errorf("503 page still carries the two-reloads-two-seconds-apart script")
	}
}
