package httpapi

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
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
// backpressure serves it instead of refusing. Its age is in the document, in
// the generatedAt the caller already reads; a 503 is not the more honest
// answer, it is only the less useful one. Two limits keep that from becoming
// a licence to invent: with nothing yet cached the pressure is still reported,
// and a fault that is NOT backpressure keeps its own status rather than being
// laundered into a stale 200.
const (
	// latestStatsFailureBackoff bounds re-reads while the read keeps failing,
	// so a fleet polling a starved database cannot turn one refusal into one
	// refused read per caller.
	latestStatsFailureBackoff = time.Second
	// defaultLatestStatsTTL is used only by zero-valued configuration.
	// Production derives the lifetime from CSX_SNAPSHOT_INTERVAL so the cache
	// cannot outlive the builder cadence that replaces the document.
	defaultLatestStatsTTL = 5 * time.Minute
)

// latestStatsCache is this process's memory of the rollup: the last one read,
// when it was read, and how long to leave a failing read alone.
type latestStatsCache struct {
	mu     sync.Mutex
	doc    string
	at     time.Time
	have   bool
	failAt time.Time
	failed error
}

func (a *api) latestStatsTTL() time.Duration {
	if a.d.Cfg.SnapshotInterval > 0 {
		return a.d.Cfg.SnapshotInterval
	}
	return defaultLatestStatsTTL
}

// latestStats answers with the stored rollup, preferring the remembered one
// inside a builder cadence and falling back to it when the read is refused.
// It reports (doc, false, nil) exactly where the store does: no rollup has
// been written yet, and the caller computes a live one.
func (a *api) latestStats(ctx context.Context) (string, bool, error) {
	c := &a.statsCache
	c.mu.Lock()
	defer c.mu.Unlock()

	now := a.now()
	if c.have && now.Sub(c.at) < a.latestStatsTTL() {
		return c.doc, true, nil
	}
	// Still inside the backoff from a failed read. Answer the way that read
	// would have: the remembered rollup, or the failure it produced.
	if now.Before(c.failAt) {
		if c.have && isBackpressure(c.failed) {
			return c.doc, true, nil
		}
		return "", false, c.failed
	}

	js, ok, err := a.d.Store.GetLatestStats(ctx)
	now = a.now()
	if err != nil {
		c.failAt, c.failed = now.Add(latestStatsFailureBackoff), err
		// Backpressure is not an answer when a real one is already in hand.
		// Any other fault keeps its own meaning and its own status.
		if c.have && isBackpressure(err) {
			return c.doc, true, nil
		}
		return "", false, err
	}
	c.failAt, c.failed = time.Time{}, nil
	if ok {
		c.doc, c.at, c.have = js, now, true
	}
	return js, ok, nil
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
		raw, jerr := compatibility.StatsJSON(counts, adopt, now)
		if jerr != nil {
			writeErr(w, http.StatusInternalServerError, "stats rollup failed")
			return
		}
		js = string(raw)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(w, a.withHotShards(r.Context(), js))
}
