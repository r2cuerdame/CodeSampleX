package web

import "testing"

// A dimension with one value is not a choice, and the control bar used to
// drop it entirely — so a reader standing on windows saw no OS control at
// all and could not tell whether the grid covered every OS or one.
//
// It renders as that value, already selected, with no "all" to pick: the
// bar states the coordinate instead of offering a decision that isn't one.
func TestASingleValuedDimensionRendersAsAlreadyChosen(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"os": "windows", "version": "2.0.4"}, Agg: pivotAgg{obsPass: 1}},
		{Dims: map[string]string{"os": "windows", "version": "2.0.3"}, Agg: pivotAgg{obsPass: 1}},
	}
	sel, ok := cubeFilterFor(facts, "os", map[string]string{}, "en")
	if !ok {
		t.Fatal("the only OS there is got no control")
	}
	if len(sel.Options) != 1 {
		t.Fatalf("options = %+v, want just the one value that exists", sel.Options)
	}
	if sel.Options[0].Value != "windows" || !sel.Options[0].Selected {
		t.Errorf("option = %+v, want windows already selected", sel.Options[0])
	}
	if !sel.Fixed {
		t.Error("a control with no alternative should not read as a choice")
	}
}

// Where there IS a choice, "all" stays: it is how the reader gets back out.
func TestADimensionWithAChoiceKeepsAll(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"os": "windows"}, Agg: pivotAgg{obsPass: 1}},
		{Dims: map[string]string{"os": "linux"}, Agg: pivotAgg{obsPass: 1}},
	}
	sel, ok := cubeFilterFor(facts, "os", map[string]string{}, "en")
	if !ok {
		t.Fatal("no control for a dimension with two values")
	}
	if sel.Fixed {
		t.Error("two values is a choice")
	}
	if sel.Options[0].Value != "" {
		t.Errorf("first option = %+v, want the all option", sel.Options[0])
	}
}

// Every option a control offers must lead somewhere. An option that pins to
// an empty grid is a dead end the reader can only discover by clicking it,
// and it makes the bar read as a list of what MIGHT exist rather than a list
// of what does.
func TestEveryOfferedOptionLeadsToEvidence(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"version": "2.0.4", "os": "alpine musl", "libc": "musl"}, Agg: pivotAgg{obsPass: 1}},
		{Dims: map[string]string{"version": "2.0.3", "os": "windows 11", "libc": ""}, Agg: pivotAgg{obsPass: 1}},
	}
	for _, dim := range cubeDimKeys {
		sel, ok := cubeFilterFor(facts, dim, map[string]string{}, "en")
		if !ok {
			continue
		}
		for _, opt := range sel.Options {
			if opt.Value == "" {
				continue // the "all" escape hatch
			}
			if len(filterCubeFacts(facts, map[string]string{dim: opt.Value})) == 0 {
				t.Errorf("%s offers %q, which matches nothing", dim, opt.Value)
			}
		}
	}
}

// The control bar must describe the grid beside it. A symbol axis drops the
// package-level aggregate — it is the total OVER the symbols, not one of
// them — and any dimension whose only evidence was package-level disappears
// from the grid with it. Built from the raw slice, the bar went on offering
// those values, so hasown showed one runtime column under a runtime control
// that listed two.
func TestTheControlBarDescribesTheGridBesideIt(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"symbol": "hasOwn", "runtime": "node 22", "tool": "npm 11"},
			Agg: pivotAgg{obsPass: 1}},
		{Dims: map[string]string{"symbol": cubePackageLevel, "runtime": "node 24", "tool": "npm 10"},
			PackageLevel: true, Agg: pivotAgg{obsPass: 1}},
	}
	onAxes := cubeFactsOnAxes(facts, "runtime", "symbol")
	sel, ok := cubeFilterFor(onAxes, "tool", map[string]string{}, "en")
	if !ok {
		t.Fatal("no tool control")
	}
	for _, opt := range sel.Options {
		if opt.Value == "npm 10" {
			t.Error("the bar offers npm 10, whose only evidence the symbol axis drops")
		}
	}
}

// The bottom of the drill-down has no grid and therefore no axes. Requiring
// axis coordinates there dropped every fact, which left the reader at the
// coordinate they had worked to reach with no control to leave it by.
func TestTheBottomedOutViewKeepsItsEvidence(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"version": "2.0.4", "symbol": "hasOwn"}, Agg: pivotAgg{obsPass: 1}},
	}
	if got := cubeFactsOnAxes(facts, "", ""); len(got) != 1 {
		t.Fatalf("facts with no axes = %d, want all of them", len(got))
	}
	bar := cubeFilterBar(facts, "", "", map[string]string{"version": "2.0.4"}, "en")
	var sawVersion bool
	for _, sel := range bar {
		if sel.Dim == "version" {
			sawVersion = true
		}
	}
	if !sawVersion {
		t.Error("the pinned version has no control at the bottom: the pin cannot be cleared")
	}
}
