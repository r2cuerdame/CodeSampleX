package search

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// fixtureSampleID is a syntactically valid content id for the fixture sample.
const fixtureSampleID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fixtureShard is a C6-shaped shard body for npm/axios/1.
const fixtureShard = `{
  "schemaVersion": 1,
  "key": "npm/axios/1",
  "generatedAt": "2026-08-13T00:00:00Z",
  "packages": [
    {
      "purl": "pkg:npm/axios@1.12.0",
      "symbols": [
        {
          "family": "axios.post",
          "stats": {
            "observationCount": 123,
            "uniquePeerBuckets": 9,
            "passRate": 0.94,
            "byStage": {"PROJECT_COMPILE": {"pass": 100, "fail": 4}},
            "confidence": "HIGH",
            "lastSeen": "2026-08-12T00:00:00Z"
          },
          "failures": [
            {
              "errorCode": "ERR_REQUIRE_ESM",
              "fingerprint": "sha256:ab",
              "count": 7,
              "envSummary": {"moduleSystem": "esm", "runtime": "node@18"}
            }
          ]
        }
      ],
      "samples": [
        {
          "sampleId": "` + fixtureSampleID + `",
          "goal": "post JSON with axios interceptors",
          "status": "CROSS_PASS",
          "license": "MIT-0",
          "environment": {"schemaVersion": 1, "ecosystem": "npm", "os": "linux", "arch": "amd64"},
          "contractStages": {"contract": "PASS"}
        }
      ]
    }
  ]
}`

// shardServer records every request so tests can assert conditional-GET
// behavior. It serves fixtureShard for /v1/shards/npm/axios/1 with the
// given ETag, answering 304 when If-None-Match matches.
type shardServer struct {
	mu   sync.Mutex
	etag string
	// ifNoneMatch holds the If-None-Match header of each request in order.
	ifNoneMatch []string
	statuses    []int
}

func (s *shardServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path != "/v1/shards/npm/axios/1" {
			s.ifNoneMatch = append(s.ifNoneMatch, r.Header.Get("If-None-Match"))
			s.statuses = append(s.statuses, http.StatusNotFound)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		inm := r.Header.Get("If-None-Match")
		s.ifNoneMatch = append(s.ifNoneMatch, inm)
		if inm != "" && inm == s.etag {
			s.statuses = append(s.statuses, http.StatusNotModified)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		s.statuses = append(s.statuses, http.StatusOK)
		w.Header().Set("ETag", s.etag)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixtureShard))
	})
}

func newTestDB(t *testing.T) *localdb.DB {
	t.Helper()
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatalf("open localdb: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestShardSyncEtagAnd304(t *testing.T) {
	ctx := context.Background()
	srv := &shardServer{etag: `"v1"`}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	db := newTestDB(t)
	sy := &Syncer{DB: db, HTTP: ts.Client(), ServerURL: ts.URL}

	// First sync: no stored etag, so no If-None-Match; 200 stores the shard.
	if err := sy.SyncKey(ctx, "npm/axios/1"); err != nil {
		t.Fatalf("first SyncKey: %v", err)
	}
	row, ok, err := db.GetShard(ctx, "npm/axios/1")
	if err != nil || !ok {
		t.Fatalf("shard not stored after 200: ok=%v err=%v", ok, err)
	}
	if row.ETag != `"v1"` {
		t.Fatalf("etag = %q, want %q", row.ETag, `"v1"`)
	}
	if !strings.Contains(row.JSON, "axios.post") {
		t.Fatalf("stored shard JSON missing body")
	}
	if row.SyncedAt.IsZero() {
		t.Fatalf("synced_at not stamped")
	}

	// Second sync: must send If-None-Match and accept 304 without clobbering.
	if err := sy.SyncKey(ctx, "npm/axios/1"); err != nil {
		t.Fatalf("second SyncKey: %v", err)
	}
	srv.mu.Lock()
	inm, statuses := srv.ifNoneMatch, srv.statuses
	srv.mu.Unlock()
	if len(inm) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(inm))
	}
	if inm[0] != "" {
		t.Fatalf("first request carried If-None-Match %q, want none", inm[0])
	}
	if inm[1] != `"v1"` {
		t.Fatalf("second request If-None-Match = %q, want %q", inm[1], `"v1"`)
	}
	if statuses[1] != http.StatusNotModified {
		t.Fatalf("second response status = %d, want 304", statuses[1])
	}
	row2, ok, err := db.GetShard(ctx, "npm/axios/1")
	if err != nil || !ok {
		t.Fatalf("shard gone after 304: ok=%v err=%v", ok, err)
	}
	if row2.JSON != row.JSON || row2.ETag != row.ETag {
		t.Fatalf("304 must keep body and etag unchanged")
	}
	if row2.SyncedAt.IsZero() {
		t.Fatalf("304 must keep synced_at stamped")
	}
}

func TestShardSyncIndexesFTS(t *testing.T) {
	ctx := context.Background()
	srv := &shardServer{etag: `"v1"`}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	db := newTestDB(t)
	sy := &Syncer{DB: db, HTTP: ts.Client(), ServerURL: ts.URL}
	if err := sy.SyncKey(ctx, "npm/axios/1"); err != nil {
		t.Fatalf("SyncKey: %v", err)
	}

	// Symbol doc is findable by error code.
	hits, err := db.FTSQuery(ctx, "ERR_REQUIRE_ESM", 10)
	if err != nil {
		t.Fatalf("FTSQuery: %v", err)
	}
	if !hasDoc(hits, "shard:npm/axios/1:axios.post") {
		t.Fatalf("symbol doc not found by error code; hits=%+v", hits)
	}

	// Symbol doc is findable by family tokens.
	hits, err = db.FTSQuery(ctx, "axios post", 10)
	if err != nil {
		t.Fatalf("FTSQuery: %v", err)
	}
	if !hasDoc(hits, "shard:npm/axios/1:axios.post") {
		t.Fatalf("symbol doc not found by family; hits=%+v", hits)
	}

	// Sample doc is findable by its goal text and carries kind "sample".
	hits, err = db.FTSQuery(ctx, "interceptors", 10)
	if err != nil {
		t.Fatalf("FTSQuery: %v", err)
	}
	found := false
	for _, h := range hits {
		if h.DocID == "sample:"+fixtureSampleID {
			found = true
			if h.Kind != "sample" {
				t.Fatalf("sample doc kind = %q, want %q", h.Kind, "sample")
			}
		}
	}
	if !found {
		t.Fatalf("sample doc not found by goal; hits=%+v", hits)
	}
}

func hasDoc(hits []localdb.DocHit, docID string) bool {
	for _, h := range hits {
		if h.DocID == docID {
			return true
		}
	}
	return false
}

func TestShardSync404IsNil(t *testing.T) {
	ctx := context.Background()
	srv := &shardServer{etag: `"v1"`}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	db := newTestDB(t)
	sy := &Syncer{DB: db, HTTP: ts.Client(), ServerURL: ts.URL}
	if err := sy.SyncKey(ctx, "npm/left-pad/1"); err != nil {
		t.Fatalf("404 should be nil error, got %v", err)
	}
	if _, ok, _ := db.GetShard(ctx, "npm/left-pad/1"); ok {
		t.Fatalf("404 must not store a shard row")
	}
}

func TestShardSyncNetworkError(t *testing.T) {
	ctx := context.Background()
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close() // server down: connection refused

	db := newTestDB(t)
	sy := &Syncer{DB: db, HTTP: &http.Client{}, ServerURL: url}
	if err := sy.SyncKey(ctx, "npm/axios/1"); err == nil {
		t.Fatalf("network error must surface to caller")
	}
}

func TestShardSyncAllContinuesOnFailure(t *testing.T) {
	ctx := context.Background()
	srv := &shardServer{etag: `"v1"`}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "boom") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		srv.handler().ServeHTTP(w, r)
	}))
	defer ts.Close()

	db := newTestDB(t)
	sy := &Syncer{DB: db, HTTP: ts.Client(), ServerURL: ts.URL}
	err := sy.SyncAll(ctx, []string{"npm/boom/1", "npm/axios/1"})
	if err == nil {
		t.Fatalf("SyncAll must aggregate the 500 into an error")
	}
	// The failing key must not prevent the good key from syncing.
	if _, ok, gerr := db.GetShard(ctx, "npm/axios/1"); gerr != nil || !ok {
		t.Fatalf("good key not synced despite failure earlier in list: ok=%v err=%v", ok, gerr)
	}
	var agg interface{ Unwrap() []error }
	if !errors.As(err, &agg) {
		t.Logf("aggregated error (not errors.Join style, still acceptable): %v", err)
	}
}

func TestWarmKeysPriorityAndDedup(t *testing.T) {
	project := []domain.PURL{
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0"},
		{Ecosystem: "golang", Name: "github.com/a/b", Version: "v1.2.0"},
	}
	recent := []string{"npm/axios/1", "npm/react/18"}
	hot := []string{"npm/react/18", "npm/zod/3"}
	pinned := []string{"pypi/requests/2", "npm/axios/1"}

	got := WarmKeys(project, recent, hot, pinned)
	want := []string{
		"npm/axios/1",
		"golang/github.com/a/b/v1",
		"npm/react/18",
		"npm/zod/3",
		"pypi/requests/2",
	}
	if len(got) != len(want) {
		t.Fatalf("WarmKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("WarmKeys[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}
