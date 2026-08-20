package web

import (
	"strings"
	"testing"
)

// matrixStore seeds a hot package whose snapshots carry full environment
// buckets, so the landing can build a real runtime × OS hero grid.
func matrixStore() *fakeStore {
	f := newCubeStore()
	f.packages = append([]PackageHit{{
		Ecosystem: "npm", Name: "reactish", LatestVersion: "19.1.0",
		Symbols: 2, EvidenceCount: 99_000,
		OperatingSystems: []string{"linux", "windows"}, Runtimes: []string{"node"},
		EvidenceBases: []string{"observed", "verified"},
	}}, f.packages...)
	return f
}

// The landing hero renders a real slice of the most-observed package's cube:
// version columns, symbol rows, and cells that drill into the explorer.
//
// It used to lead with runtime × OS, which is the worst pair this corpus can
// be shown on -- every observation is recorded on Windows and every
// verification runs on Linux, so an OS axis guarantees no cell holds both.
func TestLandingRendersHotPackageMatrix(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = matrixStore() })
	body := get(t, mux, "/").Body.String()

	mustContain(t, body, `id="matrix"`)
	mustContain(t, body, `<table class="pivot">`)
	for _, s := range []string{"19.1.0", "hydrateRoot"} {
		mustContain(t, body, s)
	}
	// hydrateRoot carries a contract failure: the cross says our own run
	// failed there, without leaning on the cell's colour.
	mustContain(t, body, `t-fail`)
	mustContain(t, body, `aria-hidden="true">✕</span>`)
	// Cells link into the package cube with their coordinates pinned.
	mustContain(t, body, `f_version=19.1.0`)
	// The package switcher offers the hot packages.
	mustContain(t, body, `class="mtabs`)
	mustContain(t, body, `?m=npm%2Freactish`)
}

// ?m= may only select a package from the hot list; anything else falls
// back to the default featured package.
func TestLandingMatrixSelectionIsBoundedToHotPackages(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = matrixStore() })
	body := get(t, mux, "/?m=npm/otherpkg").Body.String()
	mustContain(t, body, `<table class="pivot">`)
	mustContain(t, body, "hydrateRoot") // the default reactish grid rendered
}

// An empty network renders an honest placeholder, not a fabricated grid.
func TestLandingSkipsMatrixWhenNoSnapshots(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) {
		f := newFakeStore()
		f.packages = nil
		f.snapshots = map[string]string{}
		d.Store = f
	})
	rec := get(t, mux, "/")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `<table class="pivot">`) {
		t.Error("landing fabricated a pivot grid with no snapshot data")
	}
	mustContain(t, body, `id="matrix"`)
}

// A package whose environment never varies must not produce a 1×1 strip:
// the hero switches to the axis pair with the widest measured spread —
// here versions × symbols.
func TestLandingPrefersTheWidestGrid(t *testing.T) {
	f := newFakeStore()
	f.packages = []PackageHit{{
		Ecosystem: "cargo", Name: "tokioish", LatestVersion: "2.0.0",
		Symbols: 2, EvidenceCount: 50_000,
		OperatingSystems: []string{"linux"}, Runtimes: []string{"rust"},
		EvidenceBases: []string{"verified"},
	}}
	f.versions["cargo|tokioish"] = []string{"2.0.0", "1.0.0"}
	for _, v := range []string{"2.0.0", "1.0.0"} {
		f.symbols["cargo|tokioish|"+v] = []string{"alpha", "beta"}
		purl := "pkg:cargo/tokioish@" + v
		for _, sym := range []string{"", "alpha", "beta"} {
			f.snapshots[snapKey(purl, sym)] =
				cubeSnap(purl, sym, "linux", "x64", "rust", "1.85", "cargo", "CONTRACT", 2, 0)
		}
	}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/").Body.String()

	// Version columns and symbol rows, not a single linux × rust cell.
	for _, s := range []string{"2.0.0", "1.0.0", "alpha", "beta"} {
		mustContain(t, body, s)
	}
	mustContain(t, body, `/cargo/tokioish?f_symbol=alpha&amp;f_version=2.0.0`)
}

// A cell reading "✓ —" is not empty and carries no usage. Scoring counted it
// as density, which picked a six-row grid where exactly one cell had a number
// in it over a slice where most cells did — on a page whose whole job is to
// demonstrate what the network measured.
func TestHeroPrefersCellsThatCarryUsage(t *testing.T) {
	withUsage := pivotGrid{Rows: []pivotGridRow{{Cells: []pivotCell{
		{Class: "observed", Runs: 40}, {Class: "observed", Runs: 30},
	}}, {Cells: []pivotCell{
		{Class: "observed", Runs: 20}, {Class: "observed", Runs: 10},
	}}}, Cols: []pivotAxis{{}, {}}}
	marksOnly := pivotGrid{Rows: []pivotGridRow{{Cells: []pivotCell{
		{Class: "verified"}, {Class: "verified"}, {Class: "verified"},
	}}, {Cells: []pivotCell{
		{Class: "verified"}, {Class: "verified"}, {Class: "verified"},
	}}}, Cols: []pivotAxis{{}, {}, {}}}

	if heroGridScore(withUsage, 0) <= heroGridScore(marksOnly, 0) {
		t.Errorf("a grid with usage (%d) did not beat one of bare marks (%d)",
			heroGridScore(withUsage, 0), heroGridScore(marksOnly, 0))
	}
}
