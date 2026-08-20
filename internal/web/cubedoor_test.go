package web

import "testing"

func hasownFacts() []cubeFact {
	// hasown as production has it: one symbol, one runtime, but TWO versions.
	// The chosen axes are already down to one row and one column while the
	// version is still undecided.
	out := []cubeFact{}
	for _, v := range []string{"2.0.4", "2.0.3"} {
		out = append(out, cubeFact{
			Dims: map[string]string{"symbol": "hasOwn", "runtime": "node 22", "version": v},
			Agg:  pivotAgg{obsPass: 1, obsPeers: 1},
		})
	}
	return out
}

// The package page is a hub: the grid is the way in, and the cell is the
// door. A one-row one-column grid used to lose that door on the reasoning
// that pinning both axes shows nothing new — but the version was still
// unpinned, so pinning them showed a version axis the reader had not seen.
//
// With the door gone the reader was stranded on the hub, which is why the
// page had to spread the coordinate's own facts across it and read as a
// confusing pile.
func TestTheCellStaysADoorWhileAnythingIsStillUndecided(t *testing.T) {
	g := buildCubeGrid(hasownFacts(), "runtime", "symbol", pivotLinks{
		Cell: func(row, col string) string { return "/npm/hasown?f_symbol=" + row },
	}, pivotNow, false)
	if len(g.Rows) != 1 || len(g.Rows[0].Cells) != 1 {
		t.Fatalf("grid is %dx%d, want the 1x1 this test is about", len(g.Rows), len(g.Rows[0].Cells))
	}
	if g.Rows[0].Cells[0].Href == "" {
		t.Error("the cell has no link: the reader cannot reach the coordinate")
	}
}

// When nothing is left to decide, the cell would link to the page it is on.
func TestTheCellStopsBeingADoorOnceEverythingIsDecided(t *testing.T) {
	facts := []cubeFact{{
		Dims: map[string]string{"symbol": "hasOwn", "runtime": "node 22", "version": "2.0.4"},
		Agg:  pivotAgg{obsPass: 1, obsPeers: 1},
	}}
	g := buildCubeGrid(facts, "runtime", "symbol", pivotLinks{
		Cell: func(row, col string) string { return "/npm/hasown?f_symbol=" + row },
	}, pivotNow, false)
	if g.Rows[0].Cells[0].Href != "" {
		t.Errorf("cell links to %q, but there is nothing further to pin", g.Rows[0].Cells[0].Href)
	}
}

// On a one-cell grid the cell pins BOTH axes and the row header pins one of
// them — into a slice whose other axis has a single value anyway, so both
// land in the same place. Two links to one destination read as two choices,
// and the row label ends up looking like a link that goes nowhere new.
//
// The version header is the exception, and the reason this is not a blanket
// rule: a version is a thing a reader wants to hold on its own.
func TestOnOneCellOnlyTheCellAndTheVersionAreLinks(t *testing.T) {
	// One version, one symbol, two runtimes: a genuine one-cell grid with
	// something left to pin.
	facts := []cubeFact{
		{Dims: map[string]string{"version": "2.0.4", "symbol": "hasOwn", "runtime": "node 22"},
			Agg: pivotAgg{obsPass: 1, obsPeers: 1}},
		{Dims: map[string]string{"version": "2.0.4", "symbol": "hasOwn", "runtime": "node 24"},
			Agg: pivotAgg{obsPass: 1, obsPeers: 1}},
	}
	g := buildCubeGrid(facts, "version", "symbol", pivotLinks{
		Cell: func(row, col string) string { return "/cell" },
		Row:  func(row string) string { return "/row" },
		Col:  func(col string) string { return "/col" },
	}, pivotNow, false)
	if len(g.Rows) != 1 || len(g.Cols) != 1 {
		t.Fatalf("grid is %dx%d, want the one-cell grid this test is about", len(g.Rows), len(g.Cols))
	}
	if g.Rows[0].Href != "" {
		t.Errorf("the symbol row links to %q, where the cell already goes", g.Rows[0].Href)
	}
	if g.Cols[0].Href == "" {
		t.Error("the version header lost its link")
	}
	if g.Rows[0].Cells[0].Href == "" {
		t.Error("the cell is the door and has no link")
	}
}

// A version measured only at package level has no cell on a symbol axis. The
// column used to vanish with it, so hasown — two versions, one of them symbol
// grain — spread version by symbol and offered exactly one version to pick.
// The column stays, empty: nothing measured AT THIS GRAIN is not nothing.
func TestAVersionWithoutSymbolEvidenceKeepsItsColumn(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"version": "2.0.4", "symbol": "hasOwn"},
			Agg: pivotAgg{obsPass: 1, obsPeers: 1}},
		{Dims: map[string]string{"version": "2.0.3", "symbol": cubePackageLevel},
			PackageLevel: true, Agg: pivotAgg{obsPass: 1, obsPeers: 1}},
	}
	g := buildCubeGrid(facts, "version", "symbol", pivotLinks{}, pivotNow, false)
	var cols []string
	for _, c := range g.Cols {
		cols = append(cols, c.Label)
	}
	if len(cols) != 2 {
		t.Errorf("columns = %v, want both versions the package has", cols)
	}
}
