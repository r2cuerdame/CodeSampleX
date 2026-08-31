package web

import (
	"strings"
	"testing"
)

// R2C-108. The dependency table answers "what shipped with THIS release". The
// question a reader arrives with when a build breaks after an upgrade is the
// other one: which child moved between the release that worked and the one
// that does not.
//
// Measured on production: 453 (parent, child) pairs across 174 packages
// resolve to different child versions at different releases of the parent.
// Nothing on the site could show one of them.
func TestTheMatrixShowsWhichChildMovedBetweenReleases(t *testing.T) {
	m := buildDependencyMatrix([]DependencyEdge{
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "debug", ChildVersion: "4.4.1"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "debug", ChildVersion: "4.4.3"},
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "cookie", ChildVersion: "0.7.2"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "cookie", ChildVersion: "0.7.2"},
	})
	if m == nil {
		t.Fatal("no matrix built from two releases")
	}
	if len(m.Versions) != 2 || m.Versions[0] != "2.0.0" || m.Versions[1] != "1.0.0" {
		t.Fatalf("versions = %v, want newest first", m.Versions)
	}
	// A child that moved leads: it is the only row that answers the question.
	if len(m.Rows) == 0 || m.Rows[0].Child != "debug" || !m.Rows[0].Moves {
		t.Fatalf("first row = %+v, want the child that moved", m.Rows)
	}
	if m.Rows[0].Cells[0].Version != "4.4.3" || m.Rows[0].Cells[1].Version != "4.4.1" {
		t.Errorf("cells = %+v, want the version resolved at each release", m.Rows[0].Cells)
	}
	// A child that did not move is still there, and says so by not being
	// marked: hiding it would make the table look like the whole tree changed.
	var steady *dependencyMatrixRow
	for i := range m.Rows {
		if m.Rows[i].Child == "cookie" {
			steady = &m.Rows[i]
		}
	}
	if steady == nil || steady.Moves {
		t.Errorf("the unchanged child is missing or marked as moved: %+v", m.Rows)
	}
}

// A combination nobody resolved is blank, and blank means unresolved.
//
// The cell cannot say "this release does not depend on it": an absent edge is
// an absent measurement, and a resolver that never ran at that release says
// nothing about what it would have found.
func TestAnUnresolvedCombinationIsBlank(t *testing.T) {
	m := buildDependencyMatrix([]DependencyEdge{
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "only-old", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "shared", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "shared", ChildVersion: "1.0.0"},
	})
	if m == nil {
		t.Fatal("no matrix built")
	}
	var row *dependencyMatrixRow
	for i := range m.Rows {
		if m.Rows[i].Child == "only-old" {
			row = &m.Rows[i]
		}
	}
	if row == nil {
		t.Fatal("a child resolved at only one release is missing")
	}
	// Versions are newest first, so 2.0.0 is cell 0 and never resolved it.
	if row.Cells[0].Version != "" {
		t.Errorf("an unresolved combination claims %q", row.Cells[0].Version)
	}
	if row.Cells[1].Version != "1.0.0" {
		t.Errorf("the resolved combination is %q, want 1.0.0", row.Cells[1].Version)
	}
}

// One release is not a matrix.
//
// With a single version every row is the table already above it, and a
// one-column grid dressed as a comparison invites a reader to see a trend in
// one point.
func TestOneReleaseBuildsNoMatrix(t *testing.T) {
	if m := buildDependencyMatrix([]DependencyEdge{
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "debug", ChildVersion: "4.4.1"},
	}); m != nil {
		t.Errorf("a single release produced a matrix: %+v", m)
	}
}

// The comparison does not need a release pinned.
//
// Picking one release is exactly what the matrix does not do, so the rule that
// keeps the single-release table behind a pin has no purchase here -- and the
// reader who most needs it is the one who has just landed on the package and
// not yet chosen anything.
func TestTheMatrixDoesNotWaitForAPinnedRelease(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.dependencies = []DependencyEdge{
		{ParentVersion: "2.0.4", ChildName: "function-bind", ChildVersion: "1.1.2"},
		{ParentVersion: "2.0.3", ChildName: "function-bind", ChildVersion: "1.1.1"},
	}
	body := mustGet(t, mux, "/npm/axios?lang=en")
	if !strings.Contains(body, `id="depmatrix"`) {
		t.Fatal("the unpinned page has no cross-release comparison")
	}
	if !strings.Contains(body, "1.1.2") || !strings.Contains(body, "1.1.1") {
		t.Error("both releases' resolved versions are not shown")
	}
	// And it says what an edge is. Two releases resolved side by side is not
	// a claim that they work together, and a grid of versions reads like one
	// unless the page says otherwise.
	if !strings.Contains(body, "not a claim that the two work together") {
		t.Error("the matrix does not say what an edge records")
	}
}
