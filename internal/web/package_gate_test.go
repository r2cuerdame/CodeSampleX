package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gateTrackingStore wraps fakeStore, tracking all store queries and allowing
// deterministic blocking of PackageVersions calls.
type gateTrackingStore struct {
	*fakeStore
	totalCalls atomic.Int64

	mu      sync.Mutex
	hold    bool
	entered chan struct{}
	unblock chan struct{}
}

func newGateTrackingStore() *gateTrackingStore {
	return &gateTrackingStore{
		fakeStore: newFakeStore(),
		entered:   make(chan struct{}, 16),
		unblock:   make(chan struct{}),
	}
}

func (g *gateTrackingStore) recordCall() {
	g.totalCalls.Add(1)
}

func (g *gateTrackingStore) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	g.recordCall()
	g.mu.Lock()
	hold := g.hold
	entered := g.entered
	unblock := g.unblock
	g.mu.Unlock()

	if hold && entered != nil {
		entered <- struct{}{}
		if unblock != nil {
			select {
			case <-unblock:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return g.fakeStore.PackageVersions(ctx, ecosystem, name)
}

func (g *gateTrackingStore) PackageSamples(ctx context.Context, ecosystem, name string, limit int) ([]SampleListItem, error) {
	g.recordCall()
	return g.fakeStore.PackageSamples(ctx, ecosystem, name, limit)
}

func (g *gateTrackingStore) PackageCodeCounts(ctx context.Context, ecosystem, name string) ([]PackageCodeCount, error) {
	g.recordCall()
	return g.fakeStore.PackageCodeCounts(ctx, ecosystem, name)
}

func (g *gateTrackingStore) WantedForPackage(ctx context.Context, ecosystem, name string) ([]WantedRow, error) {
	g.recordCall()
	return g.fakeStore.WantedForPackage(ctx, ecosystem, name)
}

func (g *gateTrackingStore) SnapshotJSON(ctx context.Context, purl, symbol string) (string, bool) {
	g.recordCall()
	return g.fakeStore.SnapshotJSON(ctx, purl, symbol)
}

func (g *gateTrackingStore) FailureClusters(ctx context.Context, ecosystem, name string) ([]string, int, error) {
	g.recordCall()
	return g.fakeStore.FailureClusters(ctx, ecosystem, name)
}

func (g *gateTrackingStore) Dependencies(ctx context.Context, ecosystem, name string) ([]DependencyEdge, error) {
	g.recordCall()
	return g.fakeStore.Dependencies(ctx, ecosystem, name)
}

// TestPackagePageGateEnforcesConcurrencyAndFailsFastWithoutStore proves:
// 1. Only the configured number of requests (here 2) enter expensive DB/cube work.
// 2. Overflow requests fail fast with HTTP 503 and Retry-After.
// 3. Overflow requests do not touch the store at all.
// 4. Once in-flight requests complete, freed slots permit subsequent requests.
func TestPackagePageGateEnforcesConcurrencyAndFailsFastWithoutStore(t *testing.T) {
	store := newGateTrackingStore()
	store.hold = true

	mux, _ := newTestMux(t, func(d *Deps) {
		d.Store = store
		d.PackagePageConcurrency = 2
	})

	type result struct {
		rec  *httptest.ResponseRecorder
		done chan struct{}
	}

	execAsync := func(path string) *result {
		res := &result{
			rec:  httptest.NewRecorder(),
			done: make(chan struct{}),
		}
		go func() {
			defer close(res.done)
			req := httptest.NewRequest(http.MethodGet, path, nil)
			mux.ServeHTTP(res.rec, req)
		}()
		return res
	}

	// 1. Start Request 1: enters PackageVersions and blocks.
	r1 := execAsync("/npm/axios")
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request 1 to enter store")
	}

	// 2. Start Request 2: enters PackageVersions and blocks.
	r2 := execAsync("/golang/github.com/a/b")
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request 2 to enter store")
	}

	// Both configured slots are now occupied. Record the store call count.
	callsBeforeOverflow := store.totalCalls.Load()
	if callsBeforeOverflow != 2 {
		t.Fatalf("store calls before overflow = %d, want 2", callsBeforeOverflow)
	}

	// 3. Send Request 3: must overflow, fail fast, and never touch the store.
	overflowRec := get(t, mux, "/npm/axios")
	if overflowRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("overflow status = %d, want %d (Service Unavailable)",
			overflowRec.Code, http.StatusServiceUnavailable)
	}
	if got := overflowRec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("overflow Retry-After = %q, want %q", got, "2")
	}

	// Assert store call count did not increase for the overflow request.
	callsAfterOverflow := store.totalCalls.Load()
	if callsAfterOverflow != callsBeforeOverflow {
		t.Fatalf("store calls after overflow = %d, want %d (overflow request touched store!)",
			callsAfterOverflow, callsBeforeOverflow)
	}

	// 4. Release held in-flight requests.
	close(store.unblock)

	select {
	case <-r1.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request 1 completion")
	}
	select {
	case <-r2.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request 2 completion")
	}

	if r1.rec.Code != http.StatusOK {
		t.Errorf("request 1 code = %d, want 200", r1.rec.Code)
	}
	if r2.rec.Code != http.StatusOK {
		t.Errorf("request 2 code = %d, want 200", r2.rec.Code)
	}

	// 5. With slots freed, a subsequent package request must succeed.
	store.mu.Lock()
	store.hold = false
	store.mu.Unlock()

	postRec := get(t, mux, "/npm/axios")
	if postRec.Code != http.StatusOK {
		t.Fatalf("post-drain request code = %d, want 200", postRec.Code)
	}
}

// TestPackagePageGatePreservesCanonicalAndSEORedirects proves that while the
// package gate is fully saturated, canonical redirects (trailing slash, canonical Go
// version) and 404s are evaluated and served before the gate without returning 503.
func TestPackagePageGatePreservesCanonicalAndSEORedirects(t *testing.T) {
	store := newGateTrackingStore()
	store.hold = true

	mux, _ := newTestMux(t, func(d *Deps) {
		d.Store = store
		d.PackagePageConcurrency = 1
	})

	// Saturate the gate with 1 in-flight request.
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/npm/axios", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}()

	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for gate saturation")
	}

	callsDuringSaturation := store.totalCalls.Load()

	// 1. Trailing slash redirect must return 301 to slashless path, not 503.
	slashRec := get(t, mux, "/npm/axios/")
	if slashRec.Code != http.StatusMovedPermanently {
		t.Errorf("trailing slash redirect status = %d, want 301", slashRec.Code)
	}
	if loc := slashRec.Header().Get("Location"); loc != "/npm/axios" {
		t.Errorf("trailing slash redirect Location = %q, want /npm/axios", loc)
	}

	// 2. Canonical Go version redirect (1.2.0 -> v1.2.0) must return 301, not 503.
	goRec := get(t, mux, "/golang/github.com/a/b/1.2.0")
	if goRec.Code != http.StatusMovedPermanently {
		t.Errorf("canonical Go version redirect status = %d, want 301", goRec.Code)
	}
	if loc := goRec.Header().Get("Location"); loc != "/golang/github.com/a/b/v1.2.0" {
		t.Errorf("canonical Go version redirect Location = %q, want /golang/github.com/a/b/v1.2.0", loc)
	}

	// 3. Unknown ecosystem must 404, not 503.
	unknownEcoRec := get(t, mux, "/unknown_ecosystem/somepkg")
	if unknownEcoRec.Code != http.StatusNotFound {
		t.Errorf("unknown ecosystem status = %d, want 404", unknownEcoRec.Code)
	}

	// None of the canonical/404 redirects should have incremented the store call count.
	if store.totalCalls.Load() != callsDuringSaturation {
		t.Errorf("redirects unexpectedly touched store; calls = %d, want %d",
			store.totalCalls.Load(), callsDuringSaturation)
	}

	// Drain.
	close(store.unblock)
	<-done
}

// TestPackagePageGateDoesNotThrottleNonPackagePages proves that non-package routes
// (/compatibility, /findings, /gaps, /dependencies, /features, /static, etc.)
// are completely unaffected and not throttled even when the package gate is full.
func TestPackagePageGateDoesNotThrottleNonPackagePages(t *testing.T) {
	store := newGateTrackingStore()
	store.hold = true

	mux, _ := newTestMux(t, func(d *Deps) {
		d.Store = store
		d.PackagePageConcurrency = 1
	})

	// Saturate the package gate.
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/npm/axios", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}()

	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for gate saturation")
	}

	nonPackageRoutes := []string{
		"/compatibility",
		"/findings",
		"/gaps",
		"/dependencies",
		"/features",
		"/robots.txt",
		"/static/site.css",
	}

	for _, path := range nonPackageRoutes {
		rec := get(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Errorf("non-package route %s status = %d, want 200 (was throttled by package gate!)",
				path, rec.Code)
		}
	}

	// Drain.
	close(store.unblock)
	<-done
}

// TestPackagePageGateConfigSeam verifies that PackagePageConcurrency configures
// the exact concurrency threshold.
func TestPackagePageGateConfigSeam(t *testing.T) {
	t.Run("concurrency 1", func(t *testing.T) {
		store := newGateTrackingStore()
		store.hold = true
		mux, _ := newTestMux(t, func(d *Deps) {
			d.Store = store
			d.PackagePageConcurrency = 1
		})

		done := make(chan struct{})
		go func() {
			defer close(done)
			req := httptest.NewRequest(http.MethodGet, "/npm/axios", nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
		}()
		select {
		case <-store.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for request 1")
		}

		// 2nd request overflows immediately.
		rec := get(t, mux, "/golang/github.com/a/b")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("code = %d, want 503", rec.Code)
		}

		close(store.unblock)
		<-done
	})

	t.Run("concurrency 3", func(t *testing.T) {
		store := newGateTrackingStore()
		store.hold = true
		mux, _ := newTestMux(t, func(d *Deps) {
			d.Store = store
			d.PackagePageConcurrency = 3
		})

		var dones []chan struct{}
		for i := 0; i < 3; i++ {
			d := make(chan struct{})
			dones = append(dones, d)
			go func() {
				defer close(d)
				req := httptest.NewRequest(http.MethodGet, "/npm/axios", nil)
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
			}()
			select {
			case <-store.entered:
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out waiting for request %d", i+1)
			}
		}

		// 4th request overflows.
		rec := get(t, mux, "/npm/axios")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("code = %d, want 503", rec.Code)
		}

		close(store.unblock)
		for _, d := range dones {
			<-d
		}
	})

	t.Run("default constant fallback", func(t *testing.T) {
		if DefaultPackagePageConcurrency != 2 {
			t.Fatalf("DefaultPackagePageConcurrency = %d, want 2", DefaultPackagePageConcurrency)
		}

		store := newGateTrackingStore()
		store.hold = true
		// PackagePageConcurrency left at 0 -> defaults to DefaultPackagePageConcurrency (2).
		mux, _ := newTestMux(t, func(d *Deps) {
			d.Store = store
		})

		d1 := make(chan struct{})
		go func() {
			defer close(d1)
			req := httptest.NewRequest(http.MethodGet, "/npm/axios", nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
		}()
		select {
		case <-store.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for request 1")
		}

		d2 := make(chan struct{})
		go func() {
			defer close(d2)
			req := httptest.NewRequest(http.MethodGet, "/golang/github.com/a/b", nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
		}()
		select {
		case <-store.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for request 2")
		}

		// 3rd overflows because default is 2.
		rec := get(t, mux, "/npm/axios")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("code = %d, want 503", rec.Code)
		}

		close(store.unblock)
		<-d1
		<-d2
	})

	t.Run("disabled gate with negative value", func(t *testing.T) {
		store := newGateTrackingStore()
		mux, _ := newTestMux(t, func(d *Deps) {
			d.Store = store
			d.PackagePageConcurrency = -1
		})

		// Multiple requests can run without gating.
		for i := 0; i < 4; i++ {
			rec := get(t, mux, "/npm/axios")
			if rec.Code != http.StatusOK {
				t.Fatalf("request %d failed: code = %d", i, rec.Code)
			}
		}
	})
}

// TestPackagePageGateEnvVarSeam verifies CSX_PACKAGE_PAGE_CONCURRENCY environment variable.
func TestPackagePageGateEnvVarSeam(t *testing.T) {
	t.Run("env sets 1", func(t *testing.T) {
		t.Setenv("CSX_PACKAGE_PAGE_CONCURRENCY", "1")
		if got := packagePageGateLimit(0); got != 1 {
			t.Fatalf("packagePageGateLimit(0) = %d, want 1", got)
		}
	})

	t.Run("env sets off", func(t *testing.T) {
		t.Setenv("CSX_PACKAGE_PAGE_CONCURRENCY", "off")
		if got := packagePageGateLimit(0); got != 0 {
			t.Fatalf("packagePageGateLimit(0) = %d, want 0", got)
		}
	})

	t.Run("configured overrides env", func(t *testing.T) {
		t.Setenv("CSX_PACKAGE_PAGE_CONCURRENCY", "1")
		if got := packagePageGateLimit(5); got != 5 {
			t.Fatalf("packagePageGateLimit(5) = %d, want 5", got)
		}
	})
}
