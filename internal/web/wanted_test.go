package web

import (
	"net/http"
	"strings"
	"testing"
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
