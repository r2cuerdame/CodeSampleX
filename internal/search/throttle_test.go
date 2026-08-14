package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A throttled warm used to be dropped, leaving that package with no shard —
// and search answers NO_SAFE_MATCH for a package it has no shard for, so the
// user never got an answer about it and nothing said why. A 429 is the
// server asking us to come back, not a failure.
func TestThrottledShardWarmIsRetriedNotDropped(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("ETag", "x")
		w.Write([]byte(`{"schemaVersion":1,"key":"npm/axios/1","packages":[]}`))
	}))
	defer ts.Close()

	db := newTestDB(t)
	sy := &Syncer{DB: db, HTTP: ts.Client(), ServerURL: ts.URL}
	warmed, err := sy.SyncAll(ctx, []string{"npm/axios/1"})
	if err != nil {
		t.Fatalf("a throttled key should still end up warmed: %v", err)
	}
	if warmed != 1 {
		t.Errorf("warmed = %d, want 1", warmed)
	}
	if calls.Load() < 2 {
		t.Errorf("server saw %d call(s); the 429 was not retried", calls.Load())
	}
	if _, ok, gerr := db.GetShard(ctx, "npm/axios/1"); gerr != nil || !ok {
		t.Errorf("shard missing after retry: ok=%v err=%v", ok, gerr)
	}
}
