// Package servertiming records the four fixed, public request phases used by
// the diagnostic API routes. It never stores request or query text.
package servertiming

import (
	"context"
	"sync"
	"time"
)

type contextKey struct{}

// Timing is scoped to one HTTP request. A store read may run with a context
// derived by WithoutCancel, so its pool observation can arrive concurrently.
type Timing struct {
	mu            sync.Mutex
	started       time.Time
	handler       time.Time
	serialization time.Time
	poolWait      time.Duration
}

func Start(ctx context.Context) (context.Context, *Timing) {
	t := &Timing{started: time.Now()}
	return context.WithValue(ctx, contextKey{}, t), t
}

// Ensure lets the outer server middleware start the clock before it creates
// its database budget, while NewMux can still be used directly in tests.
func Ensure(ctx context.Context) (context.Context, *Timing) {
	if t, ok := ctx.Value(contextKey{}).(*Timing); ok {
		return ctx, t
	}
	return Start(ctx)
}

func Handler(ctx context.Context) {
	if t, ok := ctx.Value(contextKey{}).(*Timing); ok {
		t.mu.Lock()
		if t.handler.IsZero() {
			t.handler = time.Now()
		}
		t.mu.Unlock()
	}
}

func Serialization(ctx context.Context) {
	if t, ok := ctx.Value(contextKey{}).(*Timing); ok {
		t.mu.Lock()
		if t.serialization.IsZero() {
			t.serialization = time.Now()
		}
		t.mu.Unlock()
	}
}

func AddPoolWait(ctx context.Context, d time.Duration) {
	if t, ok := ctx.Value(contextKey{}).(*Timing); ok && d > 0 {
		t.mu.Lock()
		t.poolWait += d
		t.mu.Unlock()
	}
}

// Durations returns disjoint phases. PoolWait counts only time queued for a
// pool slot; connection dialing and SQL execution remain in work. A cache hit
// or a route without a database read has zero pool wait.
func (t *Timing) Durations() (middleware, poolWait, work, serialization time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	end := time.Now()
	if t.handler.IsZero() {
		return end.Sub(t.started), t.poolWait, 0, 0
	}
	middleware = t.handler.Sub(t.started)
	if t.serialization.IsZero() {
		work = end.Sub(t.handler)
	} else {
		work = t.serialization.Sub(t.handler)
		serialization = end.Sub(t.serialization)
	}
	poolWait = t.poolWait
	if poolWait > work {
		poolWait = work
	}
	work -= poolWait
	return
}
