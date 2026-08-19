package web

import (
	"context"
	"sync"
	"testing"
	"time"
)

// countingVersions counts cube assemblies. PackageVersions is the first store
// call loadCubeFacts makes, so one call per assembly.
type countingVersions struct {
	*fakeStore
	mu    sync.Mutex
	calls int
}

func (c *countingVersions) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	// Real assembly is dozens of round trips; the delay is what makes the
	// concurrent window wide enough for the race to be the one in production.
	time.Sleep(20 * time.Millisecond)
	return c.fakeStore.PackageVersions(ctx, ecosystem, name)
}

func (c *countingVersions) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func cubeSingleflightStore() *countingVersions {
	purl := "pkg:npm/axios@1.0.0"
	return &countingVersions{fakeStore: &fakeStore{
		versions:  map[string][]string{"npm|axios": {"1.0.0"}},
		symbols:   map[string][]string{"npm|axios|1.0.0": {"axios.get"}},
		snapshots: map[string]string{snapKey(purl, ""): cubeSnap(purl, "", "linux", "amd64", "node", "22", "npm", "PROJECT_COMPILE", 3, 0)},
	}}
}

// The cache releases its lock before loading, so every concurrent reader that
// misses runs the whole fan-out. One cold package on the landing page is six
// assemblies; a burst of visitors multiplies that against a pool of eight
// connections, which is how a slow page becomes a stalled server.
func TestCubeFactsCollapsesConcurrentColdReaders(t *testing.T) {
	store := cubeSingleflightStore()
	s := &site{d: Deps{Store: store}}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if facts, _ := s.cubeFacts(context.Background(), "npm", "axios"); len(facts) == 0 {
				t.Error("cube assembled no facts")
			}
		}()
	}
	wg.Wait()
	if got := store.count(); got != 1 {
		t.Errorf("assembled the cube %d times for 16 concurrent readers, want 1", got)
	}
}

// A second reader after the first finished must still be served from cache.
func TestCubeFactsStillCachesAfterSingleflight(t *testing.T) {
	store := cubeSingleflightStore()
	s := &site{d: Deps{Store: store}}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if facts, _ := s.cubeFacts(ctx, "npm", "axios"); len(facts) == 0 {
			t.Fatal("cube assembled no facts")
		}
	}
	if got := store.count(); got != 1 {
		t.Errorf("assembled the cube %d times for 3 sequential readers, want 1", got)
	}
}

// panickingVersions fails the way a real store failure arrives: a panic out of
// the load, which the site's own handler guard recovers (web.go handle).
type panickingVersions struct {
	*fakeStore
	mu     sync.Mutex
	panics int
}

func (p *panickingVersions) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	p.mu.Lock()
	first := p.panics == 0
	p.panics++
	p.mu.Unlock()
	if first {
		panic("store exploded mid-assembly")
	}
	return p.fakeStore.PackageVersions(ctx, ecosystem, name)
}

// The in-flight key is published before the load and removed after it. A panic
// skips the removal, so the key stays set to a channel nobody will ever close
// and every later reader blocks on it until its request context expires. The
// process survives the panic -- net/http recovers, and so does this repo's own
// handler guard -- so what is left behind is a package whose cube is gone until
// the server restarts.
func TestCubeFactsRecoversAfterAPanickingLoad(t *testing.T) {
	store := &panickingVersions{fakeStore: cubeSingleflightStore().fakeStore}
	s := &site{d: Deps{Store: store}}

	func() {
		defer func() { _ = recover() }()
		s.cubeFacts(context.Background(), "npm", "axios")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	facts, _ := s.cubeFacts(ctx, "npm", "axios")
	if waited := time.Since(start); waited > 250*time.Millisecond {
		t.Errorf("a later reader blocked %s on a key the panicking load never released", waited)
	}
	if len(facts) == 0 {
		t.Error("the package's cube never came back after one recovered panic")
	}
}

// cancelAwareStore fails the way a real store does when its caller goes away:
// the version list is already in hand, and everything after it stops.
type cancelAwareStore struct {
	*fakeStore
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (c *cancelAwareStore) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	versions, err := c.fakeStore.PackageVersions(ctx, ecosystem, name)
	// The reader hits stop right after the first hop, mid fan-out.
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.mu.Unlock()
	return versions, err
}

func (c *cancelAwareStore) PackageSymbols(ctx context.Context, ecosystem, name, version string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.fakeStore.PackageSymbols(ctx, ecosystem, name, version)
}

func (c *cancelAwareStore) SnapshotJSON(ctx context.Context, purl, symbol string) (string, bool) {
	if ctx.Err() != nil {
		return "", false
	}
	return c.fakeStore.SnapshotJSON(ctx, purl, symbol)
}

// loadCubeFacts swallows per-hop failures on purpose -- a missing symbol list
// is not a reason to lose the whole cube -- so a cancelled context does not
// surface as an error. It surfaces as an empty assembly, which then gets
// cached and served to everyone for cubeTTL. Singleflight makes it worse: the
// readers parked on that load get the empty answer too. One reader pressing
// stop must not take a package's cube off the site for five minutes.
func TestCubeFactsDoesNotCacheAnAssemblyItsCallerAbandoned(t *testing.T) {
	base := cubeSingleflightStore()
	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelAwareStore{fakeStore: base.fakeStore, cancel: cancel}
	s := &site{d: Deps{Store: store}}

	s.cubeFacts(ctx, "npm", "axios")

	facts, _ := s.cubeFacts(context.Background(), "npm", "axios")
	if len(facts) == 0 {
		t.Error("an abandoned assembly was cached, so the next reader got an empty cube")
	}
}
