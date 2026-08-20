package web

import "testing"

func factAt(version, symbol, runtime string) cubeFact {
	return cubeFact{
		Dims: map[string]string{"version": version, "symbol": symbol, "runtime": runtime},
		Agg:  pivotAgg{obsPass: 1, obsPeers: 1},
	}
}

// A dimension with one value is already decided. The reader is not choosing
// between alternatives there — there are none — so making them pin it before
// the page will say anything is asking for a click that changes nothing.
func TestASingleValuedDimensionCountsAsChosen(t *testing.T) {
	coord := cubeCoord([]cubeFact{factAt("2.0.4", "hasOwn", "node 22")}, map[string]string{})
	if coord["version"] != "2.0.4" || coord["symbol"] != "hasOwn" || coord["runtime"] != "node 22" {
		t.Fatalf("coord = %v, want every dimension decided without a pin", coord)
	}
}

// While a dimension still offers a choice, the coordinate is not decided and
// the page has nothing coordinate-specific it can honestly show.
func TestTheCoordinateIsUndecidedWhileAnyDimensionVaries(t *testing.T) {
	facts := []cubeFact{
		factAt("2.0.4", "hasOwn", "node 22"),
		factAt("2.0.3", "hasOwn", "node 22"),
	}
	if cubeCoordDecided(facts) {
		t.Error("two versions is a choice, not a coordinate")
	}
	if coord := cubeCoord(facts, map[string]string{}); coord["version"] != "" {
		t.Errorf("coord picked version %q out of two", coord["version"])
	}
	if !cubeCoordDecided(facts[:1]) {
		t.Error("one fact is a decided coordinate")
	}
}

// A pin decides its dimension even where the evidence still holds others.
func TestAPinDecidesItsDimension(t *testing.T) {
	facts := []cubeFact{factAt("2.0.4", "hasOwn", "node 22")}
	coord := cubeCoord(facts, map[string]string{"version": "2.0.4"})
	if coord["version"] != "2.0.4" {
		t.Errorf("coord = %v", coord)
	}
}
