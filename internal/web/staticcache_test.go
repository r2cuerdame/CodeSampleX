package web

import (
	"net/http"
	"strings"
	"testing"
)

// A versioned asset may be cached forever; an unversioned one may not.
//
// Every page links the stylesheet as /static/site.css?v=<short revision>, so
// the URL changes exactly when the bytes do. Serving that with max-age=3600
// makes a returning visitor re-fetch 26KB every hour over whatever link they
// have, and without an ETag the revalidation cannot even come back 304 — it
// re-sends the whole file.
//
// The same file requested WITHOUT the token is a different promise: nothing
// makes that URL change, so it gets a short life and a validator.
func TestAVersionedAssetIsCachedForeverAndAnUnversionedOneIsNot(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rev := testBuild().Revision
	if len(rev) < 7 {
		t.Fatalf("the test build has no revision to version by: %q", rev)
	}
	short := rev[:7]

	versioned := get(t, mux, "/static/site.css?v="+short)
	if versioned.Code != http.StatusOK {
		t.Fatalf("versioned asset status %d", versioned.Code)
	}
	cc := versioned.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") {
		t.Errorf("a versioned asset is not immutable: %q", cc)
	}
	if !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("a versioned asset is not cached for a year: %q", cc)
	}

	plain := get(t, mux, "/static/site.css")
	pc := plain.Header().Get("Cache-Control")
	if strings.Contains(pc, "immutable") {
		t.Errorf("an unversioned asset was marked immutable: %q", pc)
	}
	if strings.Contains(pc, "max-age=31536000") {
		t.Errorf("an unversioned asset was cached for a year: %q", pc)
	}
}

// Both carry a validator, so a revalidation is a 304 rather than a re-send.
func TestAStaticAssetCanBeRevalidated(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	first := get(t, mux, "/static/site.css")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("a static asset carries no ETag, so revalidating re-sends it whole")
	}

	rec := get(t, mux, "/static/site.css", "If-None-Match", etag)
	if rec.Code != http.StatusNotModified {
		t.Errorf("revalidation returned %d, not 304; the body was re-sent", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a 304 carried %d bytes of body", rec.Body.Len())
	}
}

// The header is sent once. Caddy set it too, so production answered with two
// Cache-Control lines for every static file.
func TestCacheControlIsSentOnce(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	if got := len(get(t, mux, "/static/site.css").Header().Values("Cache-Control")); got != 1 {
		t.Errorf("Cache-Control appears %d times, want exactly 1", got)
	}
}
