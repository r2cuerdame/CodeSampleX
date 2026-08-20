package web

import (
	"strings"
	"testing"
)

// The production shape, and the reason this section exists: every observation
// is from Windows, every proof is from Linux, so the overlap is zero. A
// reader must be told that in words, not left to compute it from a table.
func TestCoverageNamesAZeroOverlap(t *testing.T) {
	got := buildCoverageDisclosure([]CoverageRow{
		{OS: "windows", Ecosystem: "npm", Observed: 2287},
		{OS: "linux", Ecosystem: "npm", Measured: 158, Proven: 158},
	}, nil)
	if got.Overlap != 0 {
		t.Errorf("overlap = %d, want 0", got.Overlap)
	}
	if got.ObservedTotal != 2287 || got.ProvenTotal != 158 {
		t.Errorf("totals = %d observed / %d proven", got.ObservedTotal, got.ProvenTotal)
	}
}

// Every verification on record passed, which looks like a perfect instrument
// and is a publishing rule: a sample becomes a sample by passing. Saying
// "330 of 330" without that caveat is the most flattering false impression
// this page could give.
func TestCoverageFlagsTheSelectionEffect(t *testing.T) {
	all := buildCoverageDisclosure([]CoverageRow{
		{OS: "linux", Ecosystem: "npm", Measured: 158, Proven: 158},
	}, nil)
	if !all.SelectionNote {
		t.Error("an all-passing record did not disclose that it is selected")
	}
	mixed := buildCoverageDisclosure([]CoverageRow{
		{OS: "linux", Ecosystem: "npm", Measured: 158, Proven: 150},
	}, nil)
	if mixed.SelectionNote {
		t.Error("a record containing failures claimed selection")
	}
	empty := buildCoverageDisclosure(nil, nil)
	if empty.SelectionNote {
		t.Error("an empty record claimed a selection effect")
	}
}

// The disclosure has to actually reach the page. A template that fails to
// parse takes the whole site down, and one that silently renders nothing
// leaves a reader assuming coverage — the exact impression it exists to
// prevent.
func TestLandingRendersTheCoverageDisclosure(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.coverage = []CoverageRow{
		{OS: "windows", Ecosystem: "npm", Observed: 2287},
		{OS: "linux", Ecosystem: "npm", Measured: 158, Proven: 158},
	}
	body := get(t, mux, "/").Body.String()
	mustContain(t, body, `id="coverage"`)
	mustContain(t, body, "2287")
	// Zero overlap must be stated in words, not left to be computed.
	mustContain(t, body, "never met")
	// npm on windows can never be proven; a zero there is closed, not owed.
	mustContain(t, body, "windows/npm")
}

// With nothing to disclose the section stays away rather than rendering an
// empty table that reads as "nothing to report".
func TestLandingOmitsTheDisclosureWhenItHasNothing(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	if body := get(t, mux, "/").Body.String(); strings.Contains(body, `id="coverage"`) {
		t.Error("an empty disclosure rendered anyway")
	}
}
