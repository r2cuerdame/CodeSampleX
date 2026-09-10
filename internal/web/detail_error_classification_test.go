package web

import (
	"errors"
	"net/http"
	"testing"
)

// TestSampleRouteDistinguishesAbsenceFromStoreError proves that:
// - A truly absent sample returns canonical HTTP 404 Not Found.
// - An infrastructure/DB/query failure on SampleMeta returns HTTP 503 + Retry-After.
// - A failure on SampleReceipts returns HTTP 503 + Retry-After (never misleading 200 with dropped evidence).
// - A healthy sample returns HTTP 200 with complete evidence.
func TestSampleRouteDistinguishesAbsenceFromStoreError(t *testing.T) {
	mux, f := newTestMux(t, nil)

	// 1. Healthy sample -> 200 OK
	rec := get(t, mux, "/samples/sha256:d1e2f3")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy sample status = %d, want 200", rec.Code)
	}
	mustContain(t, rec.Body.String(), "POST JSON with axios and retries")

	// 2. Truly absent sample -> 404 Not Found
	rec = get(t, mux, "/samples/sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing sample status = %d, want 404", rec.Code)
	}

	// 3. Injected store error on SampleMeta -> 503 Service Unavailable with Retry-After (NOT 404)
	f.sampleMetaErr = errors.New("read timeout: pool busy")
	rec = get(t, mux, "/samples/sha256:d1e2f3")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("SampleMeta error status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("SampleMeta error Retry-After = %q, want 2", got)
	}

	// Also verify that an absent sample under store failure returns 503, never 404
	rec = get(t, mux, "/samples/sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("SampleMeta error on absent sample status = %d, want 503", rec.Code)
	}
	f.sampleMetaErr = nil

	// 4. Injected store error on SampleReceipts -> 503 with Retry-After (does not drop receipts to render 200 unverified)
	f.sampleReceiptsErr = errors.New("receipt query timeout")
	rec = get(t, mux, "/samples/sha256:d1e2f3")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("SampleReceipts error status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("SampleReceipts error Retry-After = %q, want 2", got)
	}
	f.sampleReceiptsErr = nil
}

// TestSemanticSampleRouteDistinguishesAbsenceFromStoreError proves that the
// human-readable canonical URL /{eco}/{name}/{version}/samples/{slug} distinguishes
// missing slug (404) from store failure (503 + Retry-After).
func TestSemanticSampleRouteDistinguishesAbsenceFromStoreError(t *testing.T) {
	mux, f := newTestMux(t, nil)
	semanticURL := f.withRelease(f.sampleList[0]).Href()

	// 1. Healthy semantic sample -> 200 OK
	rec := get(t, mux, semanticURL)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy semantic sample status = %d, want 200", rec.Code)
	}

	// 2. Truly missing slug under valid release -> 404 Not Found
	rec = get(t, mux, "/npm/axios/1.12.0/samples/sha256-000000-missing")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing slug status = %d, want 404", rec.Code)
	}

	// 3. Injected store error on SampleMeta -> 503 with Retry-After
	f.sampleMetaErr = errors.New("connection reset by peer")
	rec = get(t, mux, semanticURL)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("SampleMeta error status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("SampleMeta error Retry-After = %q, want 2", got)
	}
	f.sampleMetaErr = nil

	// 4. Injected store error on SampleReceipts -> 503 with Retry-After
	f.sampleReceiptsErr = errors.New("database locked")
	rec = get(t, mux, semanticURL)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("SampleReceipts error status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("SampleReceipts error Retry-After = %q, want 2", got)
	}
	f.sampleReceiptsErr = nil
}

// TestVersionRouteDistinguishesAbsenceFromStoreError proves that:
// - An unknown release returns 404 Not Found when store is healthy.
// - An infrastructure/DB timeout on PackageSamples during versionPage rendering
//   returns 503 Service Unavailable + Retry-After, NEVER a false 404.
func TestVersionRouteDistinguishesAbsenceFromStoreError(t *testing.T) {
	mux, f := newTestMux(t, nil)

	// 1. Truly absent version -> 404 Not Found
	rec := get(t, mux, "/npm/axios/99.99.99")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("absent version status = %d, want 404", rec.Code)
	}

	// 2. Injected store error on PackageSamples -> 503 Service Unavailable (NOT 404)
	f.packageSamplesErr = errors.New("read timeout: pool saturated")
	rec = get(t, mux, "/npm/axios/99.99.99")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PackageSamples error on version status = %d, want 503 (not false 404)", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}

	// 3. Known version with symbols also propagates store failure cleanly
	rec = get(t, mux, "/npm/axios/1.12.0")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PackageSamples error on known version status = %d, want 503", rec.Code)
	}
	f.packageSamplesErr = nil

	// 4. Healthy known version returns 200 OK
	rec = get(t, mux, "/npm/axios/1.12.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy version status = %d, want 200", rec.Code)
	}
}

// TestSymbolRouteDistinguishesAbsenceFromStoreError proves that:
// - An unknown symbol on an existing version returns 404 Not Found.
// - A store error on PackageSamples returns 503 Service Unavailable, NEVER 404.
func TestSymbolRouteDistinguishesAbsenceFromStoreError(t *testing.T) {
	mux, f := newTestMux(t, nil)

	// 1. Unknown symbol without snapshot or samples -> 404 Not Found
	rec := get(t, mux, "/npm/axios/1.12.0/nonexistentSymbol")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown symbol status = %d, want 404", rec.Code)
	}

	// 2. Injected store error on PackageSamples -> 503 Service Unavailable (NOT 404)
	f.packageSamplesErr = errors.New("context deadline exceeded: query timeout")
	rec = get(t, mux, "/npm/axios/1.12.0/nonexistentSymbol")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PackageSamples error on symbol status = %d, want 503 (not false 404)", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	f.packageSamplesErr = nil
}

// TestPackageRouteDistinguishesAbsenceFromStoreError proves that:
// - A truly absent package returns 404 Not Found.
// - If PackageSamples fails with an error for a package with no versions,
//   it returns 503 Service Unavailable, NEVER a false 404.
func TestPackageRouteDistinguishesAbsenceFromStoreError(t *testing.T) {
	mux, f := newTestMux(t, nil)

	// 1. Truly absent package -> 404 Not Found
	rec := get(t, mux, "/npm/completely-absent-package")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("absent package status = %d, want 404", rec.Code)
	}

	// 2. When PackageSamples errors on empty package, return 503 (NOT 404)
	f.packageSamplesErr = errors.New("read timeout: pool saturated")
	rec = get(t, mux, "/npm/completely-absent-package")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PackageSamples error status = %d, want 503 (not false 404)", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	f.packageSamplesErr = nil
}
