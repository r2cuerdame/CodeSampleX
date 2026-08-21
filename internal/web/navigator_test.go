package web

import (
	"strings"
	"testing"
)

// Every page named itself twice: the trail said "records / npm / axios" and
// the heading under it said "axios · npm", so the reader read the same words
// in two sizes before reaching anything measured.
func TestAPageNamesItselfOnce(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, path := range []string{"/npm/axios", "/npm/axios/1.12.0", "/npm/axios/1.12.0/axios.post"} {
		body := get(t, mux, path).Body.String()
		main := body[strings.Index(body, "<main"):]
		if n := strings.Count(main, "<h1"); n != 1 {
			t.Errorf("%s has %d h1 elements, want exactly one", path, n)
		}
		if !strings.Contains(main, `class="crumbtitle`) {
			t.Errorf("%s does not use the trail as its title", path)
		}
	}
}

// The title still carries the package name, which is what a search result and
// a reader both need from a heading.
func TestTheTitleTrailStillNamesThePackage(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios/1.12.0").Body.String()
	i := strings.Index(body, `class="crumbtitle`)
	if i < 0 {
		t.Fatal("no title trail")
	}
	h1 := body[i : i+strings.Index(body[i:], "</h1>")]
	for _, want := range []string{"axios", "1.12.0", "npm"} {
		if !strings.Contains(h1, want) {
			t.Errorf("title trail is missing %q: %s", want, h1)
		}
	}
}

// Clicking a cell pins a dimension and nothing said so: the grid re-drew
// around a coordinate the reader could neither see nor step back out of one
// at a time. The pins lead the control bar, each with its own way off —
// picking a cell and picking a dropdown are the same act and belong in the
// same frame.
func TestThePinsAreVisibleAndRemovableOneAtATime(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios?f_version=1.12.0&f_os=linux").Body.String()

	i := strings.Index(body, `class="pins`)
	if i < 0 {
		t.Fatal("nothing on the page says what is pinned")
	}
	if bar := strings.Index(body, `class="cubecontrols`); bar < 0 || bar > i {
		t.Error("the pins sit outside the instrument they belong to")
	}
	pins := body[i : i+strings.Index(body[i:], "</div>")]
	if strings.Count(pins, `class="pin"`) != 2 {
		t.Errorf("pins = %s, want one per pinned dimension", pins)
	}
	// Removing one keeps the other: stepping back is per pin, not all-or-nothing.
	if !strings.Contains(pins, "f_os=linux") || !strings.Contains(pins, "f_version=1.12.0") {
		t.Errorf("a pin's remove link drops more than its own dimension: %s", pins)
	}
}

// Every link inside the instrument narrows the slice. The exact record's own
// heading linked OUT of it — to pages covering every environment of that
// release, wider than the coordinate the reader had just worked down to —
// wearing the same blue as the links that go deeper.
func TestNoLinkInsideTheInstrumentLeavesIt(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios?f_version=1.12.0").Body.String()

	i := strings.Index(body, `id="cube"`)
	if i < 0 {
		t.Skip("no cube on this fixture")
	}
	inst := body[i:]
	if j := strings.Index(inst, "</section>"); j >= 0 {
		inst = inst[:j]
	}
	for _, m := range regexpAllHrefs(inst) {
		if strings.HasPrefix(m, "/npm/axios/") {
			t.Errorf("a link inside the instrument jumps out of it: %s", m)
		}
	}
}

// The trail carries them instead, because a jump belongs in the navigator.
func TestTheTrailCarriesTheDecidedCoordinate(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios?f_version=1.12.0").Body.String()
	i := strings.Index(body, `class="crumbtitle`)
	if i < 0 {
		t.Fatal("no title trail")
	}
	trail := body[i : i+strings.Index(body[i:], "</h1>")]
	if !strings.Contains(trail, "1.12.0") {
		t.Errorf("the trail does not carry the decided release: %s", trail)
	}
}

func regexpAllHrefs(s string) []string {
	var out []string
	for {
		i := strings.Index(s, `href="`)
		if i < 0 {
			return out
		}
		s = s[i+6:]
		j := strings.Index(s, `"`)
		if j < 0 {
			return out
		}
		out = append(out, s[:j])
		s = s[j:]
	}
}
