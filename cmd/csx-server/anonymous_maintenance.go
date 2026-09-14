package main

import (
	"context"
	"log"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type anonymousMaintenance interface {
	PruneAnonymousAnalytics(context.Context, time.Time, int) (int64, error)
}

// Runs once at startup and hourly with bounded batches and a total pass
// deadline. Public request handlers never perform retention maintenance.
func startAnonymousMaintenance(ctx context.Context, store any) {
	m, ok := store.(anonymousMaintenance)
	if !ok {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			pctx = serverstore.WithQueryClass(pctx, serverstore.ClassBackground)
			for pctx.Err() == nil {
				n, err := m.PruneAnonymousAnalytics(pctx, time.Now().UTC(), 1000)
				if err != nil {
					log.Print("csx: anonymous analytics retention maintenance unavailable")
					break
				}
				if n < 1000 {
					break
				}
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
