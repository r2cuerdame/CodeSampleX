package web

import (
	"testing"
	"time"
)

// Pinning a version opens something this page does not show — the release's
// own dependency list — so the version axis keeps its link however small the
// grid is. Pinning anything else on a one-cell grid narrows to the slice
// already on screen and shows nothing new, which is what the symbol row was
// doing: a link that arrives where you started.
func TestOnAOneCellGridOnlyTheVersionAxisLinks(t *testing.T) {
	facts := []cubeFact{{
		Dims: map[string]string{"version": "2.0.4", "symbol": "hasOwn"},
		Agg:  pivotAgg{verPass: 1},
	}}
	links := pivotLinks{
		Cell: func(r, c string) string { return "/cell" },
		Row:  func(r string) string { return "/row" },
		Col:  func(c string) string { return "/col" },
	}
	// symbol down the side, version across: the version header still links.
	g := buildCubeGrid(facts, "version", "symbol", links, time.Now(), false)
	if len(g.Rows) != 1 || len(g.Cols) != 1 {
		t.Fatalf("fixture is %dx%d, want 1x1", len(g.Rows), len(g.Cols))
	}
	if g.Cols[0].Href == "" {
		t.Error("the version axis lost its link; pinning it opens the dependency list")
	}
	if g.Rows[0].Href != "" {
		t.Error("the symbol row still links, and pinning it shows nothing new")
	}
	if g.Rows[0].Cells[0].Href != "" {
		t.Error("the only cell still links to the slice already on screen")
	}
}

func TestAGridWithSomewhereToGoKeepsItsLinks(t *testing.T) {
	rows := []snapshotRow{
		pvRow("linux", "", "node", "22", "2026-08-19T00:00:00Z", map[string]stageCount{"PROJECT_TEST": {Pass: 3}}),
		pvRow("windows", "", "node", "20", "2026-08-19T00:00:00Z", map[string]stageCount{"PROJECT_TEST": {Pass: 1}}),
	}
	links := pivotLinks{
		Cell: func(r, c string) string { return "/cell" },
		Row:  func(r string) string { return "/row" },
		Col:  func(c string) string { return "/col" },
	}
	g := assembleGridFor(rows, links)
	if g.Rows[0].Href == "" {
		t.Error("a grid with two rows lost its row links")
	}
}

// assembleGridFor builds a pivot with real links attached, which buildPivot's
// cellHref-only signature cannot express.
func assembleGridFor(rows []snapshotRow, links pivotLinks) pivotGrid {
	aggs := map[cellKey]*pivotAgg{}
	for _, r := range rows {
		rk, ck := osRowKey(r), contextColKey(r)
		key := cellKey{rk, ck}
		if aggs[key] == nil {
			aggs[key] = &pivotAgg{}
		}
		aggs[key].absorbRow(r)
	}
	return assembleGrid(aggs, sortPivotRows, sortPivotCols, true, false, links, time.Now(), "")
}
