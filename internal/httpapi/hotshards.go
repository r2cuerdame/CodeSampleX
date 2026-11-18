package httpapi

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/servertiming"
)

const (
	hotShardLimit       = 50
	hotShardRequestWait = 25 * time.Millisecond
	hotShardLoadTimeout = 30 * time.Second
	defaultHotShardTTL  = 5 * time.Minute
)

type hotShardHint struct {
	mu      sync.Mutex
	keys    []string
	at      time.Time
	failing bool
	loading chan struct{}
	retry   retrypolicy.Series
	retryAt time.Time
}

func (a *api) hotShardKeys(ctx context.Context) []string {
	wait := a.d.hotShardWait
	if wait <= 0 {
		wait = hotShardRequestWait
	}
	a.hotShards.mu.Lock()
	now := a.now()
	if a.hotShards.keys != nil && a.now().Sub(a.hotShards.at) < a.hotShardTTL() {
		keys := a.hotShards.keys
		a.hotShards.mu.Unlock()
		return keys
	}
	staleKeys := a.hotShards.keys
	if a.hotShards.retry.State() == retrypolicy.FailedDeferred && !now.Before(a.hotShards.retryAt) {
		a.hotShards.retry.Reset()
		a.hotShards.retryAt = time.Time{}
	}
	if now.Before(a.hotShards.retryAt) {
		a.hotShards.mu.Unlock()
		return staleKeys
	}
	done := a.hotShards.loading
	if done == nil {
		done = make(chan struct{})
		a.hotShards.loading = done
		go a.loadHotShards(ctx, done, a.hotShards.retry.State() == retrypolicy.Waiting)
	}
	failing := a.hotShards.failing
	a.hotShards.mu.Unlock()

	// After a failed read nobody waits on the next one: production's read kept
	// failing, and on its starved host the short wait stretched to hundreds of
	// milliseconds on every poll (#808).
	if staleKeys != nil || failing {
		return staleKeys
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}

	a.hotShards.mu.Lock()
	defer a.hotShards.mu.Unlock()
	return a.hotShards.keys
}

func (a *api) hotShardTTL() time.Duration {
	if a.d.Cfg.SnapshotInterval > 0 {
		return a.d.Cfg.SnapshotInterval
	}
	return defaultHotShardTTL
}

func (a *api) loadHotShards(ctx context.Context, done chan struct{}, retry bool) {
	budget := serverstore.NewQueryBudget(serverstore.ClassBackground)
	if retry {
		budget = serverstore.NewRetryQueryBudget(serverstore.ClassBackground)
	}
	baseCtx := serverstore.WithQueryBudget(servertiming.Detached(ctx), budget)
	loadCtx, cancel := context.WithTimeout(baseCtx, hotShardLoadTimeout)
	defer cancel()
	keys, err := a.d.Store.HotShardKeys(loadCtx, hotShardLimit)

	a.hotShards.mu.Lock()
	defer a.hotShards.mu.Unlock()
	if err == nil {
		// Empty stays nil so a fresh install picks up the first built shards
		// on the next poll instead of after a full TTL.
		a.hotShards.keys = nil
		if len(keys) > 0 {
			a.hotShards.keys = keys
		}
		a.hotShards.at = a.now()
		a.hotShards.retry.Reset()
		a.hotShards.retryAt = time.Time{}
	} else {
		retry, state := a.hotShards.retry.Failure()
		if state == retrypolicy.Waiting {
			delay, _ := retrypolicy.Delay(retry, nil)
			a.hotShards.retryAt = a.now().Add(delay)
		} else {
			a.hotShards.retryAt = a.now().Add(a.hotShardTTL())
		}
	}
	a.hotShards.failing = err != nil
	a.hotShards.loading = nil
	close(done)
}

func withHotShardKeys(statsJSON string, keys []string) string {
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
