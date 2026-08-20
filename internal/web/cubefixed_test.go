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

// A package-level fact is dropped from a symbol-axis GRID, but it still
// carries a real version, OS and package manager. Building the bar from what
// the grid renders took hasown 2.0.3 — measured only at package level — out
// of the version list entirely, so the reader could not select a version the
// package has.
func TestTheBarOffersValuesTheSymbolAxisDropped(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"symbol": "hasOwn", "version": "2.0.4", "tool": "npm 11"},
			Agg: pivotAgg{obsPass: 1}},
		{Dims: map[string]string{"symbol": cubePackageLevel, "version": "2.0.3", "tool": "npm 10"},
			PackageLevel: true, Agg: pivotAgg{obsPass: 1}},
	}
	sel, ok := cubeFilterFor(facts, "version", map[string]string{}, "en")
	if !ok {
		t.Fatal("no version control")
	}
	var got []string
	for _, o := range sel.Options {
		if o.Value != "" {
			got = append(got, o.Value)
		}
	}
	if len(got) != 2 {
		t.Errorf("versions offered = %v, want both the package has", got)
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

// The OS control offers whole platforms as well as exact environments, so a
// slice measured only on alpine still renders two entries — "linux" and
// "alpine musl". Counting entries called that a choice and left the control
// asking the reader to pick between a group and its only member.
func TestOneOSIsDecidedEvenThoughItOffersItsPlatform(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"os": "alpine musl", "version": "2.0.4"}, Agg: pivotAgg{obsPass: 1}},
		{Dims: map[string]string{"os": "alpine musl", "version": "2.0.3"}, Agg: pivotAgg{obsPass: 1}},
	}
	sel, ok := cubeFilterFor(facts, "os", map[string]string{}, "en")
	if !ok {
		t.Fatal("no OS control")
	}
	if !sel.Fixed {
		t.Fatalf("OS = %+v, want it fixed to the one environment measured", sel.Options)
	}
	if len(sel.Options) != 1 || sel.Options[0].Value != "alpine musl" {
		t.Errorf("options = %+v, want just alpine musl", sel.Options)
	}
}
