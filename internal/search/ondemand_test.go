package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// `csx sync` warms the shards for packages already in the local inventory
// plus the server's HOT list — and the library an agent asks about is, in
// the normal case, one it is ABOUT TO ADD. That package is in neither
// list, so the network could hold a verified answer while this machine
// returned NO_SAFE_MATCH and advised a `csx sync` that would never fetch
// it.
func TestAMissFetchesTheShardForANamedPackage(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		w.Header().Set("ETag", `"e1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schemaVersion":1,"key":"npm/puppeteer/24","packages":[]}`))
	}))
	defer srv.Close()

	e := Engine{DB: db}
	sy := &Syncer{DB: db, ServerURL: srv.URL, HTTP: srv.Client()}
	req := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "render a pdf from html",
		Packages:      []string{"pkg:npm/puppeteer@24.10.0"},
		// The tree must NOT be fetched: it is context, and asking for a
		// shard names the package to the server.
		ProjectPackages: []string{"pkg:npm/express@5.1.0"},
	}

	if !FetchMissing(context.Background(), e, sy, "community", req) {
		t.Fatal("a named package's shard was not fetched on a miss")
	}
	if len(asked) != 1 {
		t.Fatalf("fetched %d shards: %v", len(asked), asked)
	}
	if got := asked[0]; got != "/v1/shards/npm/puppeteer/24" {
		t.Errorf("fetched %q", got)
	}

	// Once cached, a second miss does not ask again.
	before := len(asked)
	FetchMissing(context.Background(), e, sy, "community", req)
	if len(asked) != before {
		t.Errorf("re-fetched a shard already cached: %v", asked)
	}
}

// Local-only answers from what it has. Naming a package to the server is
// precisely what that mode exists to prevent.
func TestLocalOnlyNeverFetchesOnAMiss(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("local-only asked the server for %s", r.URL.Path)
	}))
	defer srv.Close()

	if FetchMissing(context.Background(), Engine{DB: db},
		&Syncer{DB: db, ServerURL: srv.URL, HTTP: srv.Client()}, "local-only",
		domain.SearchRequest{SchemaVersion: 1, Packages: []string{"pkg:npm/puppeteer@24.10.0"}}) {
		t.Error("local-only reported a fetch")
	}
}
