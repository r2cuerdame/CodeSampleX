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
// at a time. The pins sit beside the trail, each with its own way off.
func TestThePinsAreVisibleAndRemovableOneAtATime(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios?f_version=1.12.0&f_os=linux").Body.String()

	i := strings.Index(body, `class="pins`)
	if i < 0 {
		t.Fatal("nothing on the page says what is pinned")
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
