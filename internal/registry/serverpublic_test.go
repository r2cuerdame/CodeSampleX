package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestServerCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	cache := &ServerCache{Store: serverstore.NewFake()}

	if _, _, ok := cache.GetPublicness(ctx, "pkg:npm/axios@1.12.0"); ok {
		t.Fatal("empty store must be a cache miss")
	}
	if err := cache.SetPublicness(ctx, "pkg:npm/axios@1.12.0", scanner.PublicnessPublic); err != nil {
		t.Fatalf("SetPublicness: %v", err)
	}
	status, at, ok := cache.GetPublicness(ctx, "pkg:npm/axios@1.12.0")
	if !ok || status != scanner.PublicnessPublic || at.IsZero() {
		t.Fatalf("GetPublicness = (%q, %v, %v), want PUBLIC hit", status, at, ok)
	}
}

func TestServerCacheUncheckedPackageIsMiss(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	// A package known only from evidence ingest: no checked_at yet.
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL: "pkg:npm/left@1.0.0", Ecosystem: "npm", Name: "left", Version: "1.0.0",
		Major: "1", Publicness: scanner.PublicnessUnknown,
	}); err != nil {
		t.Fatal(err)
	}
	cache := &ServerCache{Store: store}
	if _, _, ok := cache.GetPublicness(ctx, "pkg:npm/left@1.0.0"); ok {
		t.Fatal("package without checked_at must be a cache miss")
	}
}

func TestServerCacheRejectsBadPURL(t *testing.T) {
	cache := &ServerCache{Store: serverstore.NewFake()}
	if err := cache.SetPublicness(context.Background(), "not-a-purl", scanner.PublicnessPublic); err == nil {
		t.Fatal("SetPublicness must reject unparsable purls")
	}
}

// TestCheckerWithServerCache proves the client Checker composes with the
// server-side cache: first call hits the fake registry, second is served
// from the packages table.
func TestCheckerWithServerCache(t *testing.T) {
	ctx := context.Background()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	checker := &Checker{
		Cache:    &ServerCache{Store: serverstore.NewFake()},
		HTTP:     srv.Client(),
		BaseURLs: map[string]string{"npm": srv.URL},
	}
	p := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	if got := checker.Check(ctx, p); got != scanner.PublicnessPublic {
		t.Fatalf("first Check = %q, want PUBLIC", got)
	}
	if got := checker.Check(ctx, p); got != scanner.PublicnessPublic {
		t.Fatalf("second Check = %q, want PUBLIC", got)
	}
	if hits != 1 {
		t.Fatalf("registry hits = %d, want 1 (second call served from cache)", hits)
	}
}
