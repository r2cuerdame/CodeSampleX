package web

import (
	"net/http"
	"strings"
	"testing"
)

const adSnippet = `src="https://adisad.com/s/ydpads8kquzopej7nps4oppu.js"`

// The ad is a home-page placement, and only that.
//
// The snippet inserts its container immediately after its own script tag, so
// the tag's position is the ad's position — which also means a tag that leaks
// into the shared base template would put an ad on every record, finding and
// sample page without anyone choosing to.
func TestTheAdIsOnTheHomePageAndNowhereElse(t *testing.T) {
	mux, _ := newTestMux(t, nil)

	if body := get(t, mux, "/").Body.String(); !strings.Contains(body, adSnippet) {
		t.Error("the home page carries no ad placement")
	}
	for _, path := range []string{"/samples", "/records", "/findings", "/wanted", "/features"} {
		if strings.Contains(get(t, mux, path).Body.String(), adSnippet) {
			t.Errorf("%s carries the home ad placement", path)
		}
	}
}

// Async, so a slow or dead ad network cannot hold up the page a reader came
// for. This is the one attribute the placement must not lose.
func TestTheAdNeverBlocksTheRestOfThePage(t *testing.T) {
	body := get(t, newTestMuxOnly(t), "/").Body.String()
	i := strings.Index(body, adSnippet)
	if i < 0 {
		t.Fatal("no ad placement on the home page")
	}
	tag := body[strings.LastIndex(body[:i], "<script"):]
	if j := strings.Index(tag, ">"); j > 0 {
		tag = tag[:j]
	}
	if !strings.Contains(tag, "async") {
		t.Errorf("the ad script is render-blocking: %s", tag)
	}
}

func newTestMuxOnly(t *testing.T) *http.ServeMux {
	t.Helper()
	mux, _ := newTestMux(t, nil)
	return mux
}

// The placement is pinned, not left to the snippet's fallback.
//
// Without [data-adisad-slot] the snippet inserts its unit at the midpoint of
// the paragraphs inside <main>. Measured on the live page, that midpoint is
// inside a finding card — so the ad rendered nested in a piece of measured
// evidence, where a reader could take it as part of the finding. That is the
// one place on this site an advertisement must never be.
func TestTheAdSlotIsPinnedSoItCannotLandInsideEvidence(t *testing.T) {
	body := get(t, newTestMuxOnly(t), "/").Body.String()
	i := strings.Index(body, adSnippet)
	if i < 0 {
		t.Fatal("no ad placement on the home page")
	}
	slot := body[strings.LastIndex(body[:i], "<aside"):i]
	if !strings.Contains(slot, "data-adisad-slot") {
		t.Errorf("the ad slot carries no publisher hook, so the snippet chooses its own spot: %s", slot)
	}
}
