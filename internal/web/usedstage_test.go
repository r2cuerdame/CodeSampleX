package web

import (
	"strings"
	"testing"
)

// USED is a presence marker — "this package was in the project" — and it has
// no failing form: across the whole corpus it carries 8,697 passes and zero
// failures, structurally, because there is nothing for it to fail at. Folding
// it into the pass rate put a term in the numerator that can only ever go one
// way, and it was 12.5% of every pass the network had recorded.
func TestUsedStageIsNotPartOfThePassRate(t *testing.T) {
	row := pvRow("linux", "", "node", "22", "2026-08-19T00:00:00Z", map[string]stageCount{
		"PROJECT_TEST": {Pass: 81, Fail: 19},
		"USED":         {Pass: 100},
	})
	rows := []snapshotRow{row}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	cell := cellAt(t, g, "linux", "node 22")

	// 81 of 100 runs passed. Counting the 100 usage records would read 90%.
	if cell.Ratio != "81%" {
		t.Errorf("ratio = %q, want 81%% — usage records must not lift it", cell.Ratio)
	}
	if cell.Passes != "100" {
		t.Errorf("passes = %q, want the 100 runs the rate divides", cell.Passes)
	}
	if !strings.Contains(cell.Tip, "100 usage records") {
		t.Errorf("tip = %q, want the usage records counted separately", cell.Tip)
	}
}

// Presence is not a run. A package seen in a thousand projects that nobody
// has built has no pass rate, and printing one would invent a measurement
// out of a head count.
func TestUsageAloneReportsNoRate(t *testing.T) {
	rows := []snapshotRow{pvRow("linux", "", "node", "22", "2026-08-19T00:00:00Z",
		map[string]stageCount{"USED": {Pass: 229}})}
	g := buildPivot(rows, osRowKey, contextColKey, nil, pivotNow)
	cell := cellAt(t, g, "linux", "node 22")

	if cell.Ratio != "—" {
		t.Errorf("ratio = %q, want no rate: nothing was run", cell.Ratio)
	}
	if cell.Passes != "" {
		t.Errorf("passes = %q, want nothing beside a rate that does not exist", cell.Passes)
	}
	// But the usage is real and must still be reported.
	if !strings.Contains(cell.Tip, "229 usage records") {
		t.Errorf("tip = %q, want the usage stated", cell.Tip)
	}
	if cell.Class == "empty" {
		t.Error("a package seen in real projects is not an empty cell")
	}
}

// The same invariant on the CUBE path. buildPivot honoured it while
// mergeCubeFacts — which every cube cell and leaf row routes through —
// admitted a fact only when it carried run outcomes or verification weight,
// so a coordinate whose only evidence is USED presence merged to a zero
// aggregate and rendered as "—, never measured" throughout the cube.
func TestUsageOnlyFactIsNotAnEmptyCubeCell(t *testing.T) {
	facts := []cubeFact{{
		Dims:    map[string]string{"version": "1.0.0", "os": "linux"},
		Agg:     pivotAgg{used: 229},
		EnvHash: "env1",
	}}
	g := buildCubeGrid(facts, "version", "os", pivotLinks{}, pivotNow, true)
	cell := cellAt(t, g, "linux", "1.0.0")
	if cell.Class == "empty" {
		t.Error("a package seen in real projects is not an empty cell")
	}
	if !strings.Contains(cell.Tip, "229 usage records") {
		t.Errorf("tip = %q, want the usage stated", cell.Tip)
	}
	// Presence still has no rate — the other half of the invariant.
	if cell.Ratio != "—" {
		t.Errorf("ratio = %q, want no rate: nothing was run", cell.Ratio)
	}
}

// Colour is a claim. A cell whose only evidence is "the package was there"
// was painted the same green as a cell where a hundred runs passed, because
// zero failures out of zero runs satisfies "no failures" — an outcome colour
// on a cell that has no outcome.
func TestUsageOnlyCellCarriesNoOutcomeColour(t *testing.T) {
	rows := []snapshotRow{pvRow("linux", "", "node", "22", "2026-08-19T00:00:00Z",
		map[string]stageCount{"USED": {Pass: 229}})}
	cell := cellAt(t, buildPivot(rows, osRowKey, contextColKey, nil, pivotNow), "linux", "node 22")
	if cell.Tone != "" {
		t.Errorf("tone = %q, want none: nothing ran, so nothing passed", cell.Tone)
	}

	ran := []snapshotRow{pvRow("linux", "", "node", "22", "2026-08-19T00:00:00Z",
		map[string]stageCount{"PROJECT_TEST": {Pass: 5}, "USED": {Pass: 229}})}
	if got := cellAt(t, buildPivot(ran, osRowKey, contextColKey, nil, pivotNow), "linux", "node 22"); got.Tone != "pass" {
		t.Errorf("tone = %q, want pass: five runs passed and none failed", got.Tone)
	}
}
