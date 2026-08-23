package web

import (
	"net/url"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A partner axis must not erase the axis it was chosen to support.
//
// cargo/tokio at 1.53.1 is the shape: five APIs this network ran a contract
// against, and one package-level total that developer machines reported. The
// unpinned page spreads version by symbol and shows all five. Pin the version
// — which is what clicking the version header does — and the only dimension
// left with a spread is the symbol, so the grid pairs it with the OS. An OS
// axis drops this network's own runs (observationsOnlyOnEnvironmentAxes),
// which here is every symbol: the grid collapses to the package-level total
// alone, one cell, and dropLinksThatShowNothingNew takes its link away
// because nothing outside the two axes still varies.
//
// The reader is then standing on a one-cell grid with no way down, on a
// coordinate that has five measured APIs under it.
// https://codesamplex.dev/cargo/tokio?f_runtime=rust+1&f_version=1.53.1&lang=ko

// tokioSlice is that coordinate: package-level observations, per-symbol
// contract receipts, everything else already decided.
func tokioSlice() []cubeFact {
	env := func(sym string, agg pivotAgg, pkgLevel bool) cubeFact {
		return cubeFact{
			Dims: map[string]string{
				"version": "1.53.1", "symbol": sym,
				"os": "alpine musl", "libc": "musl", "arch": "x64",
				"runtime": "rust 1", "tool": "cargo", "context": "rust",
			},
			EnvHash:      "alpine",
			PackageLevel: pkgLevel,
			Agg:          agg,
		}
	}
	facts := []cubeFact{env(cubePackageLevel, pivotAgg{obsPass: 6, used: 3, obsPeers: 2}, true)}
	for _, sym := range []string{
		"axum::Router::layer", "axum::Router::nest", "axum::body::Body",
	} {
		facts = append(facts, env(sym, pivotAgg{verPass: 1}, false))
	}
	return facts
}

func TestASymbolAxisIsNotPairedWithAnAxisThatErasesIt(t *testing.T) {
	facts := tokioSlice()
	pinned := map[string]string{"version": "1.53.1", "runtime": "rust 1"}
	x, y, ok := defaultCubeAxes(facts, pinned)
	if !ok {
		t.Fatal("no axes for a slice with three measured APIs")
	}
	grid := buildCubeGrid(facts, x, y, pivotLinks{}, pivotNow, true)
	var labels []string
	for _, c := range grid.Cols {
		labels = append(labels, c.Label)
	}
	for _, r := range grid.Rows {
		labels = append(labels, r.Label)
	}
	for _, want := range []string{"axum::Router::layer", "axum::Router::nest", "axum::body::Body"} {
		found := false
		for _, l := range labels {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("axes = %s x %s render %v — %s has evidence here and no cell to sit in",
				x, y, labels, want)
		}
	}
}

// The cells the axes do render have to be doors. A symbol cell pins the
// symbol and lands on that exact coordinate; without the link the deepest
// coordinate on the page is unreachable by clicking.
func TestEverySymbolCellOnAPinnedVersionIsADoor(t *testing.T) {
	facts := tokioSlice()
	pinned := map[string]string{"version": "1.53.1", "runtime": "rust 1"}
	x, y, ok := defaultCubeAxes(facts, pinned)
	if !ok {
		t.Fatal("no axes")
	}
	grid := buildCubeGrid(facts, x, y, pivotLinks{
		Cell: func(row, col string) string { return "/cell?" + row + "|" + col },
	}, pivotNow, true)
	doors := 0
	for _, r := range grid.Rows {
		for _, c := range r.Cells {
			if c.Href != "" {
				doors++
			}
		}
	}
	if doors == 0 {
		t.Errorf("axes = %s x %s: no cell on the grid is clickable", x, y)
	}
}

// drillDownStore is that coordinate as a store: the shape of the page the
// report came from, and the fixture CSX_WEB_DEVSERVE_STORE=drilldown serves.
func drillDownStore() *fakeStore {
	f := newFakeStore()
	f.versions["cargo|tokio"] = []string{"1.53.1", "1.43.0"}
	f.symbols["cargo|tokio|1.53.1"] = []string{
		"axum::Router::layer", "axum::Router::nest", "axum::body::Body",
	}
	f.snapshots[snapKey("pkg:cargo/tokio@1.53.1", "")] =
		cubeSnap("pkg:cargo/tokio@1.53.1", "", "linux", "x64", "rust", "1", "cargo", "PROJECT_COMPILE", 6, 0)
	for _, sym := range f.symbols["cargo|tokio|1.53.1"] {
		f.snapshots[snapKey("pkg:cargo/tokio@1.53.1", sym)] =
			cubeSnap("pkg:cargo/tokio@1.53.1", sym, "linux", "x64", "rust", "1", "cargo", "CONTRACT", 1, 0)
	}
	f.snapshots[snapKey("pkg:cargo/tokio@1.43.0", "")] =
		cubeSnap("pkg:cargo/tokio@1.43.0", "", "linux", "x64", "rust", "1", "cargo", "PROJECT_COMPILE", 4, 0)
	return f
}

func TestAPinnedVersionKeepsItsSymbolsReachable(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = drillDownStore() })

	body := get(t, mux, "/cargo/tokio?f_runtime=rust+1&f_version=1.53.1").Body.String()
	if !strings.Contains(body, "axum::Router::layer") {
		t.Fatalf("the pinned version hides every symbol it has:\n%s", truncate(body))
	}
	want := "f_symbol=" + url.QueryEscape("axum::Router::layer")
	if !strings.Contains(body, want) {
		t.Errorf("no way to drill into a symbol from the pinned version: %s missing\n%s",
			want, truncate(body))
	}
}

// The pins the reader arrived with travel with the click.
func TestADrillDownKeepsThePinsAlreadyOnTheURL(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = drillDownStore() })

	body := get(t, mux, "/cargo/tokio?f_runtime=rust+1&f_version=1.53.1").Body.String()
	href := ""
	for _, part := range strings.Split(body, `href="`) {
		if i := strings.Index(part, `"`); i > 0 {
			candidate := part[:i]
			if strings.Contains(candidate, "f_symbol=axum") {
				href = candidate
				break
			}
		}
	}
	if href == "" {
		t.Fatalf("no symbol drill-down link at all:\n%s", truncate(body))
	}
	for _, keep := range []string{"f_runtime=rust", "f_version=1.53.1"} {
		if !strings.Contains(href, keep) {
			t.Errorf("drill-down href %q dropped %s", href, keep)
		}
	}
}

// A hand-typed or linked axis pair gets the same rule the defaults do. It
// already fell back when an axis rendered NOTHING; a pair where one axis
// erases the other renders something, and that something is the one cell the
// report was about.
func TestAnExplicitAxisPairThatErasesItselfFallsBack(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = drillDownStore() })

	body := get(t, mux, "/cargo/tokio?f_runtime=rust+1&f_version=1.53.1&x=symbol&y=os").Body.String()
	if !strings.Contains(body, "axum::Router::layer") {
		t.Errorf("an explicit symbol × OS pair hid every symbol it spread over:\n%s", truncate(body))
	}
}

// The report came from cargo, but nothing in the collapse is about Rust: it
// is the shape — package-level observations, per-symbol contract receipts —
// and that shape is what most of this network's evidence looks like.
func TestThePinnedVersionDrillsInEveryEcosystem(t *testing.T) {
	for _, eco := range []struct{ eco, name, purl, symbol string }{
		{"npm", "reactish", "pkg:npm/reactish@19.1.0", "createRoot"},
		{"golang", "example.com/mod", "pkg:golang/example.com/mod@19.1.0", "Mod.New"},
		{"pypi", "flasky", "pkg:pypi/flasky@19.1.0", "flasky.Flask"},
	} {
		t.Run(eco.eco, func(t *testing.T) {
			mux, f := newTestMux(t, nil)
			f.versions[eco.eco+"|"+eco.name] = []string{"19.1.0"}
			f.symbols[eco.eco+"|"+eco.name+"|19.1.0"] = []string{eco.symbol, eco.symbol + "2"}
			f.snapshots[snapKey(eco.purl, "")] =
				cubeSnap(eco.purl, "", "linux", "x64", "node", "22", "npm", "PROJECT_COMPILE", 6, 0)
			for _, sym := range f.symbols[eco.eco+"|"+eco.name+"|19.1.0"] {
				f.snapshots[snapKey(eco.purl, sym)] =
					cubeSnap(eco.purl, sym, "linux", "x64", "node", "22", "npm", "CONTRACT", 1, 0)
			}
			body := get(t, mux, "/"+eco.eco+"/"+eco.name+"?f_version=19.1.0").Body.String()
			if !strings.Contains(body, "f_symbol="+url.QueryEscape(eco.symbol)) {
				t.Errorf("%s: no way down to %s from the pinned version:\n%s",
					eco.eco, eco.symbol, truncate(body))
			}
		})
	}
}

// And the step below it is the bottom. A drilled coordinate states its
// answer and renders no grid at all, so the reader is never left looking at
// a cell that cannot be clicked and does not say why.
func TestDrillingIntoASymbolBottomsOutInTheAnswer(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = drillDownStore() })

	body := get(t, mux, "/cargo/tokio?f_runtime=rust+1&f_version=1.53.1&f_symbol="+
		url.QueryEscape("axum::Router::layer")).Body.String()
	if !strings.Contains(body, `class="answer`) {
		t.Errorf("the deepest coordinate states no answer:\n%s", truncate(body))
	}
	if strings.Contains(body, `<table class="pivot">`) {
		t.Errorf("a decided coordinate still renders a grid to click:\n%s", truncate(body))
	}
}

// A pin the reader arrived with has to survive the next submit even when its
// dimension ended up on an axis. Nothing but a hidden input carries it, and
// the filter bar skipped every dimension it had spread across an axis — so
// pinning a version and then touching any dropdown rebuilt the URL without
// the version, and the reader was thrown back to the whole package.
func TestAPinOnAnAxisStillTravelsWithTheForm(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = drillDownStore() })

	body := get(t, mux, "/cargo/tokio?f_runtime=rust+1&f_version=1.53.1").Body.String()
	mustContain(t, body, `<input type="hidden" name="f_version" value="1.53.1">`)
	mustContain(t, body, `<input type="hidden" name="f_runtime" value="rust 1">`)
	// And it is carried, not offered: the chip above the bar is where a pin
	// is shown and taken off.
	mustNotContain(t, body, `<select name="f_version"`)
}
