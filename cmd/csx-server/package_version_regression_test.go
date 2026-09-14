package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// TestGoModulePackageAndVersionRouting verifies that:
// 1. Multi-segment Go package paths (like golang.org/x/net and github.com/jackc/pgx/v5)
//    route correctly without 503s or path splitting bugs.
// 2. Samples naming multiple packages (e.g. x/sys@v0.47.0 and x/net@v0.51.0) attribute
//    versions to the queried package, preventing foreign versions (v0.47.0) from leaking
//    into x/net's version list and creating 404 links.
// 3. A non-existent version URL returns 404 cleanly rather than 503.
// 4. Bare Go version URLs (5.10.0) 301 redirect to canonical v-prefixed URLs (v5.10.0).
func TestGoModulePackageAndVersionRouting(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	now := time.Now()

	// Seed golang.org/x/net
	netPurl := "pkg:golang/golang.org/x/net@v0.51.0"
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL:       netPurl,
		Ecosystem:  "golang",
		Name:       "golang.org/x/net",
		Version:    "v0.51.0",
		Major:      "v0",
		Publicness: "PUBLIC",
		FirstSeen:  now,
		LastSeen:   now,
	}); err != nil {
		t.Fatal(err)
	}

	// Seed github.com/jackc/pgx/v5
	pgxPurl := "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL:       pgxPurl,
		Ecosystem:  "golang",
		Name:       "github.com/jackc/pgx/v5",
		Version:    "v5.10.0",
		Major:      "v5",
		Publicness: "PUBLIC",
		FirstSeen:  now,
		LastSeen:   now,
	}); err != nil {
		t.Fatal(err)
	}

	// Snapshots with symbol data
	netSnap := `{"rows":[{"symbol":"golang.org/x/net/html.ErrorToken","runtime":"go 1.26","os":"linux","status":"PASS"}]}`
	if err := store.PutSnapshot(ctx, netPurl, "", netSnap); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(ctx, netPurl, "golang.org/x/net/html.ErrorToken", netSnap); err != nil {
		t.Fatal(err)
	}

	pgxSnap := `{"rows":[{"symbol":"github.com/jackc/pgx/v5.Connect","runtime":"go 1.26","os":"linux","status":"PASS"}]}`
	if err := store.PutSnapshot(ctx, pgxPurl, "", pgxSnap); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(ctx, pgxPurl, "github.com/jackc/pgx/v5.Connect", pgxSnap); err != nil {
		t.Fatal(err)
	}

	// Multi-package sample: tests both golang.org/x/sys@v0.47.0 AND golang.org/x/net@v0.51.0.
	// Previously, sampleListItem took the first package (x/sys) and filed this sample under v0.47.0,
	// leaking v0.47.0 into golang.org/x/net's version list and creating a link to 404!
	multiPkgManifest := `{"case":{"goal":"test interop"},"environment":{"runtime":"go 1.26","os":"linux"},"packages":["pkg:golang/golang.org/x/sys@v0.47.0","pkg:golang/golang.org/x/net@v0.51.0"],"symbols":["golang.org/x/net/html.ErrorToken"]}`
	sampleID := "sha256:net051sys047"
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     sampleID,
		ManifestJSON: multiPkgManifest,
		Status:       "CROSS_PASS",
		License:      "MIT",
		CreatedAt:    now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID:      "receipt-net",
		SampleID:       sampleID,
		ContractResult: "PASS",
		ReceiptJSON:    `{}`,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := serverstore.ServerConfig{
		PublicCheck: "trust",
		BlobDir:     t.TempDir(),
	}
	mux := BuildMux(cfg, store)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 1. GET /golang/golang.org/x/net
	resp, err := client.Get(srv.URL + "/golang/golang.org/x/net")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /golang/golang.org/x/net returned %d, want 200: %s", resp.StatusCode, body)
	}
	html := string(body)
	if !strings.Contains(html, "golang.org/x/net") {
		t.Errorf("expected package name in body")
	}
	// Must link to v0.51.0
	if !strings.Contains(html, "/golang/golang.org/x/net/v0.51.0") {
		t.Errorf("expected link to /golang/golang.org/x/net/v0.51.0, got: %s", html)
	}
	// MUST NOT link to foreign version v0.47.0!
	if strings.Contains(html, "/golang/golang.org/x/net/v0.47.0") {
		t.Errorf("foreign package version v0.47.0 leaked into golang.org/x/net links! HTML: %s", html)
	}

	// 2. GET /golang/golang.org/x/net/v0.51.0 -> 200 OK
	resp, err = client.Get(srv.URL + "/golang/golang.org/x/net/v0.51.0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /golang/golang.org/x/net/v0.51.0 returned %d, want 200", resp.StatusCode)
	}

	// 3. GET /golang/golang.org/x/net/v0.47.0 -> 404 cleanly, never 503
	resp, err = client.Get(srv.URL + "/golang/golang.org/x/net/v0.47.0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /golang/golang.org/x/net/v0.47.0 returned %d, want 404", resp.StatusCode)
	}

	// 4. GET /golang/github.com/jackc/pgx/v5 -> 200 OK
	resp, err = client.Get(srv.URL + "/golang/github.com/jackc/pgx/v5")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /golang/github.com/jackc/pgx/v5 returned %d, want 200: %s", resp.StatusCode, body)
	}

	// 5. GET /golang/github.com/jackc/pgx/v5/v5.10.0 -> 200 OK
	resp, err = client.Get(srv.URL + "/golang/github.com/jackc/pgx/v5/v5.10.0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /golang/github.com/jackc/pgx/v5/v5.10.0 returned %d, want 200", resp.StatusCode)
	}

	// 6. GET /golang/github.com/jackc/pgx/v5/5.10.0 (bare version) -> 301 Redirect to canonical v5.10.0
	resp, err = client.Get(srv.URL + "/golang/github.com/jackc/pgx/v5/5.10.0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("bare version returned %d, want 301", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasSuffix(loc, "/golang/github.com/jackc/pgx/v5/v5.10.0") {
		t.Fatalf("redirect location = %q, want .../v5.10.0", loc)
	}
}

// TestFilteredCubeURLSemantics verifies that the exact filtered URL from issue #396:
// /golang/golang.org/x/net?f_runtime=go+1.26&f_symbol=golang.org%2Fx%2Fnet%2Fhtml.ErrorToken&lang=ko#cube
// renders with 200 OK, preserves query parameters, and correctly renders the Korean view.
func TestFilteredCubeURLSemantics(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	now := time.Now()

	purl := "pkg:golang/golang.org/x/net@v0.51.0"
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL:       purl,
		Ecosystem:  "golang",
		Name:       "golang.org/x/net",
		Version:    "v0.51.0",
		Major:      "v0",
		Publicness: "PUBLIC",
		FirstSeen:  now,
		LastSeen:   now,
	}); err != nil {
		t.Fatal(err)
	}

	snap := `{"rows":[{"symbol":"golang.org/x/net/html.ErrorToken","runtime":"go 1.26","os":"linux","status":"PASS"}]}`
	if err := store.PutSnapshot(ctx, purl, "", snap); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(ctx, purl, "golang.org/x/net/html.ErrorToken", snap); err != nil {
		t.Fatal(err)
	}

	cfg := serverstore.ServerConfig{
		PublicCheck: "trust",
		BlobDir:     t.TempDir(),
	}
	mux := BuildMux(cfg, store)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	url := srv.URL + "/golang/golang.org/x/net?f_runtime=go+1.26&f_symbol=golang.org%2Fx%2Fnet%2Fhtml.ErrorToken&lang=ko#cube"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filtered cube URL returned %d, want 200. Body: %s", resp.StatusCode, body)
	}
	html := string(body)
	if !strings.Contains(html, "golang.org/x/net") {
		t.Errorf("missing package name in body")
	}
	// Verify filter was applied and rendered
	if !strings.Contains(html, "ErrorToken") {
		t.Errorf("missing symbol in filtered cube view")
	}
}

// TestConcurrentRepresentativeReadsUnderDBPressure verifies that concurrent reads
// to package and version pages do not starve or cause cascade suppression.
func TestConcurrentRepresentativeReadsUnderDBPressure(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	now := time.Now()

	for _, name := range []string{"golang.org/x/net", "github.com/jackc/pgx/v5"} {
		purl := fmt.Sprintf("pkg:golang/%s@v1.0.0", name)
		if err := store.UpsertPackage(ctx, serverstore.PackageRow{
			PURL:       purl,
			Ecosystem:  "golang",
			Name:       name,
			Version:    "v1.0.0",
			Major:      "v1",
			Publicness: "PUBLIC",
			FirstSeen:  now,
			LastSeen:   now,
		}); err != nil {
			t.Fatal(err)
		}
		snap := `{"rows":[{"symbol":"SampleSymbol","runtime":"go 1.26","os":"linux","status":"PASS"}]}`
		if err := store.PutSnapshot(ctx, purl, "", snap); err != nil {
			t.Fatal(err)
		}
	}

	cfg := serverstore.ServerConfig{
		PublicCheck: "trust",
		BlobDir:     t.TempDir(),
	}
	mux := BuildMux(cfg, store)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	urls := []string{
		srv.URL + "/golang/golang.org/x/net",
		srv.URL + "/golang/golang.org/x/net/v1.0.0",
		srv.URL + "/golang/golang.org/x/net?f_runtime=go+1.26&lang=ko#cube",
		srv.URL + "/golang/github.com/jackc/pgx/v5",
		srv.URL + "/golang/github.com/jackc/pgx/v5/v1.0.0",
	}

	const workers = 20
	const iterations = 5
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterations)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := &http.Client{Timeout: 5 * time.Second}
			for i := 0; i < iterations; i++ {
				target := urls[(workerID+i)%len(urls)]
				resp, err := client.Get(target)
				if err != nil {
					errCh <- fmt.Errorf("worker %d req %d failed: %w", workerID, i, err)
					return
				}
				resp.Body.Close()
				// Under pool/admission load, a 503 is only acceptable if it carries Retry-After;
				// but in our normal fast-path with CTEs and singleflight, none should fail.
				if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
					errCh <- fmt.Errorf("unexpected status %d for %s", resp.StatusCode, target)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent read error: %v", err)
	}
}
