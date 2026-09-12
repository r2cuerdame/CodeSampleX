package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type crawlPressureStore struct {
	serverstore.Store
	entered chan struct{}
	unblock chan struct{}
}

func (s *crawlPressureStore) ListPackageVersions(ctx context.Context, ecosystem, name string) ([]serverstore.PackageRow, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.unblock:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.Store.ListPackageVersions(ctx, ecosystem, name)
}

func TestPackageCrawlPressureDoesNotThrottleCriticalEndpoints(t *testing.T) {
	t.Setenv("CSX_PACKAGE_PAGE_CONCURRENCY", "1")

	underlying := serverstore.NewFake()
	store := &crawlPressureStore{
		Store:   underlying,
		entered: make(chan struct{}, 10),
		unblock: make(chan struct{}),
	}
	cfg := serverstore.ServerConfig{
		PublicCheck: "trust",
		BlobDir:     t.TempDir(),
	}
	mux := BuildMux(cfg, store)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. Start a package page request to /npm/axios that saturates the gate.
	go func() {
		resp, _ := http.Get(srv.URL + "/npm/axios")
		if resp != nil {
			resp.Body.Close()
		}
	}()

	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for package page to saturate gate")
	}

	// 2. Package page overflow returns 503 + Retry-After.
	overflowResp, err := http.Get(srv.URL + "/npm/axios")
	if err != nil {
		t.Fatal(err)
	}
	if overflowResp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("overflow package page status = %d, want 503", overflowResp.StatusCode)
	}
	if got := overflowResp.Header.Get("Retry-After"); got == "" {
		t.Errorf("overflow package page missing Retry-After header")
	}
	overflowResp.Body.Close()

	// 3. Critical endpoints are NOT throttled:
	// /healthz must return 200 OK.
	healthResp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if healthResp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", healthResp.StatusCode)
	}
	healthResp.Body.Close()

	// /compatibility must return 200 OK.
	compatResp, err := http.Get(srv.URL + "/compatibility")
	if err != nil {
		t.Fatal(err)
	}
	if compatResp.StatusCode != http.StatusOK {
		t.Errorf("compatibility status = %d, want 200", compatResp.StatusCode)
	}
	compatResp.Body.Close()

	// /v1/authoring/work/next must not be throttled by package gate (401 or 400 or whatever authoring returns without token, never 503 from package gate).
	authResp, err := http.Get(srv.URL + "/v1/authoring/work/next")
	if err != nil {
		t.Fatal(err)
	}
	if authResp.StatusCode == http.StatusServiceUnavailable && authResp.Header.Get("Retry-After") == "2" {
		t.Errorf("authoring was throttled by package page gate!")
	}
	authResp.Body.Close()

	// Drain.
	close(store.unblock)
}
