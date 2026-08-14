package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A sample page named its files and offered no way to read them, so a
// visitor arriving from a search result — the entrance the sitemap work
// just built — could not open the sample that answered their question. The
// artifact endpoint is public and content-addressed, so the page can hand
// over exactly what was verified.
func TestSamplePageOffersTheArtifact(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	const id = "sha256:d1e2f3"

	req := httptest.NewRequest(http.MethodGet, sampleHref(id), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	want := "/v1/samples/" + id + "/artifact"
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("sample page does not link the artifact (%s)", want)
	}
}
