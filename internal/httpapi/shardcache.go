package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/servertiming"
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
//
// Remembering alone still left one request per cadence on the store: the
// first one after the document expired. That request is the slow tail the
// performance SLO probe measures (#695: /v1/shards/npm/zod/3 at 1.07 s median
// and 1.64 s p95 on 2026-11-12, 7.07 s p95 the day before), and while it
// waits every other request for the key misses too. So a document that is
// requested in the last fifth of its cadence is reread once, detached from
// the request that noticed, and the request is answered from memory. A hot
// shard therefore never expires under steady traffic, while freshness keeps
// its bound: a document is served only while younger than one cadence, and
// the reread starts the new document's cadence from when that read began.
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
	// shardRefreshTimeout bounds a detached reread, which outlives the
	// request that started it; the same bound the hot-shard hint's shared
	// read carries.
	shardRefreshTimeout = 30 * time.Second
)

// shardCache is this process's memory of shard documents: per key, the last
// one read and when that read began, and the reread in flight for a key
// close to expiry.
type shardCache struct {
	mu         sync.Mutex
	entries    map[string]shardCacheEntry
	bytes      int
	refreshing map[string]chan struct{}
}

type shardCacheEntry struct {
	etag       string
	doc        string
	at         time.Time
	lastAccess time.Time
	retry      retrypolicy.Series
	retryAt    time.Time
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

// refreshShardAhead starts one detached reread of key when the remembered
// document e has entered the last fifth of its cadence. The caller is
// answered from e either way; at most one reread per key is in flight, and
// failed reads back off within a bounded series for that document.
func (a *api) refreshShardAhead(ctx context.Context, key string, e shardCacheEntry) {
	now := a.now()
	ttl := a.shardCacheTTL()
	if now.Sub(e.at) < ttl-ttl/5 {
		return
	}
	c := &a.shards
	c.mu.Lock()
	held, ok := c.entries[key]
	if !ok || !held.at.Equal(e.at) || now.Before(held.retryAt) {
		c.mu.Unlock()
		return
	}
	if _, busy := c.refreshing[key]; busy {
		c.mu.Unlock()
		return
	}
	if c.refreshing == nil {
		c.refreshing = map[string]chan struct{}{}
	}
	done := make(chan struct{})
	c.refreshing[key] = done
	c.mu.Unlock()
	go a.refreshShard(servertiming.Detached(ctx), key, e.etag, now, done, held.retry.State() == retrypolicy.Waiting)
}

// refreshShard rereads key for refreshShardAhead. readAt is no later than
// the moment the read begins, so the new document's cadence is never
// overstated. An unchanged ETag keeps the remembered body instead of
// detoasting the whole document again. Failed reads back off without changing
// freshness, so the document still expires on schedule. A successful read
// replaces the entry, resetting its retry series along with its cadence.
func (a *api) refreshShard(ctx context.Context, key, etag string, readAt time.Time, done chan struct{}, retry bool) {
	c := &a.shards
	var err error
	defer func() {
		c.mu.Lock()
		if err != nil {
			// An older failure must not suppress refreshes of a document
			// replaced by a newer foreground read while this one ran.
			if e, ok := c.entries[key]; ok && !e.at.After(readAt) {
				next, state := e.retry.Failure()
				if state == retrypolicy.Waiting {
					delay, _ := retrypolicy.Delay(next, nil)
					e.retryAt = a.now().Add(delay)
				} else {
					// Exhaustion defers the optional work for a cadence.
					// The entry expires first; requests still read fresh data.
					e.retryAt = a.now().Add(a.shardCacheTTL())
				}
				c.entries[key] = e
			}
		}
		delete(c.refreshing, key)
		c.mu.Unlock()
		close(done)
	}()
	// Detached work is background work; it must not keep the request's
	// interactive pool class.
	budget := serverstore.NewQueryBudget(serverstore.ClassBackground)
	if retry {
		budget = serverstore.NewRetryQueryBudget(serverstore.ClassBackground)
	}
	ctx = serverstore.WithQueryBudget(ctx, budget)
	ctx, cancel := context.WithTimeout(ctx, shardRefreshTimeout)
	defer cancel()

	current, ok, err := a.d.Store.GetShardEtag(ctx, key)
	if err != nil {
		return
	}
	if !ok {
		a.forgetShard(key, readAt)
		return
	}
	if current == etag {
		c.mu.Lock()
		old, held := c.entries[key]
		c.mu.Unlock()
		if held && old.etag == current {
			a.rememberShard(key, current, old.doc, readAt)
			return
		}
	}
	current, doc, ok, err := a.d.Store.GetShard(ctx, key)
	if err != nil {
		return
	}
	if !ok {
		a.forgetShard(key, readAt)
		return
	}
	a.rememberShard(key, current, doc, readAt)
}

// forgetShard drops key unless what is remembered was read after at: a shard
// retired by a pass stops being served, but a newer read is kept.
func (a *api) forgetShard(key string, at time.Time) {
	c := &a.shards
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && !e.at.After(at) {
		c.remove(key)
	}
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
