package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func cubeReassemblyStore(t *testing.T) *fakeStore {
	t.Helper()
	purl := "pkg:npm/axios@1.0.0"
	var doc snapshotDoc
	if err := json.Unmarshal([]byte(cubeSnap(purl, "", "linux", "amd64", "node", "22", "npm", "PROJECT_COMPILE", 3, 0)), &doc); err != nil {
		t.Fatal(err)
	}
	row := doc.Rows[0]
	doc.Rows = make([]snapshotRow, 400)
	for i := range doc.Rows {
		doc.Rows[i] = row
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeStore{
		versions:  map[string][]string{"npm|axios": {"1.0.0"}},
		symbols:   map[string][]string{"npm|axios|1.0.0": {}},
		snapshots: map[string]string{snapKey(purl, ""): string(raw)},
	}
}

func expireCubeAssembly(s *site, key string) {
	s.cubeMu.Lock()
	e := s.cubeCache[key]
	e.at = time.Time{}
	s.cubeCache[key] = e
	s.cubeMu.Unlock()
}

// A crawl revisits a package after its assembled cube expires or is evicted.
// Its current snapshot bytes have not changed; reassembling must not unmarshal
// hundreds of rows and rehash their environment again on each visit.
func TestFarm202CubeReassemblyDoesNotRedecodeUnchangedSnapshots(t *testing.T) {
	s := &site{d: Deps{Store: cubeReassemblyStore(t)}}
	ctx := context.Background()
	if facts, _, err := s.cubeFactsWithError(ctx, "npm", "axios"); err != nil || len(facts) != 400 {
		t.Fatal("initial cube", len(facts), err)
	}
	allocs := testing.AllocsPerRun(3, func() {
		expireCubeAssembly(s, "npm|axios")
		facts, _, err := s.cubeFactsWithError(ctx, "npm", "axios")
		if err != nil || len(facts) != 400 {
			panic("complete unchanged cube missing")
		}
	})
	t.Logf("unchanged cube reassembly allocations=%.0f", allocs)
	if allocs > 200 {
		t.Fatalf("unchanged snapshot JSON and environment facts rebuilt: %.0f allocations", allocs)
	}
}

func TestFarm202CubeReassemblyUsesCurrentInputsAndCoordinates(t *testing.T) {
	f := cubeReassemblyStore(t)
	s := &site{d: Deps{Store: f}}
	ctx := context.Background()
	if facts, _, err := s.cubeFactsWithError(ctx, "npm", "axios"); err != nil || len(facts) != 400 {
		t.Fatal("initial", err)
	}
	purl := "pkg:npm/axios@1.0.0"
	f.snapshots[snapKey(purl, "")] = cubeSnap(purl, "", "linux", "arm64", "node", "24", "npm", "PROJECT_COMPILE", 7, 0)
	expireCubeAssembly(s, "npm|axios")
	facts, _, err := s.cubeFactsWithError(ctx, "npm", "axios")
	if err != nil || len(facts) != 1 || facts[0].Agg.events() != 7 || facts[0].Dims["arch"] != "arm64" {
		t.Fatal("changed current document reused older facts", facts, err)
	}
	raw := f.snapshots[snapKey(purl, "")]
	f.versions["npm|axios"] = []string{"2.0.0"}
	f.symbols["npm|axios|2.0.0"] = []string{}
	f.snapshots[snapKey("pkg:npm/axios@2.0.0", "")] = raw
	expireCubeAssembly(s, "npm|axios")
	facts, _, err = s.cubeFactsWithError(ctx, "npm", "axios")
	if err != nil || len(facts) != 1 || facts[0].Dims["version"] != "2.0.0" {
		t.Fatal("same bytes borrowed previous release coordinates", facts, err)
	}
	delete(f.snapshots, snapKey("pkg:npm/axios@2.0.0", ""))
	expireCubeAssembly(s, "npm|axios")
	if facts, _, err = s.cubeFactsWithError(ctx, "npm", "axios"); err != nil || len(facts) != 0 {
		t.Fatal("removed current snapshot still rendered", facts, err)
	}
}

func smallCubeDocuments() []string {
	return []string{"1.0.0", "", cubeSnap("pkg:npm/axios@1.0.0", "", "linux", "amd64", "node", "22", "npm", "PROJECT_COMPILE", 3, 0)}
}

func TestFarm202CubeFactsShareExistingRetentionBudget(t *testing.T) {
	c := decodedClusterCache{maxBytes: 8192, maxEntries: 64}
	ctx := context.Background()
	raw := smallCubeDocuments()
	for i := 0; i < 40; i++ {
		facts, err := c.getCube(ctx, "npm", fmt.Sprintf("cube-%d", i), raw)
		if err != nil || len(facts) != 1 {
			t.Fatal("complete cube", err)
		}
		if c.bytes > c.maxBytes {
			t.Fatalf("cube facts escaped shared byte budget: %d", c.bytes)
		}
	}
	if len(c.entries) == 0 || len(c.entries) >= 40 {
		t.Fatal("cube groups were not bounded", len(c.entries))
	}
	clusters := []string{`{"stage":"PROJECT_TEST","count":3}`}
	if docs, err := c.get(ctx, "npm|cluster", clusters); err != nil || len(docs) != 1 {
		t.Fatal("display namespace", err)
	}
	if c.bytes > c.maxBytes {
		t.Fatal("combined cache escaped shared budget", c.bytes)
	}
	if facts, err := c.getCube(ctx, "npm", "cluster", raw); err != nil || len(facts) != 1 || facts[0].Dims["version"] != "1.0.0" {
		t.Fatal("display and cube inputs collided", err)
	}
	if c.bytes > c.maxBytes {
		t.Fatal("combined namespaces escaped shared budget", c.bytes)
	}
}

func TestFarm202OversizedCubeRemainsCompleteUnretained(t *testing.T) {
	f := cubeReassemblyStore(t)
	raw := []string{"1.0.0", "", f.snapshots[snapKey("pkg:npm/axios@1.0.0", "")]}
	c := decodedClusterCache{maxBytes: 1024}
	facts, err := c.getCube(context.Background(), "npm", "axios", raw)
	if err != nil || len(facts) != 400 {
		t.Fatal("oversized cube was truncated", len(facts), err)
	}
	if c.bytes != 0 || len(c.entries) != 0 {
		t.Fatal("oversized cube retained", c.bytes, len(c.entries))
	}
}

func TestFarm202WarmCubeBypassesColdGateAndCanceledWaitLeaves(t *testing.T) {
	var c decodedClusterCache
	raw := smallCubeDocuments()
	ctx := context.Background()
	if facts, err := c.getCube(ctx, "npm", "warm", raw); err != nil || len(facts) != 1 {
		t.Fatal("warm", err)
	}
	c.slots <- struct{}{}
	c.slots <- struct{}{}
	defer func() { <-c.slots; <-c.slots }()
	warmCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if facts, err := c.getCube(warmCtx, "npm", "warm", raw); err != nil || len(facts) != 1 {
		t.Fatal("warm cube waited for cold work", err)
	}
	coldCtx, cancelCold := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancelCold()
	if _, err := c.getCube(coldCtx, "npm", "cold", raw); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cold canceled wait", err)
	}
	if len(c.loading) != 0 {
		t.Fatal("canceled caller stranded a shared work key")
	}
}

func TestFarm202ConcurrentCubeViewsCannotAppendIntoSharedFacts(t *testing.T) {
	var c decodedClusterCache
	raw := smallCubeDocuments()
	ctx := context.Background()
	initial, err := c.getCube(ctx, "npm", "axios", raw)
	if err != nil || len(initial) != 1 {
		t.Fatal("initial", err)
	}
	if cap(initial) != len(initial) {
		t.Fatal("shared cube append capacity exposed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			facts, err := c.getCube(ctx, "npm", "axios", raw)
			if err != nil {
				t.Error(err)
				return
			}
			view := append(facts, cubeFact{Dims: map[string]string{"version": "pinned"}})
			view[len(view)-1].Dims["version"] = "own pinned view"
			filtered := filterCubeFacts(facts, map[string]string{"version": "1.0.0"})
			if len(filtered) != 1 || facts[0].Dims["version"] != "1.0.0" {
				t.Error("shared facts mutated")
			}
		}()
	}
	wg.Wait()
	if initial[0].Dims["version"] != "1.0.0" {
		t.Fatal("cached coordinates changed")
	}
}

func TestFarm202MalformedCubeDocumentDoesNotDropOtherCurrentFacts(t *testing.T) {
	var c decodedClusterCache
	raw := append(smallCubeDocuments(), "9.0.0", "broken", "{bad")
	facts, err := c.getCube(context.Background(), "npm", "axios", raw)
	if err != nil || len(facts) != 1 || facts[0].Dims["version"] != "1.0.0" {
		t.Fatal("valid current facts disappeared beside malformed JSON", facts, err)
	}
}
