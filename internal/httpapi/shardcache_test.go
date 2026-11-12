package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
	// 47 s is before the last fifth of the cadence, so no reread starts and
	// the document simply expires.
	if err := store.PutShard(context.Background(), "npm/zod/3", "v2", v2); err != nil {
		t.Fatal(err)
	}
	ck.t = ck.t.Add(47 * time.Second)
	if _, etag, _ := getShard(t, url, ""); etag != `"v1"` {
		t.Fatalf("inside the cadence ETag = %q, want the remembered v1", etag)
	}

	ck.t = ck.t.Add(13 * time.Second)
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

func TestShardCacheRetainsFrequentlyAccessedShardWithoutExtendingFreshness(t *testing.T) {
	base := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	now := base
	a := &api{}
	a.d.Now = func() time.Time { return now }
	a.d.Cfg.SnapshotInterval = 5 * time.Minute

	a.rememberShard("npm/zod/3", "zod-etag", "{}", base)
	for i := range shardCacheMaxEntries - 1 {
		at := base.Add(time.Duration(i+1) * time.Millisecond)
		a.rememberShard("npm/other/"+time.Duration(i).String(), "etag", "{}", at)
	}
	if len(a.shards.entries) != shardCacheMaxEntries {
		t.Fatalf("cache has %d entries, want %d", len(a.shards.entries), shardCacheMaxEntries)
	}

	// A hot entry is old by insertion time but recent by use. It should
	// survive the next capacity eviction while an untouched entry is removed.
	now = base.Add(4 * time.Minute)
	if _, ok := a.cachedShard("npm/zod/3"); !ok {
		t.Fatal("hot shard was unavailable before eviction")
	}
	a.rememberShard("npm/new/1", "new-etag", "{}", now)
	if _, ok := a.cachedShard("npm/zod/3"); !ok {
		t.Fatal("frequently accessed shard was evicted")
	}
	if _, ok := a.cachedShard("npm/other/0s"); ok {
		t.Fatal("least recently accessed shard survived eviction")
	}

	// Access recency affects eviction only: it must not extend the one-cadence
	// freshness limit or serve stale data after the original read expires.
	now = base.Add(5 * time.Minute)
	if _, ok := a.cachedShard("npm/zod/3"); ok {
		t.Fatal("hot shard outlived its original freshness deadline")
	}
}

// shardRefreshStore lets a test hold, fail or retire the shard a reread asks
// for, and counts every read like shardCountingStore.
type shardRefreshStore struct {
	shardCountingStore
	hold    chan struct{}
	fail    atomic.Bool
	retired atomic.Bool
}

func (s *shardRefreshStore) GetShardEtag(ctx context.Context, key string) (string, bool, error) {
	if s.hold != nil {
		<-s.hold
	}
	if s.fail.Load() {
		s.etagReads.Add(1)
		return "", false, serverstore.ErrPoolBusy
	}
	if s.retired.Load() {
		s.etagReads.Add(1)
		return "", false, nil
	}
	return s.shardCountingStore.GetShardEtag(ctx, key)
}

func (s *shardRefreshStore) GetShard(ctx context.Context, key string) (string, string, bool, error) {
	if s.retired.Load() {
		s.fullReads.Add(1)
		return "", "", false, nil
	}
	return s.shardCountingStore.GetShard(ctx, key)
}

func newShardRefreshAPI(t *testing.T) (*api, *serverstore.Fake, *shardRefreshStore, *time.Time) {
	t.Helper()
	fake := serverstore.NewFake()
	spy := &shardRefreshStore{}
	spy.Store = fake
	now := time.Date(2026, 11, 12, 3, 28, 0, 0, time.UTC)
	a := &api{}
	a.d.Store = spy
	a.d.Now = func() time.Time { return now }
	a.d.Cfg.SnapshotInterval = time.Minute
	return a, fake, spy, &now
}

func serveShard(t *testing.T, a *api, ifNoneMatch string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/shards/npm/zod/3", nil)
	req.SetPathValue("ecosystem", "npm")
	req.SetPathValue("rest", "zod/3")
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	a.handleShard(rec, req)
	return rec.Code, rec.Header().Get("ETag"), rec.Body.String()
}

// waitShardRefresh waits for the reread in flight for key, if any.
func waitShardRefresh(t *testing.T, a *api, key string) {
	t.Helper()
	a.shards.mu.Lock()
	done := a.shards.refreshing[key]
	a.shards.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shard reread did not finish")
	}
}

// #695: the SLO probe's slow samples are the requests that reach the store.
// A shard requested in the last fifth of its cadence is reread in the
// background, so under steady traffic no request waits for the store after
// the first, and a document is still never served past one cadence.
func TestHotShardIsRereadBeforeItExpires(t *testing.T) {
	a, fake, spy, now := newShardRefreshAPI(t)
	const v1 = `{"schemaVersion":1,"key":"npm/zod/3","packages":[{"name":"zod"}]}`
	const v2 = `{"schemaVersion":1,"key":"npm/zod/3","packages":[]}`
	if err := fake.PutShard(context.Background(), "npm/zod/3", "v1", v1); err != nil {
		t.Fatal(err)
	}
	t0 := *now
	if status, etag, body := serveShard(t, a, ""); status != http.StatusOK || etag != `"v1"` || body != v1 {
		t.Fatalf("first read = %d %q %q", status, etag, body)
	}

	// Before the last fifth nothing is reread.
	*now = t0.Add(47 * time.Second)
	serveShard(t, a, "")
	waitShardRefresh(t, a, "npm/zod/3")
	if full, tag := spy.fullReads.Load(), spy.etagReads.Load(); full != 1 || tag != 0 {
		t.Fatalf("before the window: %d full and %d ETag reads, want 1 and 0", full, tag)
	}

	// Inside it the request is answered from memory and an unchanged shard
	// costs one ETag read, not a second detoast of the document.
	*now = t0.Add(50 * time.Second)
	if status, etag, body := serveShard(t, a, ""); status != http.StatusOK || etag != `"v1"` || body != v1 {
		t.Fatalf("inside the window = %d %q %q", status, etag, body)
	}
	waitShardRefresh(t, a, "npm/zod/3")
	if full, tag := spy.fullReads.Load(), spy.etagReads.Load(); full != 1 || tag != 1 {
		t.Fatalf("unchanged reread: %d full and %d ETag reads, want 1 and 1", full, tag)
	}

	// Past the first read's cadence the reread's own cadence serves it.
	*now = t0.Add(65 * time.Second)
	if status, _, _ := serveShard(t, a, `"v1"`); status != http.StatusNotModified {
		t.Fatalf("after the first cadence = %d, want 304 from memory", status)
	}
	if full, tag := spy.fullReads.Load(), spy.etagReads.Load(); full != 1 || tag != 1 {
		t.Fatalf("after the first cadence: %d full and %d ETag reads, want 1 and 1", full, tag)
	}

	// A pass rewrites the shard: the next reread fetches the new document,
	// and once the old one would have expired the new one is answered.
	if err := fake.PutShard(context.Background(), "npm/zod/3", "v2", v2); err != nil {
		t.Fatal(err)
	}
	*now = t0.Add(100 * time.Second)
	if _, etag, _ := serveShard(t, a, ""); etag != `"v1"` {
		t.Fatalf("reread trigger ETag = %q, want the remembered v1", etag)
	}
	waitShardRefresh(t, a, "npm/zod/3")
	*now = t0.Add(111 * time.Second)
	if status, etag, body := serveShard(t, a, `"v1"`); status != http.StatusOK || etag != `"v2"` || body != v2 {
		t.Fatalf("after the rewrite = %d %q %q, want v2", status, etag, body)
	}
	if full := spy.fullReads.Load(); full != 2 {
		t.Fatalf("full reads = %d, want 2: the first request and the changed reread", full)
	}
}

// Requests arriving while a reread is in flight start no other.
func TestShardRereadIsSingleFlight(t *testing.T) {
	a, fake, spy, now := newShardRefreshAPI(t)
	if err := fake.PutShard(context.Background(), "npm/zod/3", "v1", `{}`); err != nil {
		t.Fatal(err)
	}
	t0 := *now
	serveShard(t, a, "")
	spy.hold = make(chan struct{})
	*now = t0.Add(55 * time.Second)
	for range 5 {
		if status, etag, _ := serveShard(t, a, ""); status != http.StatusOK || etag != `"v1"` {
			t.Fatalf("held reread: request = %d %q, want 200 v1 from memory", status, etag)
		}
	}
	a.shards.mu.Lock()
	inFlight := len(a.shards.refreshing)
	a.shards.mu.Unlock()
	if inFlight != 1 {
		t.Fatalf("%d rereads in flight, want 1", inFlight)
	}
	close(spy.hold)
	waitShardRefresh(t, a, "npm/zod/3")
	if tag := spy.etagReads.Load(); tag != 1 {
		t.Fatalf("ETag reads = %d, want 1", tag)
	}
}

// A failed reread leaves the document to expire on schedule; a retired shard
// stops being served at once.
func TestShardRereadFailureAndRetirement(t *testing.T) {
	a, fake, spy, now := newShardRefreshAPI(t)
	if err := fake.PutShard(context.Background(), "npm/zod/3", "v1", `{}`); err != nil {
		t.Fatal(err)
	}
	t0 := *now
	serveShard(t, a, "")

	spy.fail.Store(true)
	*now = t0.Add(55 * time.Second)
	serveShard(t, a, "")
	waitShardRefresh(t, a, "npm/zod/3")
	if e, ok := a.cachedShard("npm/zod/3"); !ok || !e.at.Equal(t0) {
		t.Fatalf("after a failed reread cached = %+v %v, want the original read", e, ok)
	}
	spy.fail.Store(false)
	*now = t0.Add(60 * time.Second)
	if _, ok := a.cachedShard("npm/zod/3"); ok {
		t.Fatal("a failed reread extended the document past its cadence")
	}

	// A fresh read, then a pass retires the shard.
	if status, _, _ := serveShard(t, a, ""); status != http.StatusOK {
		t.Fatalf("reread after expiry = %d, want 200", status)
	}
	spy.retired.Store(true)
	*now = t0.Add(115 * time.Second)
	serveShard(t, a, "")
	waitShardRefresh(t, a, "npm/zod/3")
	if status, _, _ := serveShard(t, a, ""); status != http.StatusNotFound {
		t.Fatalf("retired shard = %d, want 404", status)
	}
}
