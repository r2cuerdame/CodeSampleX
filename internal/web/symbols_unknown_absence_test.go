package web

// A symbol list the store could not read is UNKNOWN, not empty (#445).
//
// The production adapter used to answer a failed target-index read with
// (nil, nil) so a cold index would not 503 every package page (#396). That
// left the version page unable to tell "this release has no symbols" from
// "the index could not be read", and its absence rule -- no symbols, no
// matrix, no samples, no failures -> 404 -- fired on the second as if it were
// the first. The adapter now propagates the error; these tests pin what the
// page does with it: keep serving a release that has other evidence, and
// answer 503 rather than 404 for one that has none.

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestVersionRouteWithUnknownSymbolsNever404s(t *testing.T) {
	ResetRouteMetrics()
	mux, f := newTestMux(t, nil)

	// 1. Healthy: a release with no evidence at all is a proven 404.
	rec := get(t, mux, "/npm/axios/99.99.99")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("healthy empty release status = %d, want 404", rec.Code)
	}
	if got := GetRouteMetrics().ProvenNotFound; got != 1 {
		t.Fatalf("ProvenNotFound after healthy 404 = %d, want 1", got)
	}

	// 2. Symbols unreadable, nothing else known: unknown, not absent.
	ResetRouteMetrics()
	f.symbolsErr = serverstore.ErrPoolBusy
	rec = get(t, mux, "/npm/axios/99.99.99")
	if rec.Code == http.StatusNotFound {
		t.Fatalf("release with unreadable symbols rendered 404; want 503")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("release with unreadable symbols status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	m := GetRouteMetrics()
	if m.ProvenNotFound != 0 {
		t.Errorf("ProvenNotFound = %d under unreadable symbols, want 0", m.ProvenNotFound)
	}
	if m.Final503 != 1 {
		t.Errorf("Final503 = %d, want 1", m.Final503)
	}
	if m.PoolBusy != 1 {
		t.Errorf("PoolBusy = %d, want 1 (classified before the page decided)", m.PoolBusy)
	}

	// 3. Symbols unreadable but the release has published samples: the page
	// still serves them (the #396 contract) rather than hiding them behind
	// a 503 -- and certainly never claims the release is absent.
	rec = get(t, mux, "/npm/axios/1.12.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("release with samples and unreadable symbols status = %d, want 200; body=%s", rec.Code, truncate(rec.Body.String()))
	}
	mustContain(t, rec.Body.String(), `rel="canonical" href="https://codesamplex.dev/npm/axios/1.12.0"`)
	mustNotContain(t, rec.Body.String(), `id="error-retry-btn"`)

	// 4. Recovery: once the index reads again, the same URLs answer as before.
	f.symbolsErr = nil
	if got := get(t, mux, "/npm/axios/99.99.99").Code; got != http.StatusNotFound {
		t.Errorf("recovered empty release status = %d, want 404", got)
	}
	if got := get(t, mux, "/npm/axios/1.12.0").Code; got != http.StatusOK {
		t.Errorf("recovered release status = %d, want 200", got)
	}
}

// The package page's cube reads symbols per release. An unreadable index
// must not take the whole package page down (#396), and must not make the
// package look absent (#445).
func TestPackageRouteWithUnknownSymbolsStillServes(t *testing.T) {
	ResetRouteMetrics()
	mux, f := newTestMux(t, nil)
	f.symbolsErr = errors.New("snapshot target load deferred: pool busy")

	rec := get(t, mux, "/npm/axios")
	if rec.Code == http.StatusNotFound {
		t.Fatalf("package with unreadable symbols rendered 404")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("package with unreadable symbols status = %d, want 200 (other evidence present); body=%s", rec.Code, truncate(rec.Body.String()))
	}
	if got := GetRouteMetrics().ProvenNotFound; got != 0 {
		t.Errorf("ProvenNotFound = %d, want 0", got)
	}
}

// Localized variants are the same handlers under ?lang= and a cookie, so
// they inherit the contract -- but the error page they render is localized,
// and it has to be the "temporarily unavailable" page in that language, not
// the "not found" one.
func TestLocalizedDetailRoutesUnderTransientFailureNever404(t *testing.T) {
	type variant struct {
		lang       string
		notFound   string
		unavailable string
	}
	variants := []variant{
		{"ko", "페이지를 찾을 수 없습니다", "데이터를 불러올 수 없습니다"},
		{"ja", "ページが見つかりません", "データを取得できません"},
	}
	routes := []struct {
		name   string
		path   string
		inject func(f *fakeStore, err error)
	}{
		{"version", "/npm/axios/1.12.0", func(f *fakeStore, err error) { f.packageSamplesErr = err }},
		{"symbol", "/npm/axios/1.12.0/axios.post", func(f *fakeStore, err error) { f.packageSamplesErr = err }},
		{"sample", "/samples/sha256:d1e2f3", func(f *fakeStore, err error) { f.sampleMetaErr = err }},
		{"package", "/npm/axios", func(f *fakeStore, err error) { f.versionsErr = err }},
	}
	for _, v := range variants {
		for _, rt := range routes {
			t.Run(v.lang+"/"+rt.name, func(t *testing.T) {
				ResetRouteMetrics()
				mux, f := newTestMux(t, nil)
				url := rt.path + "?lang=" + v.lang

				if got := get(t, mux, url).Code; got != http.StatusOK {
					t.Fatalf("healthy %s status = %d, want 200", url, got)
				}

				rt.inject(f, serverstore.ErrPoolBusy)
				rec := get(t, mux, url)
				if rec.Code == http.StatusNotFound {
					t.Fatalf("%s rendered 404 under pool pressure", url)
				}
				if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusGatewayTimeout {
					t.Fatalf("%s status = %d, want 503/504", url, rec.Code)
				}
				body := rec.Body.String()
				mustContain(t, body, `<html lang="`+v.lang+`"`)
				mustContain(t, body, v.unavailable)
				mustNotContain(t, body, v.notFound)
				mustContain(t, body, `id="error-retry-btn"`)
				if got := rec.Header().Get("Retry-After"); got != "2" {
					t.Errorf("Retry-After = %q, want 2", got)
				}
				if got := GetRouteMetrics().ProvenNotFound; got != 0 {
					t.Errorf("ProvenNotFound = %d, want 0", got)
				}

				rt.inject(f, nil)
				if got := get(t, mux, url).Code; got != http.StatusOK {
					t.Errorf("recovered %s status = %d, want 200", url, got)
				}
			})
		}
	}
}
