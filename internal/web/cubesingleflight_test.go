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
