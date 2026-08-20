package web

import (
	"strings"
	"testing"
)

// Across releases the same library appears at several versions, so a page
// covering every release has to choose which to display — a choice nobody
// asked it to make and one a reader cannot check. Pinned, there is exactly
// one answer, so the list exists only there.
func TestDependenciesAppearOnlyWithAVersionPinned(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.dependencies = []DependencyEdge{
		{ParentVersion: "2.0.4", ChildName: "function-bind", ChildVersion: "1.1.2"},
		{ParentVersion: "2.0.3", ChildName: "function-bind", ChildVersion: "1.1.1"},
	}
	if body := mustGet(t, mux, "/npm/axios"); strings.Contains(body, "function-bind") {
		t.Error("the unpinned page listed dependencies")
	}
	body := mustGet(t, mux, "/npm/axios?f_version=2.0.4")
	i := strings.Index(body, `<table class="shipswith">`)
	if i < 0 {
		t.Fatal("the pinned page has no dependency table")
	}
	block := body[i:]
	if j := strings.Index(block, "</table>"); j >= 0 {
		block = block[:j]
	}
	mustContain(t, block, "function-bind")
	mustContain(t, block, "1.1.2")
	if strings.Contains(block, "1.1.1") {
		t.Error("another release's dependency leaked into the pinned view")
	}
}

// A library resolved at two versions under one release is the collision worth
// seeing, and collapsing it to one row would hide exactly that.
func TestBothVersionsOfOneLibraryAreListed(t *testing.T) {
	got := buildPackageDeps([]DependencyEdge{
		{ChildName: "lib", ChildVersion: "2.0.0"},
		{ChildName: "lib", ChildVersion: "2.1.0"},
	})
	if len(got) != 2 {
		t.Errorf("deps = %+v, want both versions", got)
	}
}
