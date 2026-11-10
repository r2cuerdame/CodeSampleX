package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// shardCountingStore counts every shard read, full and ETag-only, so a test
// can pin which requests reach the store.
type shardCountingStore struct {
	serverstore.Store
	fullReads atomic.Int64
	etagReads atomic.Int64
}

func (s *shardCountingStore) GetShard(ctx context.Context, key string) (string, string, bool, error) {
	s.fullReads.Add(1)
	return s.Store.GetShard(ctx, key)
}

func (s *shardCountingStore) GetShardEtag(ctx context.Context, key string) (string, bool, error) {
	s.etagReads.Add(1)
	return s.Store.GetShardEtag(ctx, key)
}

func getShard(t *testing.T, url, ifNoneMatch string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("ETag"), string(body)
}

// #639: production answered the 151 KB npm/zod/3 shard with 0.49 s median
// application time, all of it pool wait and the store read, for a document
// that changes once per aggregation pass. Inside one builder cadence a read
// and a revalidation are answered without the store; past it the next request
// reads again and picks up what the pass wrote.
func TestShardIsReadOncePerBuilderCadence(t *testing.T) {
	spy := &shardCountingStore{}
	srv, store, ck := newTestServer(t, func(d *Deps) {
		d.Cfg.SnapshotInterval = time.Minute
		spy.Store = d.Store
		d.Store = spy
	})
	const v1 = `{"schemaVersion":1,"key":"npm/zod/3","packages":[{"name":"zod"}]}`
	const v2 = `{"schemaVersion":1,"key":"npm/zod/3","packages":[]}`
	if err := store.PutShard(context.Background(), "npm/zod/3", "v1", v1); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/v1/shards/npm/zod/3"

	if status, etag, body := getShard(t, url, ""); status != http.StatusOK || etag != `"v1"` || body != v1 {
		t.Fatalf("first read = %d %q %q", status, etag, body)
	}
	if got := spy.fullReads.Load(); got != 1 {
		t.Fatalf("first read reached the store %d times, want 1", got)
	}

	if status, etag, body := getShard(t, url, ""); status != http.StatusOK || etag != `"v1"` || body != v1 {
		t.Fatalf("second read = %d %q %q", status, etag, body)
	}
	if status, _, body := getShard(t, url, `"v1"`); status != http.StatusNotModified || body != "" {
		t.Fatalf("revalidation = %d %q, want 304 with no body", status, body)
	}
	if status, etag, body := getShard(t, url, `"older"`); status != http.StatusOK || etag != `"v1"` || body != v1 {
		t.Fatalf("stale-ETag revalidation = %d %q %q, want the document", status, etag, body)
	}
	if full, tag := spy.fullReads.Load(), spy.etagReads.Load(); full != 1 || tag != 0 {
		t.Fatalf("inside the cadence: %d full and %d ETag reads, want 1 and 0", full, tag)
	}

	// A pass rewrites the shard. Within the cadence the remembered document
	// is still the answer; that one cadence is the whole of the staleness.
	if err := store.PutShard(context.Background(), "npm/zod/3", "v2", v2); err != nil {
		t.Fatal(err)
	}
	ck.t = ck.t.Add(59 * time.Second)
	if _, etag, _ := getShard(t, url, ""); etag != `"v1"` {
		t.Fatalf("inside the cadence ETag = %q, want the remembered v1", etag)
	}

	ck.t = ck.t.Add(time.Second)
	if status, etag, body := getShard(t, url, `"v1"`); status != http.StatusOK || etag != `"v2"` || body != v2 {
		t.Fatalf("after the cadence = %d %q %q, want the rebuilt v2", status, etag, body)
	}
	if status, _, _ := getShard(t, url, `"v2"`); status != http.StatusNotModified {
		t.Fatalf("revalidating the rebuilt shard = %d, want 304", status)
	}
	if full := spy.fullReads.Load(); full != 2 {
		t.Fatalf("full reads = %d, want 2: one per cadence", full)
	}
}

// An absent shard is not remembered: a key the next pass writes is served on
// the very next request, not one cadence later.
func TestAbsentShardIsNotRemembered(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	url := srv.URL + "/v1/shards/npm/zod/3"
	if status, _, _ := getShard(t, url, ""); status != http.StatusNotFound {
		t.Fatalf("absent shard = %d, want 404", status)
	}
	if err := store.PutShard(context.Background(), "npm/zod/3", "v1", `{"schemaVersion":1}`); err != nil {
		t.Fatal(err)
	}
	if status, etag, _ := getShard(t, url, ""); status != http.StatusOK || etag != `"v1"` {
		t.Fatalf("written shard = %d %q, want 200 v1", status, etag)
	}
}

// A slow read that began before a newer one must not replace it, and the
// memory stays inside its byte and entry bounds, giving up the oldest read
// first.
func TestShardCacheKeepsNewestReadWithinBounds(t *testing.T) {
	a := &api{}
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	a.d.Now = func() time.Time { return t0 }

	a.rememberShard("npm/zod/3", "new", "{}", t0)
	a.rememberShard("npm/zod/3", "old", "{}", t0.Add(-time.Second))
	if e, ok := a.cachedShard("npm/zod/3"); !ok || e.etag != "new" {
		t.Fatalf("cached = %+v %v, want the newer read", e, ok)
	}

	doc := strings.Repeat("x", 1<<20)
	for i := range 40 {
		a.rememberShard("npm/big/"+string(rune('a'+i)), "e", doc, t0.Add(time.Duration(i)*time.Millisecond))
	}
	if a.shards.bytes > shardCacheMaxBytes {
		t.Fatalf("cache holds %d bytes, over %d", a.shards.bytes, shardCacheMaxBytes)
	}
	if _, ok := a.cachedShard("npm/zod/3"); ok {
		t.Fatal("oldest entry survived eviction")
	}
	if _, ok := a.cachedShard("npm/big/" + string(rune('a'+39))); !ok {
		t.Fatal("newest entry was evicted")
	}
	a.rememberShard("npm/huge/1", "e", strings.Repeat("x", shardCacheMaxBytes+1), t0)
	if _, ok := a.cachedShard("npm/huge/1"); ok {
		t.Fatal("a document over the whole bound was remembered")
	}

	b := &api{}
	b.d.Now = a.d.Now
	for i := range shardCacheMaxEntries + 10 {
		b.rememberShard("npm/small/"+time.Duration(i).String(), "e", "{}", t0.Add(time.Duration(i)))
	}
	if n := len(b.shards.entries); n > shardCacheMaxEntries {
		t.Fatalf("cache holds %d entries, over %d", n, shardCacheMaxEntries)
	}
}
