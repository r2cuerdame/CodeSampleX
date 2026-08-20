package web

import (
	"strings"
	"testing"
)

// The findings page led with thirty-one hand-written entries and put the
// machine-derived group behind them. That ordering was right when the
// derived group was empty and wrong once it passed five hundred: the group
// that grows on its own every time a sample is published is the page's
// substance, and the curated list is a series maintained beside it.
func TestFindingsLeadsWithTheGroupThatGrowsByItself(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.derived = []DerivedFinding{{
		Ecosystem: "pypi", Subject: "httpx@0.28.1",
		Believed: "one timeout covers the whole request",
		Measured: "each phase gets its own timeout instead",
		SampleID: "sha256:" + strings.Repeat("a", 64),
	}}
	body := get(t, mux, "/findings").Body.String()

	mustContain(t, body, "httpx@0.28.1")
	if strings.Contains(body, "Contradicts an official source</h2>") {
		t.Error("the curated group still renders on the default tab")
	}
}

// The curated entries are not deleted, only moved: they keep a tab of their
// own, which is where promoting a derived finding puts it.
func TestFindingsCuratedTabHoldsTheHandCheckedGroups(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/findings?tab=curated").Body.String()

	mustContain(t, body, "Contradicts an official source</h2>")
	mustContain(t, body, "Widely believed, measured otherwise</h2>")
	if strings.Contains(body, "Stated by the sample, measured by its contract</h2>") {
		t.Error("the growing group renders on the curated tab")
	}
}

// Both tabs are reachable from either one, and the active tab says so.
func TestFindingsRendersATabStrip(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/findings").Body.String()
	mustContain(t, body, `class="ftabs"`)
	mustContain(t, body, `href="/findings?tab=curated"`)
	mustContain(t, body, `aria-current="page"`)
}

// Links carrying the old basis parameter were published and must keep
// landing on the entries they named.
func TestFindingsHonoursTheOldBasisParameter(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/findings?basis=docs").Body.String()
	mustContain(t, body, "Contradicts an official source</h2>")
}
