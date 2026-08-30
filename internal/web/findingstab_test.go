package web

import (
	"strings"
	"testing"
)

// A filter that empties the list must not move the reader to the other tab.
//
// The auto-flip exists for a cold start: with nothing grown yet, the default
// tab would render as an empty page, so the curated group is shown instead.
// But it also fired when a reader who had CHOSEN the growing tab filtered it
// down to nothing — and then both the active-tab marker and the "Clear
// filters" link handed them the Hand-checked tab they never asked for, so the
// one escape hatch on the page led somewhere else.
//
// Reported from production: /findings?eco=maven renders "0 findings" with
// aria-current on Hand-checked and Clear filters pointing at ?tab=curated.
func TestAnEmptyFilterKeepsTheTabTheReaderChose(t *testing.T) {
	mux, _ := newTestMux(t, nil)

	body := get(t, mux, "/findings?eco=maven").Body.String()
	if !strings.Contains(body, "findings") {
		t.Fatal("the findings page did not render")
	}
	// The escape hatch must lead back to the tab they filtered, not the other
	// one.
	if strings.Contains(body, `href="/findings?tab=curated">`) && !strings.Contains(body, `href="/findings">`) {
		t.Error("the only way out of an empty filter leads to a tab the reader never chose")
	}
	// And the marker must not claim they are standing on the other tab.
	i := strings.Index(body, `<ul class="ftabs">`)
	if i < 0 {
		t.Skip("no tab strip rendered for this fixture")
	}
	strip := body[i:]
	if j := strings.Index(strip, "</ul>"); j > 0 {
		strip = strip[:j]
	}
	curated := strings.Index(strip, "tab=curated")
	current := strings.Index(strip, `aria-current="page"`)
	if curated >= 0 && current > curated {
		t.Errorf("an empty filter moved the reader to the Hand-checked tab:\n%s", strip)
	}
}
