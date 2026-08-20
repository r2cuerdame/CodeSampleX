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
	}, pivotNow)
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
	}, pivotNow)
	if g.Rows[0].Cells[0].Href != "" {
		t.Errorf("cell links to %q, but there is nothing further to pin", g.Rows[0].Cells[0].Href)
	}
}
