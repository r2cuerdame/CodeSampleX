package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type farm202SnapshotStore struct {
	serverstore.Store
	rows        map[string][]serverstore.SnapshotRow
	singleCalls atomic.Int64
}

func (s *farm202SnapshotStore) GetSnapshotsForPURLs(_ context.Context, purls []string) ([]serverstore.SnapshotRow, error) {
	var rows []serverstore.SnapshotRow
	for _, p := range purls {
		rows = append(rows, s.rows[p]...)
	}
	return rows, nil
}
func (s *farm202SnapshotStore) GetSnapshotsForPURL(_ context.Context, purl string) ([]serverstore.SnapshotRow, error) {
	s.singleCalls.Add(1)
	return append([]serverstore.SnapshotRow(nil), s.rows[purl]...), nil
}

func TestFarm202SnapshotCrawlReleasesOldPayloads(t *testing.T) {
	store := &farm202SnapshotStore{rows: make(map[string][]serverstore.SnapshotRow)}
	var purls []string
	for i := 0; i < 40; i++ {
		p := fmt.Sprintf("pkg:npm/farm202-%d@1", i)
		purls = append(purls, p)
		// Distinct payloads reproduce a crawl retaining unrelated release JSON.
		js := fmt.Sprintf("{\"rows\":[],\"package\":%d,\"padding\":\"%s\"}", i, strings.Repeat("x", 2<<20))
		store.rows[p] = []serverstore.SnapshotRow{{PURL: p, Symbol: "symbol", SnapshotJSON: js}}
	}
	w := &webStore{s: store}
	if err := w.PrefetchSnapshots(context.Background(), purls); err != nil {
		t.Fatal(err)
	}
	var retained int64
	w.snapshotJSON.Range(func(_, value any) bool { retained += int64(len(value.(cachedSnapshotJSON).json)); return true })
	if retained > 64<<20 {
		t.Fatalf("a completed crawl still retains %d bytes of unrelated snapshot JSON; want <=64 MiB", retained)
	}
	// Eviction is a cache miss, never an authoritative absence.
	js, ok, err := w.SnapshotJSONWithError(context.Background(), purls[0], "symbol")
	if err != nil || !ok || js != store.rows[purls[0]][0].SnapshotJSON {
		t.Fatalf("evicted positive snapshot must be reloaded, got ok=%v err=%v", ok, err)
	}
	if store.singleCalls.Load() == 0 {
		t.Fatal("the evicted release was not read again")
	}
}

func TestSnapshotPayloadEvictionClearsNegativeAuthority(t *testing.T) {
	purl := "pkg:npm/previously-empty@1"
	store := &farm202SnapshotStore{rows: map[string][]serverstore.SnapshotRow{}}
	w := &webStore{s: store}
	if err := w.PrefetchSnapshots(t.Context(), []string{purl}); err != nil {
		t.Fatal(err)
	}
	purls := make([]string, snapshotPayloadCachePURLs)
	for i := range purls {
		purls[i] = fmt.Sprintf("pkg:npm/negative-%d@1", i)
	}
	w.cacheSnapshotRows(nil, purls, time.Now())
	store.rows[purl] = []serverstore.SnapshotRow{{PURL: purl, Symbol: "new", SnapshotJSON: "new-positive"}}
	js, ok, err := w.SnapshotJSONWithError(t.Context(), purl, "new")
	if err != nil || !ok || js != "new-positive" || store.singleCalls.Load() != 1 {
		t.Fatalf("evicted absence suppressed a new positive: %q %t %v reads=%d", js, ok, err, store.singleCalls.Load())
	}
}

func TestSnapshotPayloadReplacementRetiresEveryOldSymbol(t *testing.T) {
	w := &webStore{}
	purl := "pkg:npm/replaced@1"
	w.cacheSnapshotRows([]serverstore.SnapshotRow{
		{PURL: purl, Symbol: "old", SnapshotJSON: "retired"},
		{PURL: purl, Symbol: "kept", SnapshotJSON: "before"},
	}, []string{purl}, time.Now())
	w.cacheSnapshotRows([]serverstore.SnapshotRow{
		{PURL: purl, Symbol: "kept", SnapshotJSON: "after"},
	}, []string{purl}, time.Now())
	js, ok, found := w.snapshotFromLoadedPURL(purl, purl+"|old", time.Now())
	if js != "" || ok || !found {
		t.Fatalf("retired symbol = %q %t %t", js, ok, found)
	}
	if _, exists := w.snapshotJSON.Load(purl + "|old"); exists {
		t.Fatal("retired payload still retained")
	}
	js, ok, found = w.snapshotFromLoadedPURL(purl, purl+"|kept", time.Now())
	if js != "after" || !ok || !found {
		t.Fatalf("replacement = %q %t %t", js, ok, found)
	}
}

func TestOversizedSnapshotRemainsCompleteWithoutCaching(t *testing.T) {
	purl := "pkg:npm/oversized@1"
	raw := strings.Repeat("x", snapshotPayloadCacheBytes+1)
	store := &farm202SnapshotStore{rows: map[string][]serverstore.SnapshotRow{
		purl: {{PURL: purl, Symbol: "large", SnapshotJSON: raw}},
	}}
	w := &webStore{s: store}
	for range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		js, ok, err := w.SnapshotJSONWithError(ctx, purl, "large")
		cancel()
		if err != nil || !ok || js != raw {
			t.Fatalf("oversized response truncated or lost: length=%d ok=%t err=%v", len(js), ok, err)
		}
	}
	if store.singleCalls.Load() != 2 {
		t.Fatalf("oversized payload was cached: reads=%d", store.singleCalls.Load())
	}
	if _, ok := w.purlsLoaded.Load(purl); ok {
		t.Fatal("uncached release incorrectly claims complete cached authority")
	}
	if w.snapshotCache.bytes != 0 {
		t.Fatalf("oversized retained bytes=%d", w.snapshotCache.bytes)
	}
}

func TestConcurrentSnapshotEvictionPreservesReadResults(t *testing.T) {
	store := &farm202SnapshotStore{rows: make(map[string][]serverstore.SnapshotRow)}
	padding := strings.Repeat("x", 2<<20)
	var purls []string
	for i := range 40 {
		purl := fmt.Sprintf("pkg:npm/concurrent-%d@1", i)
		purls = append(purls, purl)
		store.rows[purl] = []serverstore.SnapshotRow{{PURL: purl, Symbol: "value", SnapshotJSON: padding + fmt.Sprint(i)}}
	}
	w := &webStore{s: store}
	var wg sync.WaitGroup
	for _, purl := range purls {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			for range 3 {
				js, ok, err := w.SnapshotJSONWithError(t.Context(), p, "value")
				if err != nil || !ok || js != store.rows[p][0].SnapshotJSON {
					t.Errorf("concurrent eviction changed %s: ok=%t err=%v", p, ok, err)
				}
			}
		}(purl)
	}
	wg.Wait()
	w.snapshotCacheMu.Lock()
	defer w.snapshotCacheMu.Unlock()
	if w.snapshotCache.bytes > snapshotPayloadCacheBytes {
		t.Fatalf("concurrent cache exceeds budget: %d", w.snapshotCache.bytes)
	}
}

func TestCompleteCorpusDoesNotRetainRetiredPointPayloads(t *testing.T) {
	w := &webStore{}
	purl := "pkg:npm/retired-by-corpus@1"
	w.cacheSnapshotRows([]serverstore.SnapshotRow{{PURL: purl, Symbol: "old", SnapshotJSON: "old"}}, []string{purl}, time.Now())
	w.snapshotMu.Lock()
	w.replaceSnapshotCorpus([]serverstore.SnapshotRow{{PURL: purl, Symbol: "new", SnapshotJSON: "new"}})
	w.snapshotMu.Unlock()
	js, ok, err := w.SnapshotJSONWithError(t.Context(), purl, "old")
	if err != nil || ok || js != "" {
		t.Fatalf("old corpus row resurrected: %q %t %v", js, ok, err)
	}
	js, ok, err = w.SnapshotJSONWithError(t.Context(), purl, "new")
	if err != nil || !ok || js != "new" {
		t.Fatalf("complete corpus lookup failed: %q %t %v", js, ok, err)
	}
	var payloads int
	w.snapshotJSON.Range(func(_, _ any) bool { payloads++; return true })
	if payloads != 0 || w.snapshotCache.bytes != 0 {
		t.Fatalf("complete corpus retained point cache: %d entries/%d bytes", payloads, w.snapshotCache.bytes)
	}
}
