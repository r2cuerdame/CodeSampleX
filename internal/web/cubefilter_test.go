package web

import "testing"

// A dimension with one value used to be dropped from the control bar, on the
// reasoning that a dropdown whose only choice is "all" filters nothing.
//
// It filters nothing, but it SAYS something. hasown carries one OS, and with
// the control gone the bar could not distinguish "measured on windows only"
// from "OS never recorded" — the reader had to guess which grid they were
// looking at. It stays, fixed to the value that exists.
func TestAFilterWithOneValueStatesThatValue(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"version": "1.0.0", "os": "linux", "libc": "glibc"}, Agg: pivotAgg{obsPass: 1}},
		{Dims: map[string]string{"version": "2.0.0", "os": "linux", "libc": "glibc"}, Agg: pivotAgg{obsPass: 1}},
	}
	version, ok := cubeFilterFor(facts, "version", map[string]string{}, "en")
	if !ok || version.Fixed {
		t.Error("version varies across the slice; it is a choice, not a coordinate")
	}
	for _, dim := range []string{"os", "libc"} {
		sel, ok := cubeFilterFor(facts, dim, map[string]string{}, "en")
		if !ok {
			t.Errorf("%s vanished, so the bar cannot say what it was measured on", dim)
			continue
		}
		if !sel.Fixed || len(sel.Options) != 1 {
			t.Errorf("%s = %+v, want one value already chosen", dim, sel.Options)
		}
	}
}

// A dimension the reader pinned themselves keeps its "all" option even when
// the pin leaves one value standing, or they cannot get back out.
func TestAPinnedFilterCanAlwaysBeCleared(t *testing.T) {
	facts := []cubeFact{
		{Dims: map[string]string{"version": "1.0.0", "os": "linux"}, Agg: pivotAgg{obsPass: 1}},
	}
	sel, ok := cubeFilterFor(facts, "os", map[string]string{"os": "linux"}, "en")
	if !ok {
		t.Fatal("a pinned filter vanished, so it cannot be cleared")
	}
	if sel.Fixed || sel.Options[0].Value != "" {
		t.Errorf("pinned control = %+v, want a clearable one", sel.Options)
	}
}

// A dimension nothing recorded has no control at all: an empty dropdown is
// not information, and every package would carry one for every dimension it
// never touched.
func TestAnUnrecordedDimensionGetsNoControl(t *testing.T) {
	facts := []cubeFact{{Dims: map[string]string{"version": "1.0.0"}, Agg: pivotAgg{obsPass: 1}}}
	if _, ok := cubeFilterFor(facts, "libc", map[string]string{}, "en"); ok {
		t.Error("libc was never recorded and still got a control")
	}
}
