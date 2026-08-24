package admin

import (
	"strings"
	"testing"
)

// The cell census is nested under "backlog" (farm_http.go), and every field
// the script reads has to be spelled the way the handler writes it. The
// quarantine panel already lost this argument once: it read a nested object
// off the payload root, `|| []` swallowed the undefined, and the panel showed
// its empty state forever while the signal it was built for went unreported.
//
// So this pins the two ends together. A census that renders zeros for a
// payload it never received is worse than one that says it could not read —
// "no dashes left" and "nobody counted the dashes" are the two readings this
// whole panel exists to keep apart.
func TestMatrixCellsPanelReadsTheBacklogObject(t *testing.T) {
	js, err := adminStaticFS.ReadFile("static/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if strings.Contains(src, "data.matrixCells") {
		t.Error("admin.js reads matrixCells off the payload root; farm_http.go serves it under backlog")
	}
	for _, field := range []string{
		"b.matrixCells", "m.cells", "m.observed",
		"m.verifiedNoObservation", "m.unmeasured", "m.packagesShowingBothDashes",
	} {
		if !strings.Contains(src, field) {
			t.Errorf("admin.js no longer renders %s", field)
		}
	}
	// Absent is not zero. The panel must have a branch that says so.
	if !strings.Contains(src, "읽지 못함") {
		t.Error("admin.js renders no absent-state for the cell census")
	}
}
