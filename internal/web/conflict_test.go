package web

import (
	"strings"
	"testing"
)

// A resolver that installed two versions of one library has already told you
// where to look, and the package page never said so. It is the fact that
// usually explains "why does this not work", and it was in the data all
// along.
func TestPackagePageNamesVersionsThatCollidedInOneProject(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.conflicts = []VersionConflict{
		{Lower: "7.5.0", Higher: "8.19.0", Projects: 4, Failing: 3},
	}
	body := mustGet(t, mux, "/npm/axios")

	mustContain(t, body, "7.5.0")
	mustContain(t, body, "8.19.0")
	// Both halves: how many projects held both, and how many of those broke.
	mustContain(t, body, `class="conflicts"`)
	if !strings.Contains(body, "4") || !strings.Contains(body, "3") {
		t.Error("the page states neither how many projects nor how many failed")
	}
}

// Nothing to report is silence, not an empty heading.
func TestPackagePageStaysQuietWithNoConflicts(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.conflicts = nil
	body := mustGet(t, mux, "/npm/axios")
	if strings.Contains(body, `class="conflicts"`) {
		t.Error("the conflicts block rendered with nothing in it")
	}
}
