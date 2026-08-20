package web

import (
	"context"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// countingAssemblies counts cube assemblies the same way the singleflight
// test does: one PackageVersions call per assembly.
type countingAssemblies struct {
	*fakeStore
	mu    sync.Mutex
	calls int
}

func (c *countingAssemblies) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.fakeStore.PackageVersions(ctx, ecosystem, name)
}

func (c *countingAssemblies) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// heroHits builds n measurable hot packages, each with a real snapshot so
// every one of them could yield a renderable grid.
func heroHits(n int) ([]PackageHit, *countingAssemblies) {
	versions := map[string][]string{}
	symbols := map[string][]string{}
	snapshots := map[string]string{}
	hits := make([]PackageHit, 0, n)
	for i := 0; i < n; i++ {
		name := "pkg" + strconv.Itoa(i)
		purl := "pkg:npm/" + name + "@1.0.0"
		versions["npm|"+name] = []string{"1.0.0"}
		symbols["npm|"+name+"|1.0.0"] = []string{name + ".call"}
		snapshots[snapKey(purl, "")] = cubeSnap(purl, "", "linux", "amd64", "node", "22", "npm", "PROJECT_COMPILE", 5, 0)
		hits = append(hits, PackageHit{Ecosystem: "npm", Name: name})
	}
	return hits, &countingAssemblies{fakeStore: &fakeStore{
		versions: versions, symbols: symbols, snapshots: snapshots,
	}}
}

// The hero probes candidates for the richest grid and never stops early, so
// a cold landing assembled six cubes before rendering one. The page needs a
// matrix, not the best possible matrix: one assembly is the honest cost.
func TestHeroMatrixAssemblesOneCubeWhenNothingIsWarm(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)
	s := &site{d: Deps{Store: store}}
	r := httptest.NewRequest("GET", "/", nil)

	if m := s.heroMatrix(r, "en", hits); m == nil {
		t.Fatal("no hero matrix rendered")
	}
	if got := store.count(); got != 1 {
		t.Errorf("assembled %d cubes for one cold landing view, want 1", got)
	}
}

// A warm cube must be preferred over assembling anything at all, so a second
// view costs nothing.
func TestHeroMatrixCostsNothingWhenWarm(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)
	s := &site{d: Deps{Store: store}}
	r := httptest.NewRequest("GET", "/", nil)

	if m := s.heroMatrix(r, "en", hits); m == nil {
		t.Fatal("no hero matrix rendered")
	}
	before := store.count()
	for i := 0; i < 5; i++ {
		if m := s.heroMatrix(r, "en", hits); m == nil {
			t.Fatal("no hero matrix on a warm view")
		}
	}
	if got := store.count(); got != before {
		t.Errorf("warm landing views assembled %d more cubes, want 0", got-before)
	}
}

// A first candidate with no measured facts must not cost the page its
// matrix: the fallback keeps going until one renders, and only then stops.
func TestHeroMatrixSkipsCandidatesWithNoFacts(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)
	// Strip everything the first candidate could render from.
	delete(store.fakeStore.versions, "npm|pkg0")
	delete(store.fakeStore.symbols, "npm|pkg0|1.0.0")
	delete(store.fakeStore.snapshots, snapKey("pkg:npm/pkg0@1.0.0", ""))

	s := &site{d: Deps{Store: store}}
	m := s.heroMatrix(httptest.NewRequest("GET", "/", nil), "en", hits)
	if m == nil {
		t.Fatal("no hero matrix rendered despite five measurable candidates")
	}
	if m.Package == "pkg0" {
		t.Fatalf("rendered the candidate with no facts: %+v", m)
	}
	if got := store.count(); got != 2 {
		t.Errorf("assembled %d cubes, want 2 (the empty one, then the first that renders)", got)
	}
}

// Which cubes are warm is a function of what other visitors happened to browse
// in the last five minutes, so ranking the warm subset made the front page's
// featured package a function of other people's traffic. Preferring warm is a
// cost decision; it must not be a selection input.
func TestHeroMatrixFeaturesTheTopHitRegardlessOfWhatIsWarm(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)
	s := &site{d: Deps{Store: store}}

	// Someone else browsed pkg3 a moment ago, so only its cube is warm.
	if facts, _ := s.cubeFacts(context.Background(), "npm", "pkg3"); len(facts) == 0 {
		t.Fatal("failed to warm the decoy cube")
	}

	m := s.heroMatrix(httptest.NewRequest("GET", "/", nil), "en", hits)
	if m == nil {
		t.Fatal("no hero matrix rendered")
	}
	if m.Package != "pkg0" {
		t.Errorf("featured %q, want pkg0 — the top hit, not whatever was warm", m.Package)
	}
}

// The reader's own choice still wins over the ranking.
func TestHeroMatrixHonoursTheExplicitSelection(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)
	s := &site{d: Deps{Store: store}}
	m := s.heroMatrix(httptest.NewRequest("GET", "/?m=npm/pkg4", nil), "en", hits)
	if m == nil {
		t.Fatal("no hero matrix rendered")
	}
	if m.Package != "pkg4" {
		t.Errorf("featured %q, want the explicitly selected pkg4", m.Package)
	}
}

// Stopping at the first candidate that rendered anything made the featured
// slice simply the first hot package. On this corpus that is a grid where one
// cell in eighteen has a number in it: symbol-level observations exist for
// 138 packages out of 2,729, so which candidate is chosen decides whether the
// front page shows the network at its most measured or at its emptiest.
func TestHeroMatrixPrefersASliceThatCarriesUsage(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)
	// pkg0 renders, but its only evidence is a contract run: the cell shows
	// the verification mark and no usage at all.
	purl := "pkg:npm/pkg0@1.0.0"
	store.fakeStore.snapshots[snapKey(purl, "")] =
		cubeSnap(purl, "", "linux", "amd64", "node", "22", "npm", "CONTRACT", 3, 0)

	s := &site{d: Deps{Store: store}}
	m := s.heroMatrix(httptest.NewRequest("GET", "/", nil), "en", hits)
	if m == nil {
		t.Fatal("no hero matrix rendered")
	}
	if gridUsageCells(m.Grid) == 0 {
		t.Errorf("featured %q, whose grid carries no usage at all", m.Package)
	}
}

// "Mostly measured" was half the cells, and the scan stopped at the first
// candidate that cleared it. On this corpus that is a low bar — symbol-level
// observations exist for a small share of packages — so the front page could
// settle for a grid half full while a denser one sat two candidates later.
//
// The exit now wants a grid that is nearly all measured, which is what the
// page is for: the reader should meet the network where it has the most to
// show, not at the first slice that was not embarrassing.
func TestHeroMatrixKeepsLookingPastAHalfMeasuredGrid(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)

	// The first candidate clears half and no more.
	half := "pkg:npm/pkg0@1.0.0"
	store.fakeStore.snapshots[snapKey(half, "")] =
		cubeSnap(half, "", "linux", "amd64", "node", "22", "npm", "PROJECT_TEST", 4, 1)

	s := &site{d: Deps{Store: store}}
	m := s.heroMatrix(httptest.NewRequest("GET", "/", nil), "en", hits)
	if m == nil {
		t.Fatal("no hero matrix rendered")
	}
	total, used := gridCells(m.Grid), gridUsageCells(m.Grid)
	if total == 0 {
		t.Fatal("featured an empty grid")
	}
	if used*4 < total*3 {
		t.Errorf("featured %q with %d of %d cells measured; the scan settled early",
			m.Package, used, total)
	}
}
