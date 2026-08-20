package web

import "testing"

func envFact(env, symbol, version string, pkgLevel bool) cubeFact {
	return cubeFact{
		Dims:         map[string]string{"symbol": symbol, "version": version, "os": env},
		EnvHash:      env,
		PackageLevel: pkgLevel,
		Agg:          pivotAgg{obsPass: 1, obsPeers: 1},
	}
}

// A symbol axis drops the package-level aggregate, so an environment measured
// only at package level disappears from the grid entirely.
//
// hasown is that shape: one symbol on alpine, and every windows run recorded
// against the package. Defaulting to a symbol axis showed one green cell and
// hid windows — where every failure the package has actually happened.
//
// The symbol dimension counts "whole package" among its values, so one real
// symbol beside the aggregate read as two and qualified as an axis — and then
// the grid dropped the aggregate and rendered a single row. An axis has to
// spread; counting what survives the drop is the same rule every other
// dimension already gets.
func TestSymbolIsNotAnAxisWhenOnlyOneSymbolSurvives(t *testing.T) {
	facts := []cubeFact{
		envFact("alpine musl", "hasOwn", "2.0.4", false),
		envFact("windows 11", cubePackageLevel, "2.0.4", true),
		envFact("windows 11", cubePackageLevel, "2.0.3", true),
	}
	x, y, ok := defaultCubeAxes(facts, map[string]string{})
	if !ok {
		t.Fatal("no axes at all")
	}
	if x == "symbol" || y == "symbol" {
		t.Errorf("axes = %s x %s, but only one symbol survives a symbol axis", x, y)
	}
}

// Where every environment has symbol-grain evidence, the symbol axis hides
// nothing and stays the most useful thing to spread: it answers "which API".
func TestSymbolStaysTheDefaultWhenItHidesNothing(t *testing.T) {
	facts := []cubeFact{
		envFact("alpine musl", "hasOwn", "2.0.4", false),
		envFact("windows 11", "hasOwn", "2.0.4", false),
		envFact("windows 11", "isKey", "2.0.3", false),
	}
	x, y, ok := defaultCubeAxes(facts, map[string]string{})
	if !ok {
		t.Fatal("no axes at all")
	}
	if x != "symbol" && y != "symbol" {
		t.Errorf("axes = %s x %s, want the symbol axis where it costs nothing", x, y)
	}
}
