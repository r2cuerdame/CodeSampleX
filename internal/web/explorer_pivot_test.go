package web

import (
	"strings"
	"testing"
)

// The package page is the cube explorer: axis selectors, a real grid, and
// drill-down cells.
func TestPackagePageRendersCubeExplorer(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	body := get(t, mux, "/npm/reactish").Body.String()

	mustContain(t, body, "Compatibility cube")
	// Axes and filters apply on change; the Apply button is the
	// no-JavaScript fallback only.
	mustContain(t, body, `<select name="x" data-autosubmit>`)
	mustContain(t, body, `<select name="y" data-autosubmit>`)
	// Filters cover the dimensions NOT on an axis. Version and symbol are the
	// axes here, so pinning them from a dropdown would collapse the grid the
	// reader is looking at.
	mustContain(t, body, `<select name="f_os" data-autosubmit>`)
	mustNotContain(t, body, `<select name="f_version" data-autosubmit>`)
	mustNotContain(t, body, `<select name="f_symbol" data-autosubmit>`)
	mustContain(t, body, `<noscript><button type="submit">`)
	// The reload lands back on the grid instead of the top of the page.
	mustContain(t, body, `action="/npm/reactish#cube"`)
	mustContain(t, body, `id="cube"`)
	mustContain(t, body, `<table class="pivot">`)
	// Default axes on this data: version × symbol — the question the site
	// exists to answer, and the pair that fills cells. An OS axis would file
	// every observation into one row and every verification into another.
	mustContain(t, body, "19.1.0")
	mustContain(t, body, "hydrateRoot")
	// A cell drills down by pinning its coordinates.
	mustContain(t, body, `f_version=19.1.0`)
	mustContain(t, body, `f_symbol=hydrateRoot`)
}

// Drilling into a slice turns the pinned dimensions into removable chips and
// shows what is left.
//
// linux holds 19.1.0 package-level plus createRoot on node 22, so neither
// version nor symbol spreads: one real symbol beside the package-level
// aggregate is not an axis. It used to render as a one-row grid of
// createRoot, which dropped the package-level record entirely — the exact
// records show both.
func TestPackagePageCubeDrillDown(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	body := get(t, mux, "/npm/reactish?f_os=linux").Body.String()

	// The pinned dimension shows once, as a chip that can take it off. It
	// used to be drawn a second time as a dropdown the reader could not
	// use, so a fully drilled coordinate said the same five things twice.
	// It stays in the form as a hidden field, because that is the only
	// thing that carries a pin through the next submit.
	mustContain(t, body, `<span class="pinval">linux</span>`)
	mustContain(t, body, `<input type="hidden" name="f_os" value="linux">`)
	mustNotContain(t, body, `<option value="linux" selected>linux</option>`)
	mustContain(t, body, "Clear filters")
	mustContain(t, body, "createRoot")
	// The aggregate is called by the package's own name.
	mustContain(t, body, "reactish")
}

// A slice narrowed to one measured combination renders the exact record,
// not a grid.
func TestPackagePageCubeLeaf(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	body := get(t, mux, "/npm/reactish?f_os=windows&f_runtime=node+22").Body.String()

	mustContain(t, body, "Exact records")
	mustContain(t, body, "hydrateRoot")
	mustContain(t, body, "@19.1.0")
	// The exact record carries its environment; the cell's tone carries how
	// the runs went, and only a clean pass earns the check.
	mustContain(t, body, "windows · x64 · node 22 · pnpm")
	mustContain(t, body, `class="leafcell pv verified`)
}

// Explicit axes the slice never recorded fall back to real axes instead
// of rendering an empty grid shell.
func TestPackagePageCubeIgnoresUnrecordedAxes(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	body := get(t, mux, "/npm/reactish?x=libc&y=arch").Body.String()
	mustContain(t, body, `<table class="pivot">`)
	mustContain(t, body, "node 22") // fell back to the runtime × os default
}

// A cube built from a capped assembly window says so — an empty cell must
// not read as "never measured anywhere".
func TestPackagePageDisclosesCubeWindow(t *testing.T) {
	f := newCubeStore()
	f.versions["npm|reactish"] = []string{"19.1.0", "18.3.1", "17.0.2", "16.14.0", "15.7.0", "0.14.10", "0.13.3"}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/reactish").Body.String()
	mustContain(t, body, "newest 6 versions")
}

// Filters that match nothing say so instead of inventing a grid.
func TestPackagePageCubeNoMatchIsHonest(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	body := get(t, mux, "/npm/reactish?f_os=freebsd").Body.String()
	mustContain(t, body, "No recorded evidence matches these filters")
	if strings.Contains(body, `<table class="pivot">`) {
		t.Error("a no-match slice must not render a grid")
	}
}

// newSymbolObservationStore is newCubeStore with what production actually
// has at symbol grain: builds that ran and were observed, not only
// contract receipts.
//
// "Which symbol ran where" is a question about places, so the grid is
// drawn from observation. Verification is not: every receipt this network
// holds is signed inside a linux container, so keying it by environment
// would put every symbol in the linux column and leave the rest of the row
// reading as "does not run there".
func newSymbolObservationStore() *fakeStore {
	f := newCubeStore()
	f.snapshots[snapKey("pkg:npm/reactish@19.1.0", "createRoot")] =
		cubeSnap("pkg:npm/reactish@19.1.0", "createRoot", "linux", "x64",
			"node", "22.14", "npm", "PROJECT_COMPILE", 4, 0)
	f.snapshots[snapKey("pkg:npm/reactish@19.1.0", "hydrateRoot")] =
		cubeSnap("pkg:npm/reactish@19.1.0", "hydrateRoot", "windows", "x64",
			"node", "22.14", "pnpm", "PROJECT_COMPILE", 0, 2)
	return f
}

// The version page answers "which symbol ran on which OS" with a grid
// whose cells drill into the cube.
func TestVersionPageSymbolByOSGrid(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newSymbolObservationStore() })
	body := get(t, mux, "/npm/reactish/19.1.0").Body.String()

	mustContain(t, body, "Which symbol ran where")
	mustContain(t, body, `<table class="pivot">`)
	mustContain(t, body, "createRoot")
	mustContain(t, body, "hydrateRoot")
	// The package-level row is not a symbol and is no longer listed among
	// them: it is the total OVER these rows, and every observation is
	// recorded against the package, so it carried all the numbers above a
	// field of blanks.
	mustNotContain(t, body, "whole package")
	// Cells pin version + symbol + OS into the explorer.
	mustContain(t, body, `/npm/reactish?f_os=windows&amp;f_symbol=hydrateRoot&amp;f_version=19.1.0`)
}

// The "(package)" row must not repeat receipts the symbol rows already
// show — the producer files each receipt under "" AND every claimed
// symbol, and a reader would count one contract run three times.
func TestVersionPageDoesNotRepeatPackageLevelReceipts(t *testing.T) {
	f := newFakeStore()
	f.versions["npm|dup"] = []string{"1.0.0"}
	f.symbols["npm|dup|1.0.0"] = []string{"a", "b"}
	for _, sym := range []string{"", "a", "b"} {
		f.snapshots[snapKey("pkg:npm/dup@1.0.0", sym)] =
			cubeSnap("pkg:npm/dup@1.0.0", sym, "linux", "x64", "node", "22.1", "npm",
				"PROJECT_COMPILE", 1, 0)
	}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/dup/1.0.0").Body.String()
	if strings.Contains(body, "(package)") {
		t.Error("the (package) row repeats receipts the symbol rows already carry")
	}
	mustContain(t, body, `class="pv observed`)
}

// The symbol page opens with an OS × runtime summary anchored to the
// detail table below it.
func TestSymbolPageShowsOSPivotAboveDetail(t *testing.T) {
	f := newCubeStore()
	// Give the axios.post fixture snapshot a second OS so the pivot is 2D.
	f.snapshots[snapKey("pkg:npm/reactish@19.1.0", "createRoot")] = `{
	  "schemaVersion": 1,
	  "purl": "pkg:npm/reactish@19.1.0",
	  "symbol": "createRoot",
	  "generatedAt": "2026-08-13T00:00:00Z",
	  "rows": [
	    {"envBucket": {"schemaVersion":1,"os":"linux","arch":"x64","runtime":"node","runtimeVersion":"22.14"},
	     "confidence": "MEDIUM", "passRate": 1, "lastSeen": "2026-08-12T10:00:00Z",
	     "byStage": {"CONTRACT": {"pass": 4, "fail": 0}}},
	    {"envBucket": {"schemaVersion":1,"os":"windows","arch":"x64","runtime":"node","runtimeVersion":"22.14"},
	     "confidence": "LOW", "passRate": 0, "lastSeen": "2026-08-12T10:00:00Z",
	     "byStage": {"CONTRACT": {"pass": 0, "fail": 1}}}
	  ],
	  "failures": []
	}`
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/reactish/19.1.0/createRoot").Body.String()

	pivot := strings.Index(body, `<table class="pivot">`)
	detail := strings.Index(body, `id="env-detail"`)
	if pivot < 0 || detail < 0 || pivot > detail {
		t.Fatalf("pivot must render above the env detail anchor: pivot=%d detail=%d", pivot, detail)
	}
	mustContain(t, body, `href="#env-detail"`)
	mustContain(t, body, `class="glyph" aria-hidden="true">≡</span>`)
}
