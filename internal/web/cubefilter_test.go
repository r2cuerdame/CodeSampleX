package web

import (
	"strings"
	"testing"
)

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

// A pinned dimension no longer carries its own way out. Its dropdown reads
// the slice the reader is standing in, so inside the pin there is one value
// and the control states it; the pin beside the bar is what removes it.
//
// The way out must still exist, and it must drop only that one pin.
func TestAPinnedDimensionStillHasAWayOut(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios?f_version=1.12.0&f_os=linux").Body.String()
	for _, want := range []string{"f_os=linux", "f_version=1.12.0"} {
		if !strings.Contains(body, want) {
			t.Errorf("no pin carries %s, so it cannot be removed on its own", want)
		}
	}
	if !strings.Contains(body, `class="pinoff"`) {
		t.Error("a pin with no way off it")
	}
}

// A pin the reader placed must survive the form's next submit. A pinned
// dimension renders as a decided, DISABLED select — and a disabled control is
// never submitted — so changing any live dropdown rebuilt the URL without
// f_os/f_version: the grid reset, the chips vanished, and the decided-
// coordinate sections disappeared. The pin travels as a hidden input, which
// disabled controls cannot be.
func TestAReaderPlacedPinSurvivesTheNextSubmit(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	body := get(t, mux, "/npm/reactish?f_os=linux").Body.String()
	// The scenario needs a live dropdown left to change — that change is
	// what submits the form.
	mustContain(t, body, `<select name="f_symbol" data-autosubmit>`)
	mustContain(t, body, `<input type="hidden" name="f_os" value="linux">`)
	// A dimension the evidence decided WITHOUT a pin must not grow one: the
	// reader never placed it, and submitting must not invent it.
	mustNotContain(t, body, `<input type="hidden" name="f_version"`)
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
