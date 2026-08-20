package web

import "testing"

func symFact(symbol, version string, pass, fail int64, pkgLevel bool) cubeFact {
	return cubeFact{
		Dims:         map[string]string{"symbol": symbol, "version": version},
		EnvHash:      "e1",
		PackageLevel: pkgLevel,
		Agg:          pivotAgg{obsPass: pass, obsFail: fail, obsPeers: 1},
	}
}

// yaml is measured at symbol grain on alpine and at package grain on windows,
// where all 42 of its failure clusters were recorded. Spreading by symbol
// dropped the package-level aggregate, so the front door of a compatibility
// site rendered six green rows for a package with 42 recorded failures.
//
// The aggregate is not one of the symbols and never was — it is the total
// over them. It belongs on the table as a total: set apart, marked, and
// outside the tallies that describe the symbol rows.
func TestTheSymbolAxisShowsThePackageTotal(t *testing.T) {
	facts := []cubeFact{
		symFact("Alias", "2.9.0", 3, 0, false),
		symFact("Document", "2.9.0", 3, 0, false),
		symFact(cubePackageLevel, "2.9.0", 1, 9, true),
	}
	g := buildCubeGrid(facts, "version", "symbol", pivotLinks{}, pivotNow, true)
	if len(g.Rows) != 3 {
		t.Fatalf("rows = %d, want the two symbols and the package total", len(g.Rows))
	}
	total := g.Rows[0]
	if total.Label != cubePackageLevel {
		t.Errorf("first row = %q, want the total set apart from its own parts", total.Label)
	}
	if !total.Aggregate {
		t.Error("the total is not marked, so it reads as one more symbol")
	}
	// Two symbol cells are counted; the total beside them is not, or every
	// observation would be counted twice — once in its symbol row and again
	// in the row that sums them.
	if g.CountObserved != 2 || g.Measured != 2 {
		t.Errorf("tallies = %d observed, %d measured; want only the symbol rows",
			g.CountObserved, g.Measured)
	}
}

// A failure recorded against the package is still a failure the reader must
// see. It was invisible at symbol grain, which is the grain the page opens on.
func TestAPackageLevelFailureIsVisibleOnASymbolAxis(t *testing.T) {
	facts := []cubeFact{
		symFact("Alias", "2.9.0", 3, 0, false),
		symFact("Document", "2.9.0", 3, 0, false),
		symFact(cubePackageLevel, "2.9.0", 1, 9, true),
	}
	g := buildCubeGrid(facts, "version", "symbol", pivotLinks{}, pivotNow, true)
	total := g.Rows[0]
	if total.Cells[0].FailCount == 0 {
		t.Errorf("the package total shows no failures: %+v", total.Cells[0])
	}
}
