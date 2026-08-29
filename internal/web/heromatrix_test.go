package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func waitHeroMatrix(t *testing.T, s *site, r *http.Request, lang string, hits []PackageHit) *heroMatrixData {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if matrix := s.heroMatrix(r, lang, hits); matrix != nil {
			return matrix
		}
		if time.Now().After(deadline) {
			t.Fatal("background hero warm did not publish a matrix")
		}
		time.Sleep(time.Millisecond)
	}
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

type blockingHeroAssemblies struct {
	*countingAssemblies
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func (b *blockingHeroAssemblies) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	b.calls.Add(1)
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return b.fakeStore.PackageVersions(ctx, ecosystem, name)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestColdHeroIsNonBlockingCoalescedAndEventuallyPublished(t *testing.T) {
	hits, base := heroHits(1)
	store := &blockingHeroAssemblies{
		countingAssemblies: base, started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	s := &site{d: Deps{Store: store}}
	r := httptest.NewRequest("GET", "/?m=npm/pkg0", nil)

	started := time.Now()
	if matrix := s.heroMatrix(r, "en", hits); matrix != nil {
		t.Fatal("cold landing synchronously assembled a hero matrix")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("cold hero blocked the landing for %v", elapsed)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("background hero warm did not start")
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if matrix := s.heroMatrix(r, "en", hits); matrix != nil {
				t.Error("in-flight cold hero unexpectedly rendered a partial matrix")
			}
		}()
	}
	wg.Wait()
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("package assemblies for concurrent cold landings = %d, want 1", got)
	}

	close(store.release)
	matrix := waitHeroMatrix(t, s, r, "en", hits)
	if matrix.Package != "pkg0" {
		t.Fatalf("eventual selected hero = %q, want pkg0", matrix.Package)
	}
	if len(matrix.Tabs) != 1 || !matrix.Tabs[0].Selected {
		t.Fatalf("eventual selected tab semantics = %+v", matrix.Tabs)
	}
}

// A deadline can expire inside cubeFacts on the last candidate. That is an
// incomplete warm, not a completed empty hero: publishing nil here would hide
// the matrix for heroMatrixTTL instead of letting the cooldown retry it.
func TestHeroDeadlineOnOnlyCandidateDoesNotPublishNilCache(t *testing.T) {
	hits, base := heroHits(1)
	store := &blockingHeroAssemblies{
		countingAssemblies: base, started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	s := &site{d: Deps{Store: store}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)

	data, complete := s.buildHeroMatrix(r, "en", hits, hits, true)
	if data != nil {
		t.Fatalf("deadline-bounded hero returned partial data: %+v", data)
	}
	// This is the publication gate used by warmHeroMatrix.
	if complete {
		s.cacheHeroMatrix("deadline", data)
	}
	s.heroMu.Lock()
	_, cached := s.heroCache["deadline"]
	s.heroMu.Unlock()
	if complete || cached {
		t.Fatalf("deadline-bounded hero complete=%v cached=%v; want retryable incomplete warm", complete, cached)
	}
}

type failingHeroAssemblies struct{ *countingAssemblies }

func (f *failingHeroAssemblies) PackageVersions(context.Context, string, string) ([]string, error) {
	return nil, errors.New("snapshot inventory unavailable")
}

// A live outer context does not make an immediate store failure a successful
// empty hero. cubeFacts leaves failures uncached, which the warmer must turn
// into cooldown/retry rather than a nil hero cached for heroMatrixTTL.
func TestHeroLoadFailureOnOnlyCandidateDoesNotPublishNilCache(t *testing.T) {
	hits, base := heroHits(1)
	s := &site{d: Deps{Store: &failingHeroAssemblies{countingAssemblies: base}}}
	r := httptest.NewRequest("GET", "/", nil)

	data, complete := s.buildHeroMatrix(r, "en", hits, hits, true)
	if data != nil {
		t.Fatalf("failed hero load returned partial data: %+v", data)
	}
	if complete {
		s.cacheHeroMatrix("failure", data)
	}
	s.heroMu.Lock()
	_, cached := s.heroCache["failure"]
	s.heroMu.Unlock()
	if complete || cached {
		t.Fatalf("failed hero load complete=%v cached=%v; want retryable incomplete warm", complete, cached)
	}
}

// The hero used to probe candidates on the request path. A cold view now
// coalesces background work and stops once it has an honest renderable matrix.
func TestHeroMatrixAssemblesOneCubeWhenNothingIsWarm(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)
	s := &site{d: Deps{Store: store}}
	r := httptest.NewRequest("GET", "/", nil)

	waitHeroMatrix(t, s, r, "en", hits)
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

	waitHeroMatrix(t, s, r, "en", hits)
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

// Assembly is already cached; PIVOTING was not. A warm landing still built
// up to thirty grids per view — six candidates by five axis pairs, pure CPU
// on the most-requested URL of the site — to arrive at the same matrix every
// time. The finished matrix is memoized per (language, selection).
func TestHeroMatrixIsMemoizedPerView(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)
	s := &site{d: Deps{Store: store}}
	r := httptest.NewRequest("GET", "/", nil)

	first := waitHeroMatrix(t, s, r, "en", hits)
	if second := s.heroMatrix(r, "en", hits); second != first {
		t.Error("a warm landing view rebuilt the hero matrix")
	}
	// The labels and hrefs are per-language, so languages never share one.
	if other := waitHeroMatrix(t, s, r, "ko", hits); other == first {
		t.Error("two languages were served one matrix")
	}
	// A junk ?m= is the unselected view, not its own cache entry — the key
	// space must stay bounded by the hot-package list.
	junk := s.heroMatrix(httptest.NewRequest("GET", "/?m=npm/never-heard-of-it", nil), "en", hits)
	if junk != first {
		t.Error("an arbitrary ?m= minted a separate cache entry")
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
	m := waitHeroMatrix(t, s, httptest.NewRequest("GET", "/", nil), "en", hits)
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

	m := waitHeroMatrix(t, s, httptest.NewRequest("GET", "/", nil), "en", hits)
	if m.Package != "pkg0" {
		t.Errorf("featured %q, want pkg0 — the top hit, not whatever was warm", m.Package)
	}
}

// The reader's own choice still wins over the ranking.
func TestHeroMatrixHonoursTheExplicitSelection(t *testing.T) {
	hits, store := heroHits(heroMatrixTries)
	s := &site{d: Deps{Store: store}}
	m := waitHeroMatrix(t, s, httptest.NewRequest("GET", "/?m=npm/pkg4", nil), "en", hits)
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
	m := waitHeroMatrix(t, s, httptest.NewRequest("GET", "/", nil), "en", hits)
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
	m := waitHeroMatrix(t, s, httptest.NewRequest("GET", "/", nil), "en", hits)
	total, used := gridCells(m.Grid), gridUsageCells(m.Grid)
	if total == 0 {
		t.Fatal("featured an empty grid")
	}
	if used*4 < total*3 {
		t.Errorf("featured %q with %d of %d cells measured; the scan settled early",
			m.Package, used, total)
	}
}

// ageHeroCaches backdates every hero memo and every assembled cube, which is
// the state a landing request finds after the memo expires and the candidate
// cubes it would pivot are no longer all warm — the production shape below.
func ageHeroCaches(s *site, by time.Duration) {
	at := time.Now().Add(-by)
	s.heroMu.Lock()
	for k, e := range s.heroCache {
		s.heroCache[k] = heroCacheEntry{data: e.data, at: at}
	}
	s.heroMu.Unlock()
	s.cubeMu.Lock()
	for k, e := range s.cubeCache {
		e.at = at
		s.cubeCache[k] = e
	}
	s.cubeMu.Unlock()
}

// A cold cube must not turn the front page into "this network has no evidence".
//
// Measured on production 2026-08-29 (server v0.1.62, build 3f6ad8d): polling
// the landing once every 8s for 10.7 minutes, 11 of 80 responses rendered
// landing.matrix_empty — "the first compatibility grids appear here as soon
// as the network records enough environment evidence" — directly under this
// page's own counters reading 92,472 recorded observations across 1,980
// packages. Nothing was missing; the misses arrived at a strict ~66s cadence,
// one per heroMatrixTTL expiry that found the probed cubes no longer all
// warm. heroMatrix refuses to assemble on the request path, returned nil, and
// a first-time visitor arriving in that window was told the network is empty.
//
// The last grid this process rendered is the honest thing to show while the
// background refresh runs: one refresh older at worst, and it carries its own
// observation date in the corner.
func TestHeroServesLastGoodMatrixWhileTheCubeRefreshes(t *testing.T) {
	hits, store := heroHits(1)
	s := &site{d: Deps{Store: store}}
	r := httptest.NewRequest("GET", "/?m=npm/pkg0", nil)

	first := waitHeroMatrix(t, s, r, "en", hits)
	ageHeroCaches(s, 2*cubeTTL)

	got := s.heroMatrix(r, "en", hits)
	if got == nil {
		t.Fatal("landing fell back to the no-evidence-yet empty state once its cube aged out; want the last rendered grid while the refresh runs")
	}
	if got.Eco != first.Eco || got.Package != first.Package {
		t.Fatalf("stale hero = %s/%s, want the last good %s/%s", got.Eco, got.Package, first.Eco, first.Package)
	}
}

// Staleness is bounded. A grid held open indefinitely would let a store that
// stopped answering keep a months-old slice on the front page, which is the
// opposite failure to the one above.
func TestHeroStopsServingAMatrixOlderThanTheStaleWindow(t *testing.T) {
	hits, store := heroHits(1)
	s := &site{d: Deps{Store: store}}
	r := httptest.NewRequest("GET", "/?m=npm/pkg0", nil)

	waitHeroMatrix(t, s, r, "en", hits)
	ageHeroCaches(s, heroStaleTTL+time.Minute)

	if got := s.heroMatrix(r, "en", hits); got != nil {
		t.Fatalf("hero served a grid %v old; want the empty state past heroStaleTTL", heroStaleTTL+time.Minute)
	}
}
