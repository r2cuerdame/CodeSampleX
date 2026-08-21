package web

import (
	"strings"
	"testing"
)

// The version page used to answer "which symbol ran where" with a symbol-by-OS
// grid. In production every symbol-grain fact is a contract receipt and every
// receipt is signed in a linux container, so that grid could only ever draw
// one column — and it read as "these APIs run on linux and nowhere else".
//
// The grid is gone. The evidence it was carrying belongs on the symbol row,
// where it is about the API and the release and not about an OS.
func TestASymbolRowSaysWhatThisNetworkRanForIt(t *testing.T) {
	f := newCubeStore()
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/reactish/19.1.0").Body.String()

	i := strings.Index(body, `<ul class="symlist`)
	if i < 0 {
		t.Fatal("no symbol list on the version page")
	}
	list := body[i : i+strings.Index(body[i:], "</ul>")]
	if !strings.Contains(list, "symruns") {
		t.Errorf("no symbol row states what this network ran:\n%s", list)
	}
	// hydrateRoot: two contract runs on this release, both failing.
	if !strings.Contains(list, "0 of 2 runs passed") {
		t.Errorf("the counts do not match the receipts:\n%s", list)
	}
}

// An API this network never ran states nothing beyond its name — no zero, no
// implied absence of evidence elsewhere.
func TestASymbolWithNoRunsClaimsNothing(t *testing.T) {
	f := newFakeStore()
	f.versions["npm|quiet"] = []string{"1.0.0"}
	f.symbols["npm|quiet|1.0.0"] = []string{"onlyNamed"}
	f.snapshots[snapKey("pkg:npm/quiet@1.0.0", "")] =
		cubeSnap("pkg:npm/quiet@1.0.0", "", "linux", "x64", "node", "22.1", "npm", "PROJECT_COMPILE", 3, 0)
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/quiet/1.0.0").Body.String()

	if strings.Contains(body, "0 of 0 runs") {
		t.Error("a row invented a count for an API nothing ran")
	}
}

// An observation is recorded against the PACKAGE, not the API. Counting it
// per symbol would put a package's builds behind every symbol name it
// happens to mention — the same mistake that gave commons-logging a page for
// a Spring Test class.
func TestPackageObservationsAreNotCountedAsSymbolRuns(t *testing.T) {
	f := newFakeStore()
	f.versions["npm|obs"] = []string{"1.0.0"}
	f.symbols["npm|obs|1.0.0"] = []string{"someCall"}
	f.snapshots[snapKey("pkg:npm/obs@1.0.0", "")] =
		cubeSnap("pkg:npm/obs@1.0.0", "", "linux", "x64", "node", "22.1", "npm", "PROJECT_COMPILE", 40, 0)
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/obs/1.0.0").Body.String()

	if strings.Contains(body, "40") && strings.Contains(body, "symruns") {
		t.Error("the package's 40 builds were credited to a symbol row")
	}
}
