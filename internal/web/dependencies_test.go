package web

import (
	"net/http"
	"strings"
	"testing"
)

// atlasStore holds one small graph: body-parser@2.2.0 pulled by two different
// parent releases, raw-body@3.0.0 pulled by one.
func atlasStore(t *testing.T) *fakeStore {
	t.Helper()
	f := newFakeStore()
	f.dependencyEcosystem = "npm"
	f.dependencies = []DependencyEdge{
		{ParentName: "express", ParentVersion: "5.1.0", ChildName: "body-parser", ChildVersion: "2.2.0", Projects: 9},
		{ParentName: "koa", ParentVersion: "3.0.1", ChildName: "body-parser", ChildVersion: "2.2.0", Projects: 4},
		{ParentName: "express", ParentVersion: "5.1.0", ChildName: "raw-body", ChildVersion: "3.0.0", Projects: 2},
	}
	return f
}

// The atlas exists because the graph could only ever be entered from the
// parent's side.
//
// `Dependencies` answers "what did this package pull", which requires already
// knowing a parent, and the package page shows the same thing under "ships
// with". A reader arriving with "who pulls this release" — the question an
// upgrade actually raises — had nowhere to start, so 14,000 edges were
// reachable only through a package somebody already suspected.
func TestTheAtlasListsSubjectsWithBothCounts(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = atlasStore(t) })
	body := get(t, mux, "/dependencies").Body.String()

	for _, want := range []string{"body-parser@2.2.0", "raw-body@3.0.0"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s missing from the atlas", want)
		}
	}
	// Ranked by how widely each was resolved, so the most-pulled release is
	// the one a reader meets first.
	if i, j := strings.Index(body, "body-parser@2.2.0"), strings.Index(body, "raw-body@3.0.0"); i > j {
		t.Error("raw-body outranked body-parser; the list is not ordered by projects")
	}
	// Two parent RELEASES, not two names, and thirteen project-days.
	if !strings.Contains(body, "2 parent releases") {
		t.Error("the parent-release count is missing")
	}
	if !strings.Contains(body, "13 project-days") {
		t.Error("the project-day count is missing or not summed")
	}
}

// Selecting a subject answers the question the page exists for: who pulled it,
// and at which version.
func TestSelectingASubjectShowsWhoPulledIt(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = atlasStore(t) })
	body := get(t, mux, "/dependencies?eco=npm&name=body-parser&ver=2.2.0").Body.String()

	for _, want := range []string{"express@5.1.0", "koa@3.0.1"} {
		if !strings.Contains(body, want) {
			t.Errorf("parent %s missing", want)
		}
	}
	// Both ends at an exact version. "Something in express pulled something in
	// body-parser" is not a fact anybody can act on.
	if strings.Contains(body, ">express<") {
		t.Error("a parent is named without its version")
	}
	// And the parent that more projects saw comes first.
	if i, j := strings.Index(body, "express@5.1.0"), strings.Index(body, "koa@3.0.1"); i > j {
		t.Error("parents are not ordered by how widely they were observed")
	}
}

// A release nothing resolved onto says so, rather than rendering an empty box
// a reader has to interpret.
func TestASubjectWithNoParentsSaysSo(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = atlasStore(t) })
	body := get(t, mux, "/dependencies?eco=npm&name=body-parser&ver=9.9.9").Body.String()
	if !strings.Contains(body, "Nothing resolved onto this release") {
		t.Error("a release with no parents rendered no explanation")
	}
}

// The page must not let a reader infer compatibility from a row being there.
//
// An edge records that a resolver placed one release beside another. Whether
// they work together is answered by a contract that ran, and this network's
// whole claim is that it does not state what it did not measure.
func TestTheAtlasRefusesToImplyCompatibility(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = atlasStore(t) })
	body := get(t, mux, "/dependencies").Body.String()
	if !strings.Contains(body, "It is not a claim that the two work together") {
		t.Error("the page does not say that presence here is not a compatibility claim")
	}
}

// The search narrows by dependency name.
func TestTheAtlasSearchNarrowsTheList(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = atlasStore(t) })
	body := get(t, mux, "/dependencies?q=raw").Body.String()
	if !strings.Contains(body, "raw-body@3.0.0") {
		t.Error("the search dropped its own match")
	}
	if strings.Contains(body, "body-parser@2.2.0") {
		t.Error("the search kept a row that does not match")
	}
}

// An empty atlas explains why it is empty. "No edges" and "this ecosystem has
// no dependencies" are different facts, and only one of them is ever true.
func TestAnEmptyAtlasExplainsItself(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/dependencies").Body.String()
	if !strings.Contains(body, "No dependency edges match") {
		t.Error("an empty atlas rendered no message")
	}
	if !strings.Contains(body, "that is a gap rather than an absence of dependencies") {
		t.Error("an empty atlas does not distinguish a gap from an absence")
	}
}

// The atlas is reachable, and deliberately not in the primary navigation.
//
// A thousand rows of package coordinates is not something a reader arriving at
// the top of the site can act on. The dependency question is answered where it
// is asked -- at the end of a release's own page -- and the atlas is where a
// row there leads when the reader wants the other side of that edge.
//
// So it stays out of the nav and stays reachable. Both halves are pinned here
// because either one alone is wrong: in the nav it is clutter, unlinked it is
// a page nobody can find.
func TestTheAtlasIsReachableButNotFeatured(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()

	nav := body
	if i := strings.Index(nav, "</nav>"); i > 0 {
		nav = nav[:i]
	}
	if strings.Contains(nav, `href="/dependencies"`) {
		t.Error("the atlas is in the primary navigation")
	}
	if !strings.Contains(body, `href="/dependencies"`) {
		t.Error("nothing on the page links to the atlas at all")
	}
}

// And the place it is meant to be entered from: a dependency row on the
// release whose tree it belongs to.
func TestADependencyRowIsTheWayIntoTheAtlas(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = depsFixture(t) })
	body := get(t, mux, "/npm/app?f_version=1.0.0").Body.String()
	if !strings.Contains(body, "eco=npm&amp;name=proven&amp;ver=2.0.0") {
		t.Error("a dependency row does not lead into the atlas")
	}
}

// The trailing-slash form redirects rather than serving a second URL for one
// page, matching every other list on the site.
func TestTheAtlasHasOneURL(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/dependencies/")
	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/dependencies" {
		t.Errorf("Location = %q", loc)
	}
}
