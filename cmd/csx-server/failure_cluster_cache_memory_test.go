package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type boundedFailureClusterStore struct {
	*serverstore.Fake
	body  string
	reads atomic.Int64
	fail  atomic.Bool
}

func (s *boundedFailureClusterStore) ListFailureClustersForPage(_ context.Context, ecosystem, name string, _ int) ([]serverstore.ClusterRow, int, error) {
	s.reads.Add(1)
	if s.fail.Load() {
		return nil, 0, errors.New("refresh unavailable")
	}
	return []serverstore.ClusterRow{{
		Ecosystem: ecosystem, PackageName: name, Symbol: "Example",
		Stage: "contract", ErrorFingerprint: name, ErrorSummary: s.body,
		ObservationCount: 7,
	}}, 7, nil
}

func retainedFailureClusterDocs(w *webStore) (entries, bytes int) {
	w.pkgFailureClusters.Range(func(key, value any) bool {
		entries++
		bytes += len(key.(string))
		for _, doc := range value.(cachedFailureClusters).docs {
			bytes += len(doc)
		}
		return true
	})
	return
}

// A freshness TTL is not a retention limit: navigation to new packages must
// release old cached documents while an evicted package remains reloadable.
func TestFailureClusterDisplayCacheEntryBoundAndReload(t *testing.T) {
	store := &boundedFailureClusterStore{Fake: serverstore.NewFake()}
	w := &webStore{s: store}
	first, matched, err := w.FailureClusters(t.Context(), "npm", "package-0")
	if err != nil || len(first) != 1 || matched != 7 {
		t.Fatalf("first page=%d matched=%d err=%v", len(first), matched, err)
	}
	for i := 1; i <= 1024; i++ {
		docs, total, err := w.FailureClusters(t.Context(), "npm", fmt.Sprintf("package-%d", i))
		if err != nil || len(docs) != 1 || total != 7 {
			t.Fatalf("page %d=%d matched=%d err=%v", i, len(docs), total, err)
		}
	}
	if entries, _ := retainedFailureClusterDocs(w); entries > 1024 {
		t.Errorf("retained %d package entries; want at most 1024", entries)
	}
	before := store.reads.Load()
	reloaded, total, err := w.FailureClusters(t.Context(), "npm", "package-0")
	if err != nil || len(reloaded) != 1 || reloaded[0] != first[0] || total != matched {
		t.Fatalf("evicted page changed: matched=%d err=%v", total, err)
	}
	if got := store.reads.Load() - before; got != 1 {
		t.Errorf("evicted package store reads=%d, want 1", got)
	}
}

// Ordinary display pages can contain large diagnostic strings even with the
// row cap. Sum the documents reachable from the cache, not total allocations.
func TestFailureClusterDisplayCacheByteBound(t *testing.T) {
	store := &boundedFailureClusterStore{Fake: serverstore.NewFake(), body: strings.Repeat("x", 1<<20)}
	w := &webStore{s: store}
	for i := 0; i < 65; i++ {
		docs, matched, err := w.FailureClusters(t.Context(), "npm", fmt.Sprintf("large-%d", i))
		if err != nil || len(docs) != 1 || matched != 7 || !strings.Contains(docs[0], store.body) {
			t.Fatalf("complete large page %d: docs=%d matched=%d err=%v", i, len(docs), matched, err)
		}
	}
	if _, bytes := retainedFailureClusterDocs(w); bytes > 64<<20 {
		t.Fatalf("retained %d document bytes; want at most 64 MiB", bytes)
	}
}

func TestFailureClusterDisplayCacheRetainsStaleOnRefreshFailure(t *testing.T) {
	store := &boundedFailureClusterStore{Fake: serverstore.NewFake()}
	w := &webStore{s: store}
	docs, matched, err := w.FailureClusters(t.Context(), "npm", "stale")
	if err != nil || len(docs) != 1 {
		t.Fatalf("seed page: %v", err)
	}
	key := "npm|stale"
	value, _ := w.pkgFailureClusters.Load(key)
	entry := value.(cachedFailureClusters)
	entry.at = time.Now().Add(-2 * packageDetailCacheTTL)
	w.pkgFailureClusters.Store(key, entry)
	store.fail.Store(true)
	got, total, err := w.FailureClusters(t.Context(), "npm", "stale")
	if err != nil || len(got) != 1 || got[0] != docs[0] || total != matched {
		t.Fatalf("stale response changed: matched=%d err=%v", total, err)
	}
	deadline := time.Now().Add(time.Second)
	for store.reads.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if store.reads.Load() < 2 {
		t.Fatal("stale entry did not start its existing background refresh")
	}
	kept, ok := w.pkgFailureClusters.Load(key)
	if !ok || kept.(cachedFailureClusters).docs[0] != docs[0] {
		t.Fatal("failed refresh discarded the retained stale page")
	}
}
