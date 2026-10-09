package main

import (
	"container/list"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Expiration alone does not release a sync.Map value. Evict complete PURLs
// together with their authority stamps so eviction is a reload, never absence.
const (
	snapshotPayloadCacheBytes = 64 << 20
	snapshotPayloadCachePURLs = 1024
)

type snapshotPayloadEntry struct {
	purl  string
	keys  []string
	bytes int
}

type snapshotPayloadCache struct {
	order   list.List
	entries map[string]*list.Element
	bytes   int
}

// Caller holds snapshotCacheMu.
func (w *webStore) evictSnapshotPURL(purl string) {
	element := w.snapshotCache.entries[purl]
	if element == nil {
		return
	}
	entry := element.Value.(*snapshotPayloadEntry)
	for _, key := range entry.keys {
		w.snapshotJSON.Delete(key)
	}
	w.purlsLoaded.Delete(purl)
	w.snapshotCache.bytes -= entry.bytes
	delete(w.snapshotCache.entries, purl)
	w.snapshotCache.order.Remove(element)
}

func (w *webStore) cacheSnapshotRows(rows []serverstore.SnapshotRow, purls []string, at time.Time) {
	groups := make(map[string][]serverstore.SnapshotRow, len(purls))
	for _, row := range rows {
		groups[row.PURL] = append(groups[row.PURL], row)
	}
	w.snapshotCacheMu.Lock()
	defer w.snapshotCacheMu.Unlock()
	if w.snapshotCache.entries == nil {
		w.snapshotCache.entries = make(map[string]*list.Element)
	}
	for _, purl := range purls {
		w.evictSnapshotPURL(purl)
		size := len(purl) + 64
		for _, row := range groups[purl] {
			size += len(row.PURL) + len(row.Symbol) + 1 + len(row.SnapshotJSON) + 64
		}
		// Oversized releases remain valid uncached responses. Shared loads
		// return their complete rows directly to every waiting reader.
		if size > snapshotPayloadCacheBytes {
			continue
		}
		for w.snapshotCache.bytes+size > snapshotPayloadCacheBytes || len(w.snapshotCache.entries) >= snapshotPayloadCachePURLs {
			w.evictSnapshotPURL(w.snapshotCache.order.Front().Value.(*snapshotPayloadEntry).purl)
		}
		entry := &snapshotPayloadEntry{purl: purl, bytes: size}
		for _, row := range groups[purl] {
			key := row.PURL + "|" + row.Symbol
			entry.keys = append(entry.keys, key)
			w.snapshotJSON.Store(key, cachedSnapshotJSON{at: at, json: row.SnapshotJSON, ok: true})
		}
		w.purlsLoaded.Store(purl, at)
		w.snapshotCache.entries[purl] = w.snapshotCache.order.PushBack(entry)
		w.snapshotCache.bytes += size
	}
}

// Caller holds snapshotMu. The complete immutable corpus has its own compact
// index and must not also fill an independently retained per-PURL payload map.
func (w *webStore) replaceSnapshotCorpus(rows []serverstore.SnapshotRow) {
	index := make(map[string]int, len(rows))
	for i, row := range rows {
		index[row.PURL+"|"+row.Symbol] = i
	}
	w.snapshotRows, w.snapshotAt, w.snapshotLookup = rows, time.Now(), index
	w.snapshotCacheMu.Lock()
	defer w.snapshotCacheMu.Unlock()
	w.snapshotJSON.Range(func(key, _ any) bool { w.snapshotJSON.Delete(key); return true })
	w.purlsLoaded.Range(func(key, _ any) bool { w.purlsLoaded.Delete(key); return true })
	w.snapshotCache = snapshotPayloadCache{}
}

func snapshotFromRows(rows []serverstore.SnapshotRow, purl, symbol string) (string, bool, error) {
	for _, row := range rows {
		if row.PURL == purl && row.Symbol == symbol {
			return row.SnapshotJSON, true, nil
		}
	}
	return "", false, nil
}
