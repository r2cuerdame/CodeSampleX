package web

import (
	"strings"
	"testing"
)

// verifiedOnlyFact is the shape of a coordinate this network alone has run:
// a signed contract receipt and no developer-machine observation at all.
func verifiedOnlyFact(tool, symbol, version string, pkgLevel bool) cubeFact {
	return cubeFact{
		Dims: map[string]string{
			"symbol": symbol, "version": version,
			"os": "debian glibc", "tool": tool, "runtime": "ruby 3",
		},
		EnvHash:      tool,
		PackageLevel: pkgLevel,
		Agg:          pivotAgg{verPass: 1},
	}
}

// A package the farm authored and nobody has installed yet renders a blank
// cube: the axes are picked over every dimension the facts carry, and then
// observationsOnlyOnEnvironmentAxes drops this network's own runs from any
// grid spread over WHERE things ran. With no other evidence to hold, every
// cell empties and the page shows an empty shell.
//
// This is not a corner: 726 of the 2,362 public samples sit on a coordinate
// no developer machine has ever reported, and gem/rack-test — 21 samples, 19
// signed PASS receipts — served exactly that empty grid.
//
// The axes must be decided over the evidence the grid will actually render,
// which is the same rule the symbol axis already gets.
func TestEnvironmentAxisIsNotTheDefaultWhenOnlyOurOwnRunsExist(t *testing.T) {
	facts := []cubeFact{
		verifiedOnlyFact("bundler", "Rack::Test::Session", "2.2.0", false),
		verifiedOnlyFact("rubygems", "Rack::Test::CookieJar", "2.2.0", false),
		verifiedOnlyFact("bundler", cubePackageLevel, "2.2.0", true),
	}
	x, y, ok := defaultCubeAxes(facts, map[string]string{})
	if !ok {
		t.Fatal("no axes at all, so the package page renders no cube")
	}
	if isEnvironmentDim(x) && isEnvironmentDim(y) {
		t.Fatalf("axes = %s x %s: both environment dimensions, so every "+
			"verification is dropped and the grid renders empty", x, y)
	}
	grid := buildCubeGrid(facts, x, y, pivotLinks{}, pivotNow, true)
	if len(grid.Rows) == 0 {
		t.Errorf("axes = %s x %s produced an empty grid for a package with "+
			"19 signed contract runs", x, y)
	}
}

// The same rule the other way round: where developer machines DID report,
// an environment axis still answers "where did builds run".
func TestEnvironmentAxisStaysAvailableWhenObservationsExist(t *testing.T) {
	observed := func(os string) cubeFact {
		return cubeFact{
			Dims:    map[string]string{"symbol": cubePackageLevel, "version": "1.0.0", "os": os},
			EnvHash: os,
			Agg:     pivotAgg{obsPass: 3, obsPeers: 1, verPass: 1},
		}
	}
	facts := []cubeFact{observed("alpine musl"), observed("windows 11")}
	grid := buildCubeGrid(facts, "os", "symbol", pivotLinks{}, pivotNow, true)
	if len(grid.Cols) != 2 {
		t.Errorf("os columns = %d, want 2 — observations belong on an environment axis", len(grid.Cols))
	}
}

// End to end on the shape production actually serves: a package whose only
// evidence is this network's own contract runs, spread over two package
// managers because the farm ran it under both.
//
// gem/rack-test served an empty <table class="pivot"> at
// https://codesamplex.dev/gem/rack-test — the whole compatibility cube blank
// on a package with 21 samples and 19 signed PASS receipts, because the
// default axes came out "Package manager × Symbol" and a package-manager
// axis is an environment axis.
func TestPackagePageWithOnlyOurOwnRunsStillRendersItsCube(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.versions["gem|rack-test"] = []string{"2.2.0"}
	f.symbols["gem|rack-test|2.2.0"] = []string{"Rack::Test::Session", "Rack::Test::CookieJar"}
	f.snapshots[snapKey("pkg:gem/rack-test@2.2.0", "")] =
		cubeSnap("pkg:gem/rack-test@2.2.0", "", "linux", "x64", "ruby", "3", "bundler", "CONTRACT", 11, 0)
	f.snapshots[snapKey("pkg:gem/rack-test@2.2.0", "Rack::Test::Session")] =
		cubeSnap("pkg:gem/rack-test@2.2.0", "Rack::Test::Session", "linux", "x64", "ruby", "3", "bundler", "CONTRACT", 5, 0)
	f.snapshots[snapKey("pkg:gem/rack-test@2.2.0", "Rack::Test::CookieJar")] =
		cubeSnap("pkg:gem/rack-test@2.2.0", "Rack::Test::CookieJar", "linux", "x64", "ruby", "3", "rubygems", "CONTRACT", 3, 0)

	body := get(t, mux, "/gem/rack-test").Body.String()
	if !strings.Contains(body, `<table class="pivot">`) {
		t.Fatalf("no cube at all on the page:\n%s", truncate(body))
	}
	if strings.Contains(body, "<tbody>\n</tbody>") {
		t.Errorf("the cube rendered as an empty shell for a package with 19 "+
			"signed contract runs:\n%s", truncate(body))
	}
}

// A link or a hand-typed ?x= naming an environment dimension gets the same
// treatment: fall back to what the grid can actually render rather than
// serving an empty shell. https://codesamplex.dev/gem/rack-test?x=tool&y=symbol
// returned one.
func TestExplicitEnvironmentAxisFallsBackWhenItWouldRenderNothing(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.versions["gem|rack-test"] = []string{"2.2.0"}
	f.symbols["gem|rack-test|2.2.0"] = []string{"Rack::Test::Session", "Rack::Test::CookieJar"}
	f.snapshots[snapKey("pkg:gem/rack-test@2.2.0", "")] =
		cubeSnap("pkg:gem/rack-test@2.2.0", "", "linux", "x64", "ruby", "3", "bundler", "CONTRACT", 11, 0)
	f.snapshots[snapKey("pkg:gem/rack-test@2.2.0", "Rack::Test::Session")] =
		cubeSnap("pkg:gem/rack-test@2.2.0", "Rack::Test::Session", "linux", "x64", "ruby", "3", "bundler", "CONTRACT", 5, 0)
	f.snapshots[snapKey("pkg:gem/rack-test@2.2.0", "Rack::Test::CookieJar")] =
		cubeSnap("pkg:gem/rack-test@2.2.0", "Rack::Test::CookieJar", "linux", "x64", "ruby", "3", "rubygems", "CONTRACT", 3, 0)

	body := get(t, mux, "/gem/rack-test?x=tool&y=symbol").Body.String()
	if strings.Contains(body, "<tbody>\n</tbody>") {
		t.Errorf("an explicit package-manager axis served an empty shell:\n%s", truncate(body))
	}
}
