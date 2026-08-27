package httpapi

// The hotShards warming hint, and why it is not allowed to decide how long
// GET /v1/stats takes.
//
// R2C-159: three of four unattended production rollouts either failed on, or
// barely survived, the deploy's first request through the proxy -- a
// GET /v1/stats with a ten-second ceiling. Caddy recorded that request with
// status 0 (the client gave up) at 20:27:32Z and 23:11:14Z, and with 200 at
// 23:30:44Z, nine seconds into the same budget.
//
// Measured against production with an idle database, /healthz -- which reads
// the same stats row through the store -- answers in 36ms while /v1/stats
// answers in 337-458ms. About ninety percent of the endpoint is this hint,
// and the hint is four whole-corpus reads recomputed for every caller. A
// freshly deployed server must complete a full builder pass before the deploy
// will accept it; that pass owns the database for roughly fourteen minutes,
// and during it the same reads take an order of magnitude longer. Every
// statement stayed inside its own eight-second interactive ceiling while the
// request as a whole crossed ten seconds: the ceilings were per statement,
// and nothing bounded the request.
//
// So the hint gets its own budget, and three properties:
//
//   - A caller waits hotShardRequestWait for it and no longer. Past that the
//     stats document is served without the key, which is what withHotShards
//     already did for every other failure.
//   - The read a caller stopped waiting for is not abandoned. It is detached
//     from that caller, keeps its own hotShardLoadTimeout, and publishes what
//     it finds, so under pressure the hint comes back on its own without any
//     request paying for it twice.
//   - Simultaneous callers share one read -- the fleet polls this endpoint --
//     and a computed answer is reused for hotShardTTL.
//
// An EMPTY answer is deliberately not remembered. Absence means no shard has
// been built yet, which is the one state a first builder pass is about to
// change, and a fresh install needs the hint the moment it exists.

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const (
	// hotShardLimit is how many shard keys a client is told to warm. Each is
	// one small HTTP GET; the point is that a fresh install has something
	// cached before its first search, not that it mirrors the network.
	hotShardLimit = 50
	// hotShardRequestWait is the only time GET /v1/stats spends on the hint.
	// It is about five times the healthy measured cost of the read and about
	// five times below the ceiling the production deploy smoke puts on this
	// endpoint, so a healthy server always carries the key and a saturated
	// one still answers.
	hotShardRequestWait = 2 * time.Second
	// hotShardLoadTimeout bounds the shared read, which deliberately outlives
	// the caller that started it. Each statement inside it is still capped by
	// the interactive class's own eight-second ceiling; this bounds the
	// sequence, so a read under pressure can still finish and fill the cache.
	hotShardLoadTimeout = 30 * time.Second
	// defaultHotShardTTL is used only by zero-valued test configuration.
	// Production derives the lifetime from CSX_SNAPSHOT_INTERVAL so the hint
	// cannot outlive (or needlessly refresh inside) the configured builder
	// cadence.
	defaultHotShardTTL = 5 * time.Minute
)

// hotShardHint is this process's view of the hint: the last answer it
// computed, when it computed it, and the single read currently in flight.
type hotShardHint struct {
	mu      sync.Mutex
	keys    []string
	at      time.Time
	loading chan struct{}
}

// hotShardKeys reports the keys to advertise, or nil when this process has
// none it can produce inside the caller's budget. It never blocks longer than
// that budget, and it never abandons a read it started.
func (a *api) hotShardKeys(ctx context.Context) []string {
	wait := a.d.hotShardWait
	if wait <= 0 {
		wait = hotShardRequestWait
	}
	a.hotShards.mu.Lock()
	if a.hotShards.keys != nil && a.now().Sub(a.hotShards.at) < a.hotShardTTL() {
		keys := a.hotShards.keys
		a.hotShards.mu.Unlock()
		return keys
	}
	done := a.hotShards.loading
	if done == nil {
		done = make(chan struct{})
		a.hotShards.loading = done
		go a.loadHotShards(ctx, done)
	}
	a.hotShards.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}

	a.hotShards.mu.Lock()
	defer a.hotShards.mu.Unlock()
	// Whatever this process last computed, even when the refresh it waited on
	// has not finished: a hint one interval old is still the hint, and
	// dropping it would leave a fresh install with nothing to warm.
	return a.hotShards.keys
}

func (a *api) hotShardTTL() time.Duration {
	if a.d.Cfg.SnapshotInterval > 0 {
		return a.d.Cfg.SnapshotInterval
	}
	return defaultHotShardTTL
}

// loadHotShards performs the one shared whole-corpus read and publishes it.
func (a *api) loadHotShards(ctx context.Context, done chan struct{}) {
	// Detached from the caller on purpose: the read is shared, so a client
	// that hung up must neither abandon the callers waiting with it nor make
	// the next poll pay for the same four whole-corpus reads. The caller's
	// class travels with the context; only its cancellation is dropped.
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hotShardLoadTimeout)
	defer cancel()
	keys, err := a.d.Store.HotShardKeys(loadCtx, hotShardLimit)

	a.hotShards.mu.Lock()
	defer a.hotShards.mu.Unlock()
	if err == nil && len(keys) > 0 {
		a.hotShards.keys = keys
		a.hotShards.at = a.now()
	}
	a.hotShards.loading = nil
	close(done)
}

// withHotShards adds the "hotShards" key to a stats document. Clients read
// it to decide what to warm (daemon.fetchHot), and a fresh install has no
// local package history to warm from — without this key its cache stays
// empty and every search answers "no cached data". A failure or a budget
// this hint could not meet degrades to the stats document unchanged rather
// than failing, or delaying, the request.
func (a *api) withHotShards(ctx context.Context, statsJSON string) string {
	keys := a.hotShardKeys(ctx)
	if len(keys) == 0 {
		return statsJSON
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(statsJSON), &doc); err != nil {
		return statsJSON
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return statsJSON
	}
	doc["hotShards"] = raw
	merged, err := json.Marshal(doc)
	if err != nil {
		return statsJSON
	}
	return string(merged)
}
