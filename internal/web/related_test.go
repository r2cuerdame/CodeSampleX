package web

import (
	"strings"
	"testing"
)

// R2C-135. Measured on the live site: of the twenty ordered pairs among
// Findings, Samples, Compatibility, Gaps and the dependency atlas, exactly one
// had a link in the page body — findings to samples. Everything else was
// reachable only through the header, and two of the five are deliberately not
// in the header, so a reader landing on /gaps from a search result had no
// route to any of the others.
func TestEveryCollectionLeadsOnToAnother(t *testing.T) {
	mux := newTestMuxOnly(t)
	for _, tc := range []struct {
		path  string
		wants []string
	}{
		{"/findings", []string{"/samples", "/gaps"}},
		{"/samples", []string{"/findings", "/gaps"}},
		{"/compatibility", []string{"/findings", "/gaps"}},
		{"/gaps", []string{"/compatibility", "/samples"}},
		{"/dependencies", []string{"/compatibility", "/gaps"}},
	} {
		body := get(t, mux, tc.path+"?lang=en").Body.String()
		nav := body
		if i := strings.Index(body, `class="related"`); i >= 0 {
			nav = body[i:]
		} else {
			t.Errorf("%s has no way out to another collection", tc.path)
			continue
		}
		for _, want := range tc.wants {
			if !strings.Contains(nav, `href="`+want+`"`) {
				t.Errorf("%s does not lead to %s", tc.path, want)
			}
		}
		// It says what the reader would find there. A bare row of links is
		// a second navigation; the question is what makes it worth following.
		if !strings.Contains(nav, "dim small") {
			t.Errorf("%s links on without saying what they answer", tc.path)
		}
	}
}

// A page that is not a collection gets none of this.
//
// It is the last thing on a collection, not site chrome: putting it on every
// package page would be a second navigation on the page whose whole job is to
// answer one question.
func TestAPackagePageCarriesNoCollectionTrail(t *testing.T) {
	mux := newTestMuxOnly(t)
	if body := get(t, mux, "/npm/axios?lang=en").Body.String(); strings.Contains(body, `class="related"`) {
		t.Error("a package page carries the collection trail")
	}
}

// The navigation is ordered by what a reader gets.
//
// R2C-135 sets the order: what was found out, then what can be taken away,
// then the evidence underneath both. Compatibility used to sit second, ahead
// of the findings it exists to support.
func TestTheNavigationLeadsWithFindings(t *testing.T) {
	body := get(t, newTestMuxOnly(t), "/findings?lang=en").Body.String()
	nav := body[strings.Index(body, `<nav class="nav">`):]
	nav = nav[:strings.Index(nav, "</nav>")]

	order := []string{`href="/findings"`, `href="/samples"`, `href="/compatibility"`}
	at := -1
	for _, want := range order {
		i := strings.Index(nav, want)
		if i < 0 {
			t.Fatalf("%s is not in the navigation", want)
		}
		if i < at {
			t.Errorf("%s comes before the entry that should precede it", want)
		}
		at = i
	}
}
