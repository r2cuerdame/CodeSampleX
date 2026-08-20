package web

import (
	"testing"
	"time"
)

// A grid with one row and one column has nothing left to narrow. Pinning its
// row, its column or its single cell all produce the slice already on screen,
// so every link on it is an invitation to arrive where you started.
//
// The symbol page already refuses to draw a 1x1 summary above the detail it
// repeats. This is the same fact one layer down: a link that cannot narrow
// anything is not a link.
func TestASingleCellGridOffersNoDrillDown(t *testing.T) {
	rows := []snapshotRow{pvRow("linux", "", "node", "22", "2026-08-19T00:00:00Z",
		map[string]stageCount{"PROJECT_TEST": {Pass: 3}})}
	links := pivotLinks{
		Cell: func(r, c string) string { return "/cell" },
		Row:  func(r string) string { return "/row" },
		Col:  func(c string) string { return "/col" },
	}
	one := assembleGridFor(rows, links)
	if len(one.Rows) != 1 || len(one.Cols) != 1 {
		t.Fatalf("fixture is %dx%d, want 1x1", len(one.Rows), len(one.Cols))
	}
	if one.Rows[0].Href != "" || one.Cols[0].Href != "" || one.Rows[0].Cells[0].Href != "" {
		t.Errorf("a 1x1 grid still links: row=%q col=%q cell=%q",
			one.Rows[0].Href, one.Cols[0].Href, one.Rows[0].Cells[0].Href)
	}
}

// A grid that can actually narrow keeps its links.
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
	return assembleGrid(aggs, sortPivotRows, sortPivotCols, true, false, links, time.Now())
}
