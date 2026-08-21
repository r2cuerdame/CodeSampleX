package web

import "testing"

// A pinned dimension's dropdown was built from the slice WITHOUT its own pin,
// so it advertised every value the pin had excluded. On a decided coordinate
// that left one control looking live — five OS entries beside an exact record
// — while nothing about the slice could still be chosen. It is why the rule
// for reaching the exact records was unreadable: the bar said "keep going"
// where there was nowhere to go.
//
// Every control reads the slice the reader is actually standing in. A live
// control now means exactly one thing: this dimension still holds a choice.
// The pin beside the bar is how you leave a coordinate you chose.
func TestOnlyADimensionThatStillHoldsAChoiceIsLive(t *testing.T) {
	// Inside windows 11 the runtime still varies and the version does not.
	facts := []cubeFact{
		{Dims: map[string]string{"os": "windows 11", "runtime": "node 24", "version": "2.9.0"},
			Agg: pivotAgg{obsPass: 1}},
		{Dims: map[string]string{"os": "windows 11", "runtime": "node 22", "version": "2.9.0"},
			Agg: pivotAgg{obsPass: 1}},
		{Dims: map[string]string{"os": "alpine musl", "runtime": "node 24", "version": "2.8.2"},
			Agg: pivotAgg{obsPass: 1}},
	}
	pinned := map[string]string{"os": "windows 11"}

	os, ok := cubeFilterFor(facts, "os", pinned, "en")
	if !ok {
		t.Fatal("the pinned dimension lost its control")
	}
	if !os.Fixed {
		t.Errorf("os = %+v, want it stated: inside this pin there is one OS", os.Options)
	}
	// The dimension that genuinely still varies inside the pin stays live.
	if sel, ok := cubeFilterFor(facts, "runtime", pinned, "en"); !ok || sel.Fixed {
		t.Errorf("runtime = %+v, want it live: windows 11 holds two runtimes", sel.Options)
	}
	// The one the pin settled reads as settled.
	if sel, ok := cubeFilterFor(facts, "version", pinned, "en"); !ok || !sel.Fixed {
		t.Errorf("version = %+v, want it stated: windows 11 holds one version", sel.Options)
	}
}

// The bar and the grid must agree: a live control means a grid, and no live
// control means the exact records. They were computed from different slices.
func TestALiveControlAndAGridAgree(t *testing.T) {
	decided := []cubeFact{
		{Dims: map[string]string{"os": "windows 11", "runtime": "node 24", "version": "2.9.0"},
			Agg: pivotAgg{obsPass: 1}},
	}
	bar := cubeFilterBar(decided, "", "", map[string]string{"os": "windows 11"}, "en")
	for _, sel := range bar {
		if !sel.Fixed {
			t.Errorf("%s is live on a slice where nothing varies: %+v", sel.Dim, sel.Options)
		}
	}
	if !cubeCoordDecided(decided) {
		t.Error("the slice says undecided while every control says decided")
	}
}
