package httpapi

import (
	"sync"
	"time"
)

// The shard documents, and why GET /v1/shards stopped reading them per
// request.
//
// A shard is replaced only by an aggregation pass -- the cadence the daily
// rollup (stats.go) and the hot-shard hint are already remembered for. Reading
// it per request put every sync behind the shared pool and a JSONB detoast of
// the whole document. On 2026-11-10 production v0.2.23 answered
// /v1/shards/npm/zod/3 (151 KB) with an application time of 0.49 s median and
// 1.68 s p95 -- db_wait up to 0.88 s, query_handler up to 0.96 s -- while
// /version, which reads nothing, spent 0.03 ms (#639). A revalidation cost the
// same: the ETag-only read waits for the same pool.
//
// So the last document this process read for a key is answered from memory,
// revalidations included, for one builder cadence. That adds at most one
// cadence to how long a rebuilt or retired shard takes to reach a client --
// the bound the rollup and the hint already carry -- and nothing else: an
// absent shard is never remembered, so a new key is served the moment it is
// written, and the memory is bounded so a sweep over every key cannot hold
// the corpus.
const (
	// defaultShardCacheTTL is used only by zero-valued configuration.
	// Production derives the lifetime from CSX_SNAPSHOT_INTERVAL so a document
	// cannot outlive the builder cadence that replaces it.
	defaultShardCacheTTL = 5 * time.Minute
	// shardCacheMaxBytes bounds the remembered documents on a 2 GB host; the
	// hottest shard is ~151 KB, so this holds the synced set with room to spare.
	shardCacheMaxBytes = 32 << 20
	// shardCacheMaxEntries bounds eviction's scan when the documents are small.
	shardCacheMaxEntries = 1024
)

// shardCache is this process's memory of shard documents: per key, the last
// one read and when that read began.
type shardCache struct {
	mu      sync.Mutex
	entries map[string]shardCacheEntry
	bytes   int
}

type shardCacheEntry struct {
	etag       string
	doc        string
	at         time.Time
	lastAccess time.Time
}

func (e shardCacheEntry) size(key string) int { return len(key) + len(e.etag) + len(e.doc) }

func (a *api) shardCacheTTL() time.Duration {
	if a.d.Cfg.SnapshotInterval > 0 {
		return a.d.Cfg.SnapshotInterval
	}
	return defaultShardCacheTTL
}

// cachedShard returns the remembered document for key while it is younger
// than one builder cadence.
func (a *api) cachedShard(key string) (shardCacheEntry, bool) {
	now := a.now()
	c := &a.shards
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return shardCacheEntry{}, false
	}
	if now.Sub(e.at) >= a.shardCacheTTL() {
		c.remove(key)
		return shardCacheEntry{}, false
	}
	// Keep frequently requested shards resident under key churn. Freshness
	// still expires from at, so accesses never extend the builder-cadence TTL.
	e.lastAccess = now
	c.entries[key] = e
	return e, true
}

// rememberShard keeps a document read from the store. at is when the read
// began, so the cadence is counted from the oldest moment the document can
// describe, and a slow read that finishes after a newer one cannot replace it.
func (a *api) rememberShard(key, etag, doc string, at time.Time) {
	e := shardCacheEntry{etag: etag, doc: doc, at: at, lastAccess: at}
	size := e.size(key)
	if size > shardCacheMaxBytes {
		return
	}
	c := &a.shards
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		if old.at.After(at) {
			return
		}
		c.remove(key)
	}
	for len(c.entries) > 0 && (c.bytes+size > shardCacheMaxBytes || len(c.entries) >= shardCacheMaxEntries) {
		c.remove(c.oldest())
	}
	if c.entries == nil {
		c.entries = map[string]shardCacheEntry{}
	}
	c.entries[key] = e
	c.bytes += size
}

// oldest is the least recently accessed key. Callers hold mu.
func (c *shardCache) oldest() string {
	var key string
	var at time.Time
	for k, e := range c.entries {
		lastAccess := e.lastAccess
		if lastAccess.IsZero() {
			lastAccess = e.at
		}
		if key == "" || lastAccess.Before(at) {
			key, at = k, lastAccess
		}
	}
	return key
}

// remove forgets key. Callers hold mu.
func (c *shardCache) remove(key string) {
	if e, ok := c.entries[key]; ok {
		c.bytes -= e.size(key)
		delete(c.entries, key)
	}
}
