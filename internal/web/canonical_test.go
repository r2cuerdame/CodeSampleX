package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// Every localized page named the English URL as its canonical, so nine
// translations told search engines to index only one of them. A canonical
// that disagrees with the page's own hreflang entry is a contradiction, and
// the crawler resolves it by dropping the translation.
func TestLocalizedPagesAreSelfCanonical(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, lang := range i18n.Supported {
		req := httptest.NewRequest(http.MethodGet, "/compatibility?lang="+lang, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", lang, rec.Code)
		}
		body := rec.Body.String()
		want := "?lang=" + lang
		canonical := canonicalOf(body)
		if lang == i18n.Default {
			if strings.Contains(canonical, "lang=") {
				t.Errorf("default locale canonical carries a lang: %q", canonical)
			}
			continue
		}
		if !strings.Contains(canonical, want) {
			t.Errorf("%s canonical = %q, want it to carry %q", lang, canonical, want)
		}
	}
}

// The body depends on Accept-Language and on the language cookie, so a
// shared cache must be told, or it will serve one visitor's language to the
// next.
func TestLanguageVaryIsDeclared(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/compatibility", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	vary := strings.ToLower(strings.Join(rec.Header().Values("Vary"), ","))
	for _, want := range []string{"accept-language", "cookie"} {
		if !strings.Contains(vary, want) {
			t.Errorf("Vary = %q, missing %q", vary, want)
		}
	}
}

func canonicalOf(body string) string {
	const marker = `rel="canonical" href="`
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// A canonical URL must be a function of the address and of nothing else.
//
// Measured on production during the 2026-08-06..09-02 Search Console window:
// `GET /npm/nanoid` with `Accept-Language: ko` answered
// `<link rel="canonical" href="https://codesamplex.dev/npm/nanoid?lang=ko">`,
// so the canonical package page disavowed itself in favour of its own query
// variant on every locale-adaptive crawl. That is the ?lang= cannibalization
// the issue reports against the package and version routes.
func TestCanonicalDoesNotVaryWithRequestHeaders(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	for _, path := range []string{
		"/npm/browserslist", "/npm/browserslist/4.28.7",
		"/compatibility", "/findings", "/samples", "/gaps", "/dependencies",
	} {
		plain := canonicalOf(get(t, mux, path).Body.String())
		if plain == "" {
			t.Fatalf("%s has no canonical", path)
		}
		if strings.Contains(plain, "lang=") {
			t.Errorf("%s canonical = %q, want no lang on the undecorated URL", path, plain)
		}
		for _, hdr := range [][2]string{
			{"Accept-Language", "ko"},
			{"Accept-Language", "ja,en;q=0.8"},
			{"Cookie", "csx_lang=de"},
		} {
			got := canonicalOf(get(t, mux, path, hdr[0], hdr[1]).Body.String())
			if got != plain {
				t.Errorf("%s with %s: %q canonical = %q, want %q",
					path, hdr[0], hdr[1], got, plain)
			}
		}
	}
}

// The ?lang= URLs stay indexable in their own right: the fix above removes
// the contradiction, not the translations. Each locale variant is still
// self-canonical and still advertises the whole cluster.
func TestExplicitLangURLsStaySelfCanonical(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	for _, path := range []string{"/npm/browserslist", "/npm/browserslist/4.28.7"} {
		for _, lang := range i18n.Supported {
			body := get(t, mux, path+"?lang="+lang).Body.String()
			want := "https://codesamplex.dev" + path
			if lang != i18n.Default {
				want += "?lang=" + lang
			}
			if got := canonicalOf(body); got != want {
				t.Errorf("%s?lang=%s canonical = %q, want %q", path, lang, got, want)
			}
			if !strings.Contains(body, `hreflang="x-default"`) {
				t.Errorf("%s?lang=%s dropped its hreflang cluster", path, lang)
			}
		}
	}
}

// The landing is the site's strongest URL and it disavowed itself: built
// from the negotiated language, `GET /` with `Accept-Language: ko` answered
// canonical=/ko/ on production. The locale cluster here is path-prefixed,
// so the address decides the canonical and an explicit ?lang= resolves to
// that locale's own path rather than to a query variant nothing links to.
func TestLandingCanonicalFollowsTheAddress(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	const base = "https://codesamplex.dev"

	cases := []struct {
		target, header, want string
	}{
		{"/", "", base + "/"},
		{"/", "ko", base + "/"},
		{"/", "ja,en;q=0.8", base + "/"},
		{"/ko/", "", base + "/ko/"},
		{"/ko/", "ja", base + "/ko/"},
		{"/?lang=ko", "", base + "/ko/"},
		{"/?lang=en", "ko", base + "/"},
		// /en/ serves English so a browser asking for another language can
		// still reach it, but the hreflang cluster and the sitemap both name
		// "/" for English. A self-canonical /en/ would be a second address
		// for the page they point at.
		{"/en/", "", base + "/"},
		{"/en/", "ko", base + "/"},
		{"/en/?lang=ja", "", base + "/ja/"},
	}
	for _, tc := range cases {
		var hdr []string
		if tc.header != "" {
			hdr = []string{"Accept-Language", tc.header}
		}
		got := canonicalOf(get(t, mux, tc.target, hdr...).Body.String())
		if got != tc.want {
			t.Errorf("GET %s (Accept-Language %q) canonical = %q, want %q",
				tc.target, tc.header, got, tc.want)
		}
	}
}
