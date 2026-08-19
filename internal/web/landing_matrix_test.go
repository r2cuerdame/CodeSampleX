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

// The landing hero renders a real slice of the most-measured package's
// cube: runtime columns, OS rows, and cells that drill into the explorer.
func TestLandingRendersHotPackageMatrix(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = matrixStore() })
	body := get(t, mux, "/").Body.String()

	mustContain(t, body, `id="matrix"`)
	mustContain(t, body, `<table class="pivot">`)
	for _, s := range []string{"node 22", "node 20", "linux", "windows", "macos"} {
		mustContain(t, body, s)
	}
	// The Windows/node-22 slice holds hydrateRoot's contract failure — the
	// cell says FAIL and carries the anomaly marker, never a bare color.
	mustContain(t, body, `class="pvstate mono">FAIL</span>`)
	mustContain(t, body, `<b class="mark bang" aria-hidden="true">!</b>`)
	// Cells link into the package cube with their coordinates pinned
	// (html/template escapes "+" as &#43; inside href attributes).
	mustContain(t, body, `/npm/reactish?f_os=windows&amp;f_runtime=node&#43;22`)
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
	mustContain(t, body, "node 22") // the default reactish grid rendered
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
