package web

import (
	"strings"
	"testing"
)

// The question is not "who pulled this" but "what shipped WITH this":
// upgrade a library and its dependencies move under you, and the one that
// moved is usually the one that broke the build.
//
// Vertical the referenced library, horizontal the version — the shape asked
// for, and one a lockfile can actually fill.
func TestShipsWithIsALibraryByVersionGrid(t *testing.T) {
	grid := buildShipsWith([]DependencyEdge{
		{ParentVersion: "1.1.0", ChildName: "lib", ChildVersion: "3.0.0", Projects: 2},
		{ParentVersion: "1.0.0", ChildName: "lib", ChildVersion: "2.0.0", Projects: 1},
		{ParentVersion: "1.1.0", ChildName: "other", ChildVersion: "9.9.9", Projects: 1},
	})
	// Newest version first: the reader is asking what an upgrade moved.
	if want := []string{"1.1.0", "1.0.0"}; !equalCols(grid.Versions, want) {
		t.Errorf("versions = %v, want %v", grid.Versions, want)
	}
	if len(grid.Rows) != 2 || grid.Rows[0].Library != "lib" {
		t.Fatalf("rows = %+v, want lib first", grid.Rows)
	}
	if got := grid.Rows[0].Cells; got[0] != "3.0.0" || got[1] != "2.0.0" {
		t.Errorf("lib row = %v, want 3.0.0 under 1.1.0 and 2.0.0 under 1.0.0", got)
	}
	// A version that never shipped this library says so, rather than
	// borrowing the neighbouring cell.
	if grid.Rows[1].Cells[1] != "" {
		t.Errorf("other row = %v, want nothing under 1.0.0", grid.Rows[1].Cells)
	}
}

// Two versions of one library under a single release of the parent is the
// collision worth seeing, and the cell must not pick one and hide the other.
func TestACellHoldsEveryVersionThatShipped(t *testing.T) {
	grid := buildShipsWith([]DependencyEdge{
		{ParentVersion: "1.0.0", ChildName: "lib", ChildVersion: "2.0.0", Projects: 3},
		{ParentVersion: "1.0.0", ChildName: "lib", ChildVersion: "2.1.0", Projects: 1},
	})
	cell := grid.Rows[0].Cells[0]
	if !strings.Contains(cell, "2.0.0") || !strings.Contains(cell, "2.1.0") {
		t.Errorf("cell = %q, want both versions", cell)
	}
}

func equalCols(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

// Pinning a version filters the page, and half the page was ignoring it:
// ?f_version=2.0.4 narrowed the cube and left the dependency tables showing
// every release. A page that says it is filtered has to be.
func TestPinningAVersionNarrowsTheGrid(t *testing.T) {
	edges := []DependencyEdge{
		{ParentVersion: "2.0.4", ChildName: "function-bind", ChildVersion: "1.1.2", Projects: 1},
		{ParentVersion: "2.0.3", ChildName: "function-bind", ChildVersion: "1.1.1", Projects: 4},
	}
	all := buildShipsWith(edges)
	if len(all.Versions) != 2 {
		t.Fatalf("unpinned versions = %v, want both", all.Versions)
	}
	pinned := buildShipsWith(filterEdgesToVersion(edges, "2.0.4"))
	if len(pinned.Versions) != 1 || pinned.Versions[0] != "2.0.4" {
		t.Errorf("pinned versions = %v, want only 2.0.4", pinned.Versions)
	}
	if len(pinned.Rows) != 1 || pinned.Rows[0].Cells[0] != "1.1.2" {
		t.Errorf("pinned rows = %+v, want the 2.0.4 row only", pinned.Rows)
	}
}

// The dependants table answers "who pulled THIS version", so pinning one is
// exactly the question it was already shaped for.
func TestPinningAVersionNarrowsTheDependants(t *testing.T) {
	edges := []DependencyEdge{
		{ParentName: "a", ParentVersion: "1.0.0", ChildVersion: "2.0.4", Projects: 1},
		{ParentName: "b", ParentVersion: "9.0.0", ChildVersion: "2.0.3", Projects: 2},
	}
	got := filterDependantsToVersion(edges, "2.0.4")
	if len(got) != 1 || got[0].ParentName != "a" {
		t.Errorf("dependants = %+v, want only what pulled 2.0.4", got)
	}
}
