package httpapi

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/servertiming"
)

// The daily rollup, and why this endpoint stopped reading it per request.
//
// The document changes at most once per builder pass. During the 2026-09-09
// #174 incident production's carried generatedAt 2026-09-08T19:49:36Z for over
// a day while every caller still paid a database read to be told so, and live
// probes measured GET /v1/stats refusing 5 of 6 requests with 503 "database
// busy" -- on a box whose problem was that it had no capacity to spare. The
// read is also the only part of the endpoint that can fail: withHotShards is
// already bounded and omits its hint rather than failing the request.
//
// So the last rollup this process read is kept for one builder cadence, and
// an expired rollup stays available while one background refresh checks for
// the builder's next pass. Backpressure also serves it instead of refusing.
// Its age is in the document, in the generatedAt the caller already reads;
// a 503 is not the more honest answer, it is only the less useful one. With
// nothing yet cached the pressure is still reported, and a fault that is NOT
// backpressure keeps its own status once the refresh has reported it.
const (
	// latestStatsFailureBackoff bounds re-reads while the read keeps failing,
	// so a fleet polling a starved database cannot turn one refusal into one
	// refused read per caller.
	latestStatsFailureBackoff = time.Second
	// latestStatsReadTimeout bounds the shared read, which every other caller
	// waits behind. It is the healthz budget: past it the answer is the
	// remembered rollup, not a longer wait.
	latestStatsReadTimeout = healthzTimeout
	// defaultLatestStatsTTL is used only by zero-valued configuration.
	// Production derives the lifetime from CSX_SNAPSHOT_INTERVAL so the cache
	// cannot outlive the builder cadence that replaces the document.
	defaultLatestStatsTTL = 5 * time.Minute
)

// latestStatsCache is this process's memory of the rollup: the last one read,
// when it was read, whether a refresh is in flight, and how long to leave a
// failing read alone.
type latestStatsCache struct {
	mu      sync.Mutex
	doc     string
	at      time.Time
	have    bool
	loading bool
	failAt  time.Time
	failed  error
}

func (a *api) latestStatsTTL() time.Duration {
	if a.d.Cfg.SnapshotInterval > 0 {
		return a.d.Cfg.SnapshotInterval
	}
	return defaultLatestStatsTTL
}

// latestStats answers with the stored rollup, preferring the remembered one
// inside a builder cadence and serving it while an expired value refreshes.
// It reports (doc, false, nil) exactly where the store does: no rollup has
// been written yet, and the caller computes a live one.
func (a *api) latestStats(ctx context.Context) (string, bool, error) {
	c := &a.statsCache
	c.mu.Lock()

	now := a.now()
	if c.have && now.Sub(c.at) < a.latestStatsTTL() {
		doc := c.doc
		c.mu.Unlock()
		return doc, true, nil
	}
	// Still inside the backoff from a failed read. Answer the way that read
	// would have: the remembered rollup, or the failure it produced.
	if now.Before(c.failAt) {
		if c.have && isBackpressure(c.failed) {
			doc := c.doc
			c.mu.Unlock()
			return doc, true, nil
		}
		err := c.failed
		c.mu.Unlock()
		return "", false, err
	}
	if c.have {
		// A rollup changes only when the builder publishes another pass.
		// Once this one reaches its cadence, serve its self-dated document
		// while one lower-priority read checks for a replacement. The old
		// rollup remains honest (generatedAt is unchanged) and a slow database
		// cannot hold every polling client behind this cache lock.
		if c.loading {
			doc := c.doc
			c.mu.Unlock()
			return doc, true, nil
		}
		c.loading = true
		doc := c.doc
		c.mu.Unlock()
		go a.refreshLatestStats(ctx)
		return doc, true, nil
	}

	// The read is shared: this caller holds the lock every other caller is
	// waiting behind, so it must not run on this caller's context. A client
	// that hangs up -- which is exactly what a starved box produces -- would
	// otherwise cancel the read for everyone, and a cancellation is not
	// backpressure (IsQueryTimeout excludes it by message on purpose), so the
	// rollup already in hand would be passed over in favour of a 500. This is
	// the same reason databaseHealth loads WithoutCancel.
	js, ok, err := a.readLatestStats(ctx, false)
	now = a.now()
	if err != nil {
		c.failAt, c.failed = now.Add(latestStatsFailureBackoff), err
		// Backpressure is not an answer when a real one is already in hand.
		// Any other fault keeps its own meaning and its own status.
		if c.have && isBackpressure(err) {
			doc := c.doc
			c.mu.Unlock()
			return doc, true, nil
		}
		c.mu.Unlock()
		return "", false, err
	}
	c.failAt, c.failed = time.Time{}, nil
	if ok {
		c.doc, c.at, c.have = js, now, true
	}
	c.mu.Unlock()
	return js, ok, nil
}

// refreshLatestStats checks for a newer rollup without tying the query to the
// request that happened to notice expiry. Background accounting keeps the
// refresh from competing with foreground API queries for the reserved pool.
func (a *api) refreshLatestStats(ctx context.Context) {
	js, ok, err := a.readLatestStats(ctx, true)
	now := a.now()
	c := &a.statsCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loading = false
	if err != nil {
		c.failAt, c.failed = now.Add(latestStatsFailureBackoff), err
		return
	}
	c.failAt, c.failed = time.Time{}, nil
	if ok {
		c.doc, c.at, c.have = js, now, true
		return
	}
	// A successful empty read means the stored rollup was removed. Do not
	// keep serving it; the handler will return to the existing live-rollup
	// path on its next request.
	c.doc, c.at, c.have = "", time.Time{}, false
}

func (a *api) readLatestStats(ctx context.Context, background bool) (string, bool, error) {
	loadCtx := context.WithoutCancel(ctx)
	if background {
		loadCtx = servertiming.Detached(loadCtx)
		loadCtx = serverstore.WithQueryBudget(loadCtx, serverstore.NewQueryBudget(serverstore.ClassBackground))
	}
	loadCtx, cancel := context.WithTimeout(loadCtx, latestStatsReadTimeout)
	defer cancel()
	return a.d.Store.GetLatestStats(loadCtx)
}

// handleStats implements GET /v1/stats: the latest builder-generated daily
// rollup. Before the first builder pass it computes a live rollup so the
// endpoint never 404s. estimatedReasoningAvoided ALWAYS carries
// "estimated": true — the dashboard never presents an estimate as a
// measurement.
func (a *api) handleStats(w http.ResponseWriter, r *http.Request) {
	js, ok, err := a.latestStats(r.Context())
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "stats lookup failed")
		return
	}
	var keys []string
	if !ok {
		now := a.now()
		counts, cerr := a.d.Store.NetworkCounts(r.Context(), now)
		if cerr != nil {
			writeErr(w, http.StatusInternalServerError, "stats rollup failed")
			return
		}
		// A live rollup reads the adoption reports too; failing to read
		// them would publish a rate of zero, which is a claim rather than
		// a gap.
		adopt, aerr := a.d.Store.AdoptionSummary(r.Context())
		if aerr != nil {
			writeErr(w, http.StatusInternalServerError, "stats rollup failed")
			return
		}
		keys = a.hotShardKeys(r.Context())
		servertiming.Serialization(r.Context())
		raw, jerr := compatibility.StatsJSON(counts, adopt, now)
		if jerr != nil {
			writeErr(w, http.StatusInternalServerError, "stats rollup failed")
			return
		}
		js = string(raw)
	} else {
		keys = a.hotShardKeys(r.Context())
		servertiming.Serialization(r.Context())
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	body := a.statsResponse(js, keys)
	_, _ = io.WriteString(w, body)
}

// statsResponse returns the cached wire representation when the rollup and
// optional hint have not changed. The endpoint is polled frequently while
// both inputs change only on their respective builder cadence.
func (a *api) statsResponse(doc string, keys []string) string {
	a.statsResponseMu.Lock()
	defer a.statsResponseMu.Unlock()
	if a.statsResponseBody != "" && a.statsResponseDoc == doc && equalStrings(a.statsResponseKeys, keys) {
		return a.statsResponseBody
	}
	body := withHotShardKeys(doc, keys)
	a.statsResponseDoc = doc
	a.statsResponseKeys = append(a.statsResponseKeys[:0], keys...)
	a.statsResponseBody = body
	return body
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
