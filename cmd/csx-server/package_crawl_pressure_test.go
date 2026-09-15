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
	if name != "slow-one" && name != "slow-two" {
		return s.Store.ListPackageVersions(ctx, ecosystem, name)
	}
	select {
	case s.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-s.unblock:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.Store.ListPackageVersions(ctx, ecosystem, name)
}

func TestPackageCrawlPressureDoesNotThrottleCriticalEndpoints(t *testing.T) {
	underlying := serverstore.NewFake()
	now := time.Now()
	axios := serverstore.PackageRow{
		PURL: "pkg:npm/axios@1.0.0", Ecosystem: "npm", Name: "axios", Version: "1.0.0",
		Major: "1", Publicness: "PUBLIC", FirstSeen: now, LastSeen: now,
	}
	if err := underlying.UpsertPackage(t.Context(), axios); err != nil {
		t.Fatal(err)
	}
	if err := underlying.PutSnapshot(t.Context(), axios.PURL, "", `{"rows":[]}`); err != nil {
		t.Fatal(err)
	}
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

	// Prime a real package through the production adapter. Every package-level
	// detail used by the next navigation is now cached.
	prime, err := http.Get(srv.URL + "/npm/axios")
	if err != nil {
		t.Fatal(err)
	}
	prime.Body.Close()
	if prime.StatusCode != http.StatusOK {
		t.Fatalf("prime package status = %d, want 200", prime.StatusCode)
	}

	// Two unrelated cold pages are allowed to reach their underlying loads.
	for _, name := range []string{"slow-one", "slow-two"} {
		go func() {
			resp, _ := http.Get(srv.URL + "/npm/" + name)
			if resp != nil {
				resp.Body.Close()
			}
		}()
	}
	for range 2 {
		select {
		case <-store.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for two cold package loads")
		}
	}

	// Cached package navigation must not be rejected or queued behind them.
	started := time.Now()
	warm, err := http.Get(srv.URL + "/npm/axios")
	if err != nil {
		t.Fatal(err)
	}
	warm.Body.Close()
	if warm.StatusCode != http.StatusOK {
		t.Errorf("warm package status = %d, want 200", warm.StatusCode)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Errorf("warm package waited %v behind unrelated cold loads", elapsed)
	}

	// Critical endpoints remain independent of package-detail pressure.
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
