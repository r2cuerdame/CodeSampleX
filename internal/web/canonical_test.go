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
		req := httptest.NewRequest(http.MethodGet, "/records?lang="+lang, nil)
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
	req := httptest.NewRequest(http.MethodGet, "/records", nil)
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
