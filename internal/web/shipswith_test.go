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
