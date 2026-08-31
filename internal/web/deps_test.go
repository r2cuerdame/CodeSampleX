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
	// The unpinned page must not print ONE release's dependency table: the
	// page would have to pick which release, which is a choice nobody asked
	// it to make. The cross-release matrix picks none -- it shows every
	// release side by side -- so it is exactly the answer to that objection
	// and is allowed to appear here. The assertion is on the table.
	if body := mustGet(t, mux, "/npm/axios"); strings.Contains(body, `<table class="shipswith">`) {
		t.Error("the unpinned page printed one release's dependency table")
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
	got := buildPackageDeps("npm", []DependencyEdge{
		{ChildName: "lib", ChildVersion: "2.0.0"},
		{ChildName: "lib", ChildVersion: "2.1.0"},
	})
	if len(got) != 2 {
		t.Errorf("deps = %+v, want both versions", got)
	}
}

// A dependency row names a package at an exact version, and that coordinate
// has its own page. Reading "function-bind 1.1.2" and then having to go and
// find it is the one step the row was already holding the answer to.
func TestEachDependencyLinksToThatExactVersion(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.dependencies = []DependencyEdge{
		{ParentVersion: "2.0.4", ChildName: "function-bind", ChildVersion: "1.1.2"},
	}
	body := mustGet(t, mux, "/npm/axios?f_version=2.0.4")
	mustContain(t, body, `href="/npm/function-bind?f_version=1.1.2`)
}

// A scoped name has to survive the URL: @isaacs/brace-expansion is one
// package, not a path.
func TestAScopedDependencyLinksCorrectly(t *testing.T) {
	got := buildPackageDeps("npm", []DependencyEdge{
		{ChildName: "@isaacs/brace-expansion", ChildVersion: "5.0.1"},
	})
	if len(got) != 1 {
		t.Fatalf("deps = %+v", got)
	}
	if !strings.Contains(got[0].Href, "%40isaacs%2Fbrace-expansion") &&
		!strings.Contains(got[0].Href, "@isaacs/brace-expansion") {
		t.Errorf("href = %q, does not carry the scoped name", got[0].Href)
	}
}
