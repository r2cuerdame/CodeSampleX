package web

import (
	"strconv"
	"strings"
	"testing"
)

// findingsTileLabel is the English label of the counter under test.
const findingsTileLabel = "Findings"

func formatTestInt(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// The third counter measured the ecosystem: how many distinct APIs the
// network had observed. Coverage of somebody else's work is the weakest
// thing this page can say about itself, and the strongest is the one it
// stands behind — a measured correction, each backed by a published sample
// whose contract runs. So the counter is the findings.
func TestHomepageCountsFindingsRatherThanCoveredAPIs(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.derived = []DerivedFinding{
		{Ecosystem: "pypi", Subject: "httpx@0.28.1", Believed: "one timeout",
			Measured: "four timeouts", SampleID: "sha256:" + strings.Repeat("a", 64)},
		{Ecosystem: "npm", Subject: "ws@8.19.0", Believed: "close waits",
			Measured: "close returns first", SampleID: "sha256:" + strings.Repeat("b", 64)},
	}
	body := get(t, mux, "/").Body.String()

	mustContain(t, body, findingsTileLabel)
	if strings.Contains(body, "APIs covered") {
		t.Error("the covered-API counter is still on the homepage")
	}
	// Two derived plus every hand-checked entry, which is the exact count
	// the findings page itself reports.
	want := 2 + len(documentedFindings) + len(believedFindings)
	if !strings.Contains(body, `aria-label="`+findingsTileLabel+`: `+formatTestInt(want)+`"`) {
		t.Errorf("homepage does not report %d findings", want)
	}
}

// A stats outage blanks the counters it comes from. The findings count does
// not come from there — it is read from the same place the findings page
// reads it — so blanking it would hide a number the page knows.
func TestFindingsCounterSurvivesAStatsOutage(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.statsOK = false
	body := get(t, mux, "/").Body.String()
	want := len(documentedFindings) + len(believedFindings)
	if !strings.Contains(body, `aria-label="`+findingsTileLabel+`: `+formatTestInt(want)+`"`) {
		t.Errorf("findings counter blanked with the stats: want %d", want)
	}
}
