package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestFailureClusterCacheOversizedReplacementIsNotRetained(t *testing.T) {
	w := &webStore{}
	w.cacheFailureClusterPage("npm|large", cachedFailureClusters{at: time.Now(), docs: []string{"old"}, matched: 1})
	body := strings.Repeat("x", 1<<20)
	docs := make([]string, 65)
	for i := range docs {
		docs[i] = body
	}
	value := cachedFailureClusters{at: time.Now(), docs: docs, matched: 65}
	w.cacheFailureClusterPage("npm|large", value)
	if _, ok := w.loadFailureClusterPage("npm|large"); ok {
		t.Fatal("oversized replacement retained a page or its older value")
	}
	if entries, _ := retainedFailureClusterDocs(w); entries != 0 ||
		w.failureClustersCache.bytes != 0 || w.failureClustersCache.order.Len() != 0 {
		t.Fatal("oversized replacement left cache ownership or accounting behind")
	}
	if len(value.docs) != 65 || value.matched != 65 || value.docs[64] != body {
		t.Fatal("uncached complete response was truncated or mutated")
	}
}

func TestFailureClusterCacheReadKeepsHotEntryAndReplacementReleasesBudget(t *testing.T) {
	w := &webStore{}
	for i := 0; i < 1024; i++ {
		w.cacheFailureClusterPage(fmt.Sprintf("npm|%d", i), cachedFailureClusters{docs: []string{"unchanged"}, matched: 7})
	}
	if _, ok := w.loadFailureClusterPage("npm|0"); !ok {
		t.Fatal("hot page absent before pressure")
	}
	w.cacheFailureClusterPage("npm|new", cachedFailureClusters{docs: []string{"new"}})
	if _, ok := w.loadFailureClusterPage("npm|1"); ok {
		t.Fatal("least recently used page retained after capacity pressure")
	}
	if got, ok := w.loadFailureClusterPage("npm|0"); !ok || got.docs[0] != "unchanged" || got.matched != 7 {
		t.Fatal("reading a hot page did not protect its complete cached value")
	}

	replacement := &webStore{}
	replacement.cacheFailureClusterPage("npm|same", cachedFailureClusters{docs: []string{strings.Repeat("x", 1<<20)}})
	replacement.cacheFailureClusterPage("npm|same", cachedFailureClusters{docs: []string{"small"}})
	if entries, _ := retainedFailureClusterDocs(replacement); entries != 1 ||
		len(replacement.failureClustersCache.entries) != 1 || replacement.failureClustersCache.order.Len() != 1 ||
		replacement.failureClustersCache.bytes >= 1<<20 {
		t.Fatal("replacement retained the old entry or its accounted bytes")
	}
}

func TestFailureClusterCacheConcurrentNavigationStaysBounded(t *testing.T) {
	w := &webStore{s: &boundedFailureClusterStore{Fake: serverstore.NewFake()}}
	var wg sync.WaitGroup
	failures := make(chan string, 64)
	for worker := 0; worker < 64; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for page := 0; page < 25; page++ {
				name := fmt.Sprintf("worker-%d-page-%d", worker, page)
				docs, matched, err := w.FailureClusters(t.Context(), "npm", name)
				if err != nil || len(docs) != 1 || matched != 7 || !strings.Contains(docs[0], name) {
					failures <- name
					return
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for name := range failures {
		t.Errorf("concurrent complete response changed for %s", name)
	}
	entries, bytes := retainedFailureClusterDocs(w)
	if entries > 1024 || bytes > 64<<20 {
		t.Fatalf("concurrent cache retained %d entries/%d bytes", entries, bytes)
	}
	if entries != len(w.failureClustersCache.entries) || entries != w.failureClustersCache.order.Len() ||
		w.failureClustersCache.bytes > 64<<20 {
		t.Fatal("concurrent navigation left orphaned ownership or exceeded accounted budget")
	}
}
