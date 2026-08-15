package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// shardBody renders a shard listing one package with these sample ids.
func shardBody(key, purl string, sampleIDs ...string) string {
	var samples []map[string]string
	for _, id := range sampleIDs {
		samples = append(samples, map[string]string{
			"sampleId": id, "goal": "parse a semver range", "status": "PUBLISHED",
		})
	}
	body, _ := json.Marshal(map[string]any{
		"schemaVersion": 1, "key": key,
		"packages": []map[string]any{{"purl": purl, "samples": samples}},
	})
	return string(body)
}

// A sample is listed by EVERY shard of every package it declares, and its
// FTS document is not namespaced by shard key — one document serves them
// all. Retiring it on one shard's word alone would drop a live sample out
// of local search the moment the 20-sample cap evicted it from one of its
// packages, and the other shard would not put it back: an unchanged shard
// answers 304 and is never re-indexed.
//
// A sample the network still publishes must stay answerable.
func TestASampleStillListedByAnotherShardIsNotRetired(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)

	shared := "sha256:" + strings.Repeat("ef", 32)
	const keyA, keyB = "npm/left-pad/1", "npm/right-pad/1"

	bodies := map[string][]string{
		keyA: {
			shardBody(keyA, "pkg:npm/left-pad@1.0.0", shared),
			shardBody(keyA, "pkg:npm/left-pad@1.0.0"), // evicted here
		},
		keyB: {shardBody(keyB, "pkg:npm/right-pad@1.0.0", shared)},
	}
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/v1/shards/")
		list := bodies[key]
		i := seen[key]
		if i >= len(list) {
			i = len(list) - 1
		}
		seen[key]++
		w.Header().Set("ETag", fmt.Sprintf("%s-%d", key, i))
		w.Write([]byte(list[i]))
	}))
	defer srv.Close()

	sy := &Syncer{DB: db, ServerURL: srv.URL, HTTP: srv.Client()}
	for _, k := range []string{keyA, keyB} {
		if err := sy.SyncKey(ctx, k); err != nil {
			t.Fatalf("sync %s: %v", k, err)
		}
	}
	if !indexHas(t, db, shared) {
		t.Fatal("setup: the sample should be indexed after the first syncs")
	}

	// left-pad's shard drops it; right-pad's still lists it.
	if err := sy.SyncKey(ctx, keyA); err != nil {
		t.Fatalf("re-sync %s: %v", keyA, err)
	}
	if !indexHas(t, db, shared) {
		t.Error("a sample another shard still lists was retired: it is now unanswerable locally")
	}
}
