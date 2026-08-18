package main

import (
	"context"
	"log"
	"os"
	"runtime/debug"
	"time"

	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/admin"
	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/httpapi"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/storage/blob"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

var processStartedAt = time.Now()

// BuildMux assembles the csx-server HTTP handler: the complete /v1 API
// (contract C5) plus /healthz. Publicness gating follows CSX_PUBLIC_CHECK:
// "trust" skips the registry probe (dev/e2e), anything else runs the strict
// Checker backed by the packages table as its cache.
func BuildMux(cfg serverstore.ServerConfig, store serverstore.Store) *http.ServeMux {
	return buildMux(context.Background(), cfg, store)
}

func buildMux(ctx context.Context, cfg serverstore.ServerConfig, store serverstore.Store) *http.ServeMux {
	mux, _ := buildMuxWithTracker(ctx, cfg, store)
	return mux
}

func buildMuxWithTracker(ctx context.Context, cfg serverstore.ServerConfig, store serverstore.Store) (*http.ServeMux, *activity.Tracker) {
	deps := httpapi.Deps{Store: store, Cfg: cfg}
	if cfg.BlobDir != "" {
		blobs, err := blob.NewFS(cfg.BlobDir)
		if err != nil {
			log.Printf("csx-server: blob dir %s unavailable: %v (sample endpoints disabled)", cfg.BlobDir, err)
		} else {
			deps.Blobs = blobs
		}
	}
	if cfg.PublicCheck != "trust" && store != nil {
		deps.Checker = &registry.Checker{Cache: &registry.ServerCache{Store: store}}
	}
	inner := httpapi.NewMux(deps)
	var activityStore activity.Store
	if candidate, ok := store.(activity.Store); ok {
		activityStore = candidate
	}
	var activityMaintenance activity.MaintenanceStore
	if candidate, ok := store.(activity.MaintenanceStore); ok {
		activityMaintenance = candidate
	}
	activityTracker := activity.NewWithMaintenance(ctx, activityStore, activityMaintenance, activity.Config{HashKeyHex: cfg.ActivityHashKey})
	var accessMetrics admin.AccessMetricsReader
	if accessLogPath := os.Getenv("CSX_ADMIN_ACCESS_LOG"); accessLogPath != "" {
		accessMetrics = admin.NewAccessLogReader(accessLogPath)
	}
	var authoringStore serverstore.AuthoringSessionStore
	if candidate, ok := store.(serverstore.AuthoringSessionStore); ok {
		authoringStore = candidate
	}
	admin.Register(inner, admin.Deps{
		Store:         newAdminStore(store),
		TokenSHA256:   cfg.AdminTokenSHA256,
		PublicURL:     cfg.PublicURL,
		Version:       serverVersion(),
		StartedAt:     processStartedAt,
		AccessMetrics: accessMetrics,
		Activity:      activityTracker,
		Authoring:     authoringStore,
	})
	web.Register(inner, web.Deps{
		Store:     &webStore{s: store, blobs: deps.Blobs},
		PublicURL: cfg.PublicURL,
		Version:   serverVersion(),
		DistDir:   os.Getenv("CSX_DIST_DIR"),
	})
	outer := http.NewServeMux()
	outer.Handle("/", activityTracker.Wrap(inner))
	return outer, activityTracker
}

// serverVersion prefers an explicit CSX_VERSION, falling back to the module
// build info stamp.
func serverVersion() string {
	if v := os.Getenv("CSX_VERSION"); v != "" {
		return v
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// StartBuilder launches the aggregation pipeline (snapshots, failure
// clusters, shards, matrix jobs, daily stats) on the CSX_SNAPSHOT_INTERVAL
// cadence. It returns immediately; the loop stops when ctx is canceled.
func StartBuilder(ctx context.Context, cfg serverstore.ServerConfig, store serverstore.Store) {
	b := &compatibility.Builder{Store: store}
	go b.RunLoop(ctx, cfg.SnapshotInterval)
}
