package web

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// Every NO_SAFE_MATCH used to be thrown away on the one machine that
// already knew. Aggregated it is the only ranking of demand on this site
// that is not a guess, and the only page a contributor can act on.
func TestWantedPageRanksUnansweredQuestions(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.wanted = []WantedRow{
		{Ecosystem: "npm", Name: "undici", Version: "7.13.0", Symbol: "ProxyAgent", Asks: 214},
		{Ecosystem: "pypi", Name: "protobuf", Asks: 96},
	}
	rec := get(t, mux, "/wanted")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "npm/undici")
	mustContain(t, body, "npm/undici@7.13.0")
	mustContain(t, body, "ProxyAgent")
	mustContain(t, body, "214")
	mustContain(t, body, "pypi/protobuf")
	// Most-asked first.
	if strings.Index(body, "undici") > strings.Index(body, "protobuf") {
		t.Error("the list is not ranked most-asked first")
	}
	// The nav reaches it, or nobody finds it.
	if !strings.Contains(get(t, mux, "/").Body.String(), `href="/wanted"`) {
		t.Error("the front page does not link to the wanted board")
	}
}

// An empty board must say it is empty, not render as a broken page — and
// it must say WHY, because "nobody asked" and "we lost the reports" look
// identical to a visitor.
func TestWantedPageWithNothingWantedSaysSo(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/wanted")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Nothing is being asked for yet")
}

// The question a person typed never reaches this page, and the page has to
// say so — a ranking of questions with no questions on it looks like an
// omission unless the reason is stated.
func TestWantedPageStatesThatQuestionsStayLocal(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.wanted = []WantedRow{{Ecosystem: "npm", Name: "undici", Asks: 3}}
	body := get(t, mux, "/wanted").Body.String()
	mustContain(t, body, "stays on the machine that asked")
}

// Wanted-only package pages now carry an honest coordinate-specific stub,
// so every board row is actionable without turning into a 404. HasPage is
// retained on the store row for compatibility, but no longer decides links.
func TestEveryWantedRowLinksToItsHonestStubPage(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.wanted = []WantedRow{
		{Ecosystem: "npm", Name: "puppeteer", Asks: 3},             // nothing known yet
		{Ecosystem: "npm", Name: "undici", Asks: 2, HasPage: true}, // has evidence
	}
	body := get(t, mux, "/wanted").Body.String()
	mustContain(t, body, "npm/puppeteer")
	mustContain(t, body, `href="/npm/puppeteer"`)
	mustContain(t, body, `href="/npm/undici"`)
	if rec := get(t, mux, "/npm/puppeteer"); rec.Code != http.StatusOK {
		t.Fatalf("wanted-only package status = %d, want 200", rec.Code)
	}
}

func TestWantedSearchMatchesEveryVisibleCoordinateField(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.wanted = []WantedRow{
		{Ecosystem: "pypi", Name: "alpha", Version: "2.0.0", Symbol: "Other", Asks: 10},
		{Ecosystem: "npm", Name: "alpha", Version: "1.2.3", Symbol: "parse.value", Asks: 9},
		{Ecosystem: "npm", Name: "beta", Version: "1.2.3", Symbol: "parse.value", Asks: 8},
	}
	body := get(t, mux, "/wanted?q=npm+alpha+parse").Body.String()
	mustContain(t, body, "npm/alpha@1.2.3")
	if strings.Contains(body, "pypi/alpha") || strings.Contains(body, "npm/beta") {
		t.Fatalf("multi-word search did not require every word: %s", body)
	}
	mustContain(t, body, `name="q" value="npm alpha parse"`)
}

func TestWantedPaginationKeepsStableRanksAndBoundaries(t *testing.T) {
	mux, store := newTestMux(t, nil)
	for i := 81; i >= 0; i-- { // reverse insertion must not decide ties
		store.wanted = append(store.wanted, WantedRow{
			Ecosystem: "npm", Name: fmt.Sprintf("pkg-%03d", i), Asks: 1,
		})
	}

	first := get(t, mux, "/wanted").Body.String()
	mustContain(t, first, "pkg-000")
	mustContain(t, first, "pkg-039")
	if strings.Contains(first, "pkg-040") {
		t.Error("page one crossed the 40-row boundary")
	}
	mustContain(t, first, `class="wantedrank">1</span>`)
	mustContain(t, first, `class="wantedrank">40</span>`)
	mustContain(t, html.UnescapeString(first), `href="/wanted?page=2" rel="next"`)

	second := get(t, mux, "/wanted?page=2").Body.String()
	mustContain(t, second, "pkg-040")
	mustContain(t, second, "pkg-079")
	mustContain(t, second, `class="wantedrank">41</span>`)
	mustContain(t, second, `class="wantedrank">80</span>`)
	if strings.Contains(second, "pkg-039") || strings.Contains(second, "pkg-080") {
		t.Error("page two crossed a stable boundary")
	}

	last := get(t, mux, "/wanted?page=3").Body.String()
	mustContain(t, last, "pkg-080")
	mustContain(t, last, "pkg-081")
	mustContain(t, last, `class="wantedrank">82</span>`)
}

func TestWantedPageNormalizesInvalidAndStalePageNumbers(t *testing.T) {
	mux, store := newTestMux(t, nil)
	for i := 0; i < wantedPerPage+1; i++ {
		store.wanted = append(store.wanted, WantedRow{
			Ecosystem: "npm", Name: fmt.Sprintf("react-addon-%02d", i), Asks: int64(100 - i),
		})
	}
	for _, raw := range []string{"nope", "-2", "0", "999999999999999999999999"} {
		rec := get(t, mux, "/wanted?page="+raw)
		if rec.Code != http.StatusOK {
			t.Errorf("page=%q status=%d, want page one", raw, rec.Code)
		}
		mustContain(t, rec.Body.String(), "react-addon-00")
	}

	rec := get(t, mux, "/wanted?q=react&lang=ko&page=999")
	if rec.Code != http.StatusFound {
		t.Fatalf("stale page status=%d, want redirect", rec.Code)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Path != "/wanted" || location.Query().Get("q") != "react" ||
		location.Query().Get("lang") != "ko" || location.Query().Get("page") != "2" {
		t.Fatalf("stale-page redirect lost state: %q", location.String())
	}
}

func TestWantedPagerAndSearchPreserveQueryAndLanguage(t *testing.T) {
	mux, store := newTestMux(t, nil)
	for i := 0; i < wantedPerPage*2+1; i++ {
		store.wanted = append(store.wanted, WantedRow{
			Ecosystem: "npm", Name: fmt.Sprintf("react-addon-%02d", i), Asks: int64(1000 - i),
		})
	}
	body := html.UnescapeString(get(t, mux, "/wanted?q=react&lang=ko&page=2").Body.String())
	mustContain(t, body, `name="q" value="react"`)
	mustContain(t, body, `type="hidden" name="lang" value="ko"`)
	mustContain(t, body, `href="/wanted?lang=ko&q=react" rel="prev"`)
	mustContain(t, body, `href="/wanted?lang=ko&page=3&q=react" rel="next"`)
	mustContain(t, body, `href="/wanted?lang=ko"`)
}

func TestWantedSearchAndPagerRenderInEveryLocale(t *testing.T) {
	mux, store := newTestMux(t, nil)
	for i := 0; i < wantedPerPage+1; i++ {
		store.wanted = append(store.wanted, WantedRow{
			Ecosystem: "npm", Name: fmt.Sprintf("react-addon-%02d", i), Asks: int64(100 - i),
		})
	}
	for _, lang := range i18n.Supported {
		rec := get(t, mux, "/wanted?q=react&lang="+url.QueryEscape(lang))
		if rec.Code != http.StatusOK {
			t.Errorf("lang=%s status=%d", lang, rec.Code)
			continue
		}
		body := rec.Body.String()
		mustContain(t, body, `role="search"`)
		mustContain(t, body, `class="pager"`)
	}
}

func TestWantedSearchNoMatchesIsNotAnEmptyNetworkClaim(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.wanted = []WantedRow{{Ecosystem: "npm", Name: "react", Asks: 4}}
	body := get(t, mux, "/wanted?q=definitely-absent").Body.String()
	mustContain(t, body, "No packages found")
	if strings.Contains(body, "Nothing is being asked for yet") {
		t.Error("a filtered miss was presented as an empty wanted network")
	}
}
