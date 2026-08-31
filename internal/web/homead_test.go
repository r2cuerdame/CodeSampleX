package web

import (
	"net/http"
	"strings"
	"testing"
)

const adSnippet = `src="https://adisad.com/s/ydpads8kquzopej7nps4oppu.js"`

// adPages is every page a reader can reach from the navigation, plus the home
// page. All of them carry the placement now.
var adPages = []string{"/", "/samples", "/records", "/findings", "/wanted", "/features"}

// Every page carries the placement, and exactly one.
//
// The snippet takes the FIRST [data-adisad-slot] in the document, so a page
// with two slots would place its ad by document order rather than by anyone's
// decision. The home page has its own, mid-page between the findings and the
// install block; the shared one in base.html stands down there and serves
// everywhere else.
func TestEveryPageCarriesExactlyOnePlacement(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, path := range adPages {
		body := get(t, mux, path).Body.String()
		if n := strings.Count(body, adSnippet); n != 1 {
			t.Errorf("%s carries %d ad scripts, want exactly 1", path, n)
		}
		if n := strings.Count(body, "data-adisad-slot"); n != 1 {
			t.Errorf("%s declares %d slots; the snippet takes the first and the rest are a coin toss", path, n)
		}
	}
}

// The slot is outside <main> on every page but the home page.
//
// This is the rule that matters. Without a slot the snippet inserts its unit
// at the midpoint of the paragraphs inside <main> — and on a page whose
// content IS the evidence, that midpoint is inside a finding card. It happened
// on the live home page: an advertisement nested under a finding's measured
// line, where a reader scanning findings has no reason to expect that the box
// below it is paid.
func TestTheSharedSlotIsOutsideTheContent(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, path := range adPages {
		if path == "/" {
			continue // the home page places its own, deliberately mid-page
		}
		body := get(t, mux, path).Body.String()
		mainEnd := strings.Index(body, "</main>")
		slot := strings.Index(body, "data-adisad-slot")
		if mainEnd < 0 || slot < 0 {
			t.Errorf("%s: main=%d slot=%d", path, mainEnd, slot)
			continue
		}
		if slot < mainEnd {
			t.Errorf("%s puts the ad inside <main>, where the evidence is", path)
		}
	}
}

// Async, so a slow or dead ad network cannot hold up the page a reader came
// for. This is the one attribute the placement must not lose, on any page.
func TestTheAdNeverBlocksTheRestOfThePage(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, path := range adPages {
		body := get(t, mux, path).Body.String()
		i := strings.Index(body, adSnippet)
		if i < 0 {
			t.Errorf("%s has no ad placement", path)
			continue
		}
		tag := body[strings.LastIndex(body[:i], "<script"):]
		if j := strings.Index(tag, ">"); j > 0 {
			tag = tag[:j]
		}
		if !strings.Contains(tag, "async") {
			t.Errorf("%s loads the ad script render-blocking: %s", path, tag)
		}
	}
}

// The drill-down pages carry it too, which is what "every step" means: a
// reader who clicks from the collection into a package, a version, a symbol or
// a sample never leaves the placement behind.
//
// They inherit it rather than declaring it — every one of these templates is a
// "content" block inside base.html — so this test exists to notice if one ever
// stops being, not because each needed wiring.
func TestTheDrillDownPagesCarryItToo(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["npm|axios"] = []string{"1.12.0"}
	for _, path := range []string{
		"/npm/axios",
		"/npm/axios?f_version=1.12.0",
	} {
		body := get(t, mux, path).Body.String()
		if n := strings.Count(body, adSnippet); n != 1 {
			t.Errorf("%s carries %d ad scripts, want exactly 1", path, n)
		}
		mainEnd := strings.Index(body, "</main>")
		slot := strings.Index(body, "data-adisad-slot")
		if mainEnd >= 0 && slot >= 0 && slot < mainEnd {
			t.Errorf("%s puts the ad inside <main>", path)
		}
	}
}

func newTestMuxOnly(t *testing.T) *http.ServeMux {
	t.Helper()
	mux, _ := newTestMux(t, nil)
	return mux
}
