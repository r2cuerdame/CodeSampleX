package main

import "container/list"

// Bound retained package display documents, as the snapshot payload cache
// already does. This accounts cached payloads and entry overhead, not total Go
// heap/RSS or in-flight responses. Eviction is a reload, never package absence.
const (
	failureClusterCacheBytes    = 64 << 20
	failureClusterCachePackages = 1024
)

type failureClusterCacheEntry struct {
	key   string
	bytes int
}

type failureClusterDisplayCache struct {
	order   list.List
	entries map[string]*list.Element
	bytes   int
}

func (w *webStore) loadFailureClusterPage(key string) (cachedFailureClusters, bool) {
	w.failureClustersCacheMu.Lock()
	defer w.failureClustersCacheMu.Unlock()
	value, ok := w.pkgFailureClusters.Load(key)
	if !ok {
		return cachedFailureClusters{}, false
	}
	if element := w.failureClustersCache.entries[key]; element != nil {
		w.failureClustersCache.order.MoveToBack(element)
	}
	return value.(cachedFailureClusters), true
}

// Caller holds failureClustersCacheMu.
func (w *webStore) evictFailureClusterPage(key string) {
	w.pkgFailureClusters.Delete(key)
	element := w.failureClustersCache.entries[key]
	if element == nil {
		return
	}
	entry := element.Value.(*failureClusterCacheEntry)
	w.failureClustersCache.bytes -= entry.bytes
	delete(w.failureClustersCache.entries, key)
	w.failureClustersCache.order.Remove(element)
}

func (w *webStore) cacheFailureClusterPage(key string, value cachedFailureClusters) {
	size := 256 + len(key) + 16*cap(value.docs)
	for _, doc := range value.docs {
		// Saturate before adding untrusted document lengths.
		if size > failureClusterCacheBytes || len(doc) > failureClusterCacheBytes-size {
			size = failureClusterCacheBytes + 1
			break
		}
		size += len(doc)
	}

	w.failureClustersCacheMu.Lock()
	defer w.failureClustersCacheMu.Unlock()
	if w.failureClustersCache.entries == nil {
		w.failureClustersCache.entries = make(map[string]*list.Element)
	}
	w.evictFailureClusterPage(key)
	// FailureClusters still returns the complete loaded page to its coalesced
	// callers. An oversized page simply does not become a retained cache entry.
	if size > failureClusterCacheBytes {
		return
	}
	for w.failureClustersCache.bytes+size > failureClusterCacheBytes ||
		len(w.failureClustersCache.entries) >= failureClusterCachePackages {
		oldest := w.failureClustersCache.order.Front().Value.(*failureClusterCacheEntry)
		w.evictFailureClusterPage(oldest.key)
	}
	entry := &failureClusterCacheEntry{key: key, bytes: size}
	w.pkgFailureClusters.Store(key, value)
	w.failureClustersCache.entries[key] = w.failureClustersCache.order.PushBack(entry)
	w.failureClustersCache.bytes += size
}
