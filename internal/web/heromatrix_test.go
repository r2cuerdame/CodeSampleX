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
