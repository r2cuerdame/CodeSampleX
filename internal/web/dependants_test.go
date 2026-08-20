package web

import (
	"strings"
	"testing"
)

// "Two versions are installed" is the half nobody can act on. This is the
// other half, and only the machine that held the lockfile could report it:
// the server receives one package per record, so a resolution arrives
// already shredded.
func TestPackagePageNamesWhoPulledEachVersion(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.dependants = []DependencyEdge{
		{ParentName: "a", ParentVersion: "1.2.0", ChildVersion: "1.9.0", Projects: 4},
		{ParentName: "c", ParentVersion: "3.0.0", ChildVersion: "2.1.0", Projects: 1},
	}
	body := mustGet(t, mux, "/npm/axios")
	mustContain(t, body, `class="dependants"`)
	mustContain(t, body, "a@1.2.0")
	mustContain(t, body, "c@3.0.0")
	// The child version leads, because the question is "who wanted THIS one".
	if i, j := strings.Index(body, "1.9.0"), strings.Index(body, "a@1.2.0"); i < 0 || i > j {
		t.Error("the version being explained does not lead its line")
	}
}

func TestPackagePageStaysQuietWithNoDependants(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.dependants = nil
	if strings.Contains(mustGet(t, mux, "/npm/axios"), `class="dependants"`) {
		t.Error("the block rendered with nothing in it")
	}
}
