package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serveShardEncoded(t testing.TB, a *api, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/shards/npm/zod/3", nil)
	req.SetPathValue("ecosystem", "npm")
	req.SetPathValue("rest", "zod/3")
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	a.handleShard(rec, req)
	return rec
}

func gunzipBody(t *testing.T, body []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(plain)
}

// #739: the CLI syncs the zod shard with Accept-Encoding: gzip, and every
// sync compressed the 151 KB document again on a host short of CPU. A
// remembered document is compressed once per read from the store; syncs,
// identity reads and an unchanged reread reuse it, and a rewritten shard is
// compressed once more.
func TestRememberedShardIsCompressedOncePerRead(t *testing.T) {
	a, fake, spy, now := newShardRefreshAPI(t)
	var compressions atomic.Int64
	a.shards.compress = func(doc string) []byte {
		compressions.Add(1)
		return gzipShard(doc)
	}
	const v1 = `{"schemaVersion":1,"key":"npm/zod/3","packages":[{"name":"zod"}]}`
	const v2 = `{"schemaVersion":1,"key":"npm/zod/3","packages":[]}`
	if err := fake.PutShard(context.Background(), "npm/zod/3", "v1", v1); err != nil {
		t.Fatal(err)
	}
	t0 := *now

	for i := range 3 {
		rec := serveShardEncoded(t, a, "gzip")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("ETag") != `"v1"` {
			t.Fatalf("sync %d = %d %q %q", i, rec.Code, rec.Header().Get("Content-Encoding"), rec.Header().Get("ETag"))
		}
		if !bytes.Equal(rec.Body.Bytes(), gzipShard(v1)) {
			t.Fatalf("sync %d body differs from the streamed gzip body", i)
		}
		if got := gunzipBody(t, rec.Body.Bytes()); got != v1 {
			t.Fatalf("sync %d decoded = %q", i, got)
		}
	}
	if rec := serveShardEncoded(t, a, ""); rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != v1 {
		t.Fatalf("identity read = %d %q %q", rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.String())
	}
	if got := compressions.Load(); got != 1 {
		t.Fatalf("compressed %d times for one store read, want 1", got)
	}

	// An unchanged reread keeps the compressed body with the document.
	*now = t0.Add(50 * time.Second)
	serveShardEncoded(t, a, "gzip")
	waitShardRefresh(t, a, "npm/zod/3")
	if full, tag := spy.fullReads.Load(), spy.etagReads.Load(); full != 1 || tag != 1 {
		t.Fatalf("unchanged reread: %d full and %d ETag reads, want 1 and 1", full, tag)
	}
	if got := compressions.Load(); got != 1 {
		t.Fatalf("an unchanged reread compressed again: %d", got)
	}

	// A rewritten shard is read and compressed once, then served compressed.
	if err := fake.PutShard(context.Background(), "npm/zod/3", "v2", v2); err != nil {
		t.Fatal(err)
	}
	*now = t0.Add(3 * time.Minute)
	for range 2 {
		rec := serveShardEncoded(t, a, "gzip")
		if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"v2"` {
			t.Fatalf("after the rewrite = %d %q", rec.Code, rec.Header().Get("ETag"))
		}
		if got := gunzipBody(t, rec.Body.Bytes()); got != v2 {
			t.Fatalf("after the rewrite decoded = %q", got)
		}
	}
	if got := compressions.Load(); got != 2 {
		t.Fatalf("compressed %d times for two store reads, want 2", got)
	}
}

// The compressed body is part of what the memory bound counts.
func TestShardCacheCountsCompressedBody(t *testing.T) {
	a := &api{}
	t0 := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	a.d.Now = func() time.Time { return t0 }
	doc := syntheticShard(2000)
	a.rememberShard("npm/zod/3", "e", doc, t0)
	e, ok := a.cachedShard("npm/zod/3")
	if !ok || len(e.gz) == 0 {
		t.Fatalf("remembered = %v with %d gzip bytes", ok, len(e.gz))
	}
	if want := len("npm/zod/3") + len("e") + len(doc) + len(e.gz); a.shards.bytes != want {
		t.Fatalf("cache counts %d bytes, want %d", a.shards.bytes, want)
	}
	if got := gunzipBody(t, e.gz); got != doc {
		t.Fatal("remembered gzip body does not decode to the document")
	}
}

// syntheticShard is a shard-shaped JSON document of n packages; 1000 is about
// the 151 KB of production's npm/zod/3. Real shards repeat less, so this
// compresses faster than the real one.
func syntheticShard(n int) string {
	var b strings.Builder
	b.WriteString(`{"schemaVersion":1,"key":"npm/zod/3","packages":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"zod","version":"3.%d.%d","sampleId":"s-%08x","grade":"verified","env":{"os":"linux","node":"%d.%d"},"symbols":["z.object","z.string","z.parse%d"]}`,
			i%25, i%13, uint32(i)*2654435761, 18+i%5, i%11, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

// BenchmarkShardSync measures one gzip sync of a ~151 KB shard served from
// memory, against compressing the document again per request (what the
// handler did before #739, on top of the same serving).
func BenchmarkShardSync(b *testing.B) {
	doc := syntheticShard(1000)
	b.Run("remembered", func(b *testing.B) {
		a := &api{}
		a.d.Cfg.SnapshotInterval = time.Hour
		a.rememberShard("npm/zod/3", "e", doc, time.Now().UTC())
		b.SetBytes(int64(len(doc)))
		b.ReportAllocs()
		for b.Loop() {
			if rec := serveShardEncoded(b, a, "gzip"); rec.Code != http.StatusOK {
				b.Fatal(rec.Code)
			}
		}
	})
	b.Run("compress-per-request", func(b *testing.B) {
		b.SetBytes(int64(len(doc)))
		b.ReportAllocs()
		for b.Loop() {
			_ = gzipShard(doc)
		}
	})
}
