package web

import (
	"strings"
	"testing"
)

// depsFixture pins one release of one package and gives it three dependencies
// in three different measured states.
func depsFixture(t *testing.T) *fakeStore {
	t.Helper()
	f := newFakeStore()
	f.dependencyEcosystem = "npm"
	f.versions["npm|app"] = []string{"1.0.0"}
	f.dependencies = []DependencyEdge{
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "proven", ChildVersion: "2.0.0", Projects: 7},
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "seen", ChildVersion: "3.0.0", Projects: 2},
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "unknown", ChildVersion: "4.0.0", Projects: 1},
		// A different release of the same package, which this page must ignore.
		{ParentName: "app", ParentVersion: "0.9.0", ChildName: "old", ChildVersion: "1.1.1", Projects: 50},
	}
	f.snapshots[snapKey("pkg:npm/proven@2.0.0", "")] = cubeSnap("pkg:npm/proven@2.0.0", "",
		"linux", "x64", "node", "22", "npm", "CONTRACT", 3, 0)
	f.snapshots[snapKey("pkg:npm/seen@3.0.0", "")] = cubeSnap("pkg:npm/seen@3.0.0", "",
		"linux", "x64", "node", "22", "npm", "PROJECT_COMPILE", 5, 0)
	// "unknown" deliberately has no snapshot at all.
	return f
}

// Each dependency row says what this network measured about THAT release.
//
// The list used to be name and version and nothing else, so a reader could not
// tell a dependency the network has run a contract against from one it has
// never seen — and both look equally settled in a table.
func TestADependencyRowSaysWhatWasMeasuredAtThatCoordinate(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = depsFixture(t) })
	body := get(t, mux, "/npm/app?f_version=1.0.0").Body.String()

	for _, want := range []string{"contract passed", "builds observed", "nothing measured"} {
		if !strings.Contains(body, want) {
			t.Errorf("state %q missing from the dependency table", want)
		}
	}
	// "nothing measured" is a gap and must be stated, not left blank for the
	// reader to fill in with an assumption.
	if strings.Count(body, "nothing measured") != 1 {
		t.Error("the unmeasured dependency did not get exactly one honest state")
	}
}

// The state is about the child alone, and the page says so. Otherwise the
// column reads as a verdict on the pair, which is the claim this network
// refuses to make without a contract that ran on it.
func TestTheDependencyStateIsNotACompatibilityClaim(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = depsFixture(t) })
	body := get(t, mux, "/npm/app?f_version=1.0.0").Body.String()
	if !strings.Contains(body, "not about the pair") {
		t.Error("the table does not say the measured column is about the dependency alone")
	}
}

// The project count was measured all along and thrown away at this boundary.
// It separates a dependency the whole ecosystem shares from one this release
// alone pulled, and it links to the other side of the edge.
func TestADependencyRowCarriesItsProjectCountAndOpensTheAtlas(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = depsFixture(t) })
	body := get(t, mux, "/npm/app?f_version=1.0.0").Body.String()

	if !strings.Contains(body, "7 project-days") {
		t.Error("the project count is missing from the dependency row")
	}
	if !strings.Contains(body, "eco=npm&amp;name=proven&amp;ver=2.0.0") {
		t.Error("the row does not open that release in the atlas")
	}
	// A dependency of a DIFFERENT release must not appear on a pinned page.
	// Matched by its exact coordinate: "old" alone is a substring of ordinary
	// words on the page, and an assertion that loose reports a leak that is
	// not there.
	if strings.Contains(body, "name=old&amp;ver=1.1.1") {
		t.Error("a dependency of another release leaked onto this one")
	}
}

// "Read and found empty" and "nobody has read it" are opposite states, and an
// empty section renders them the same.
//
// One closes the dependency axis for that coordinate; the other leaves it open
// for future work. A page that cannot tell them apart turns a measurement into
// a blank.
func TestAReleaseMeasuredToDeclareNothingSaysSo(t *testing.T) {
	f := newFakeStore()
	f.dependencyEcosystem = "npm"
	f.versions["npm|leaf"] = []string{"1.0.0"}
	f.resolvedNone = map[string]bool{"leaf@1.0.0": true}

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/leaf?f_version=1.0.0").Body.String()
	if !strings.Contains(body, "found no dependencies") {
		t.Error("a release measured to declare nothing rendered as an empty section")
	}
}

// And a release nothing has read stays silent rather than borrowing that
// claim.
func TestAReleaseNobodyHasReadClaimsNothing(t *testing.T) {
	f := newFakeStore()
	f.dependencyEcosystem = "npm"
	f.versions["npm|silent"] = []string{"1.0.0"}

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/silent?f_version=1.0.0").Body.String()
	if strings.Contains(body, "found no dependencies") {
		t.Error("a release nothing read was reported as having none")
	}
}
