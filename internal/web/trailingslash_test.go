package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Trailing slashes were handled three different ways: /records/ and
// /findings/ hard 404'd, while a package page served /npm/zod/ as a second
// 200 whose canonical pointed at itself — the same page at two indexed URLs.
// One rule now: the slashless form is the page, everything else redirects.
func TestTrailingSlashAlwaysRedirects(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, path := range []string{
		"/compatibility/",
		"/findings/",
		"/npm/axios/",
		"/npm/axios/1.12.0/",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("GET %s = %d, want 301", path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Location"); got != path[:len(path)-1] {
			t.Errorf("GET %s redirected to %q, want %q", path, got, path[:len(path)-1])
		}
	}
}

// The language must survive the hop, or a redirect silently drops a visitor
// back into English.
func TestTrailingSlashRedirectKeepsTheLanguage(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/compatibility/?lang=ko", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if got := rec.Header().Get("Location"); got != "/compatibility?lang=ko" {
		t.Errorf("Location = %q, want /records?lang=ko", got)
	}
}
