// Package sample bounds concurrent work with errgroup.
package sample

import (
	"context"
	"sync"

	"golang.org/x/sync/errgroup"
)

// RunLimited runs every task with at most limit in flight and returns the
// FIRST error, plus the peak concurrency actually observed.
//
// SetLimit must be called before the first Go: calling it afterwards
// panics. Wait returns only the first error — later ones are dropped, so a
// group is the wrong tool when every failure matters.
func RunLimited(ctx context.Context, limit int, tasks []func(context.Context) error) (int, error, context.Context) {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)

	var mu sync.Mutex
	active, peak := 0, 0

	for _, task := range tasks {
		g.Go(func() error {
			mu.Lock()
			active++
			if active > peak {
				peak = active
			}
			mu.Unlock()
			defer func() {
				mu.Lock()
				active--
				mu.Unlock()
			}()
			return task(gctx)
		})
	}
	err := g.Wait()
	return peak, err, gctx
}
