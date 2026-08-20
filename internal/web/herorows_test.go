package web

import "testing"

// The front page is the showcase, and it was showing a row labelled
// "node (version not recorded)" — a real gap, honestly named, and the worst
// possible thing to put in a shop window. The full explorer keeps it: there
// completeness is the point, and a reader who has drilled in wants to know
// the dimension was never captured.
func TestHeroGridDropsRowsForDimensionsNothingRecorded(t *testing.T) {
	g := pivotGrid{
		Cols: []pivotAxis{{Label: "1.0.0"}, {Label: "2.0.0"}},
		Rows: []pivotGridRow{
			{Label: "node 22", Cells: []pivotCell{{Class: "observed"}, {Class: "observed"}}},
			{Label: "node" + unrecordedAxisSuffix, Cells: []pivotCell{{Class: "observed"}, {Class: "empty"}}},
		},
	}
	out := dropUnrecordedAxes(g)
	if len(out.Rows) != 1 || out.Rows[0].Label != "node 22" {
		t.Errorf("rows = %+v, want only the recorded one", out.Rows)
	}
}

// A column can be unrecorded too, and dropping it must take its cell out of
// every row rather than leaving the rows a different width than the header.
func TestHeroGridDropsUnrecordedColumns(t *testing.T) {
	g := pivotGrid{
		Cols: []pivotAxis{{Label: "node 22"}, {Label: "node" + unrecordedAxisSuffix}},
		Rows: []pivotGridRow{
			{Label: "1.0.0", Cells: []pivotCell{{Class: "observed"}, {Class: "observed"}}},
		},
	}
	out := dropUnrecordedAxes(g)
	if len(out.Cols) != 1 || out.Cols[0].Label != "node 22" {
		t.Errorf("cols = %+v, want only the recorded one", out.Cols)
	}
	if len(out.Rows[0].Cells) != len(out.Cols) {
		t.Errorf("row has %d cells against %d columns", len(out.Rows[0].Cells), len(out.Cols))
	}
}

// Everything unrecorded means there is nothing to show rather than an empty
// frame: the caller falls through to the next candidate.
func TestHeroGridWithNothingRecordedIsEmpty(t *testing.T) {
	g := pivotGrid{
		Cols: []pivotAxis{{Label: "1.0.0"}},
		Rows: []pivotGridRow{{Label: "node" + unrecordedAxisSuffix, Cells: []pivotCell{{Class: "observed"}}}},
	}
	if out := dropUnrecordedAxes(g); !out.Empty() {
		t.Errorf("grid = %+v, want empty", out)
	}
}

// The label is matched structurally, not by hunting for English in a
// translated page.
func TestUnrecordedLabelIsRecognisedWhereItIsMade(t *testing.T) {
	if !isUnrecordedAxisLabel(runtimeBucket("node", "")) {
		t.Error("the label runtimeBucket makes for a missing version is not recognised")
	}
	if isUnrecordedAxisLabel(runtimeBucket("node", "22.1.0")) {
		t.Error("a recorded runtime was treated as a gap")
	}
	if isUnrecordedAxisLabel("") || isUnrecordedAxisLabel("1.2.3") {
		t.Error("an ordinary label was treated as a gap")
	}
}
