package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// shardWith renders a shard body listing exactly these sample ids.
func shardWith(key string, sampleIDs ...string) string {
	var samples []map[string]string
	for _, id := range sampleIDs {
		samples = append(samples, map[string]string{
			"sampleId": id, "goal": "parse a semver range", "status": "PUBLISHED",
		})
	}
	body, _ := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"key":           key,
		"packages": []map[string]any{{
			"purl":    "pkg:cargo/semver@1.0.28",
			"samples": samples,
		}},
	})
	return string(body)
}

// Indexing a shard was add-only, so nothing the network published could
// ever be taken back. A sample withdrawn on the server — quarantined for a
// takedown, or found to be wrong — stayed in the local index of every
// machine that had synced it once and kept being returned as a HIT, with
// no way for the server to reach it.
func TestResyncedShardStopsAnsweringWithARemovedSample(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)

	const key = "cargo/semver/1"
	withdrawn := "sha256:" + strings.Repeat("cd", 32)
	kept := "sha256:" + strings.Repeat("ab", 32)

	bodies := []string{
		shardWith(key, kept, withdrawn), // first sync: both
		shardWith(key, kept),            // after the takedown: only one
	}
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := bodies[n]
		if n < len(bodies)-1 {
			n++
		}
		w.Header().Set("ETag", fmt.Sprintf("etag-%d", n))
		w.Write([]byte(body))
	}))
	defer srv.Close()

	sy := &Syncer{DB: db, ServerURL: srv.URL, HTTP: srv.Client()}
	if err := sy.SyncKey(ctx, key); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if !indexHas(t, db, withdrawn) {
		t.Fatal("setup: the sample should be indexed after the first sync")
	}

	if err := sy.SyncKey(ctx, key); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if indexHas(t, db, withdrawn) {
		t.Error("a sample the shard no longer lists is still answerable locally")
	}
	if !indexHas(t, db, kept) {
		t.Error("the sample that is still listed was retired too")
	}
}

// indexHas reports whether a sample doc is present in the local FTS index.
func indexHas(t *testing.T, db *localdb.DB, sampleID string) bool {
	t.Helper()
	hits, err := db.FTSQuery(context.Background(), "semver range parse", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.DocID == "sample:"+sampleID {
			return true
		}
	}
	return false
}
