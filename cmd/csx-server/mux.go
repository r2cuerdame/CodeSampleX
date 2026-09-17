package main

import (
	"context"
	"log"
	"os"
	"time"

	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/admin"
	"github.com/r2cuerdame/codesamplex/internal/buildinfo"
	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/hostpressure"
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
	return buildMuxWithTrackerAndWanted(ctx, cfg, store, nil)
}

func buildMuxWithTrackerAndWanted(ctx context.Context, cfg serverstore.ServerConfig, store serverstore.Store, wanted *httpapi.WantedSnapshot) (*http.ServeMux, *activity.Tracker) {
	build := buildinfo.FromEnvironment()
	deps := httpapi.Deps{Store: store, Cfg: cfg, Build: build, WantedSnapshot: wanted}
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
	// Nil when the store cannot keep operator tokens, which leaves the admin
	// surface reachable only through the browser's password prompt.
	var adminTokenStore serverstore.AdminTokenStore
	if candidate, ok := store.(serverstore.AdminTokenStore); ok {
		adminTokenStore = candidate
	}
	var farmStats serverstore.FarmStatsStore
	if candidate, ok := store.(serverstore.FarmStatsStore); ok {
		farmStats = candidate
	}
	var anomalyStore serverstore.AnomalyStore
	if candidate, ok := store.(serverstore.AnomalyStore); ok {
		anomalyStore = candidate
	}
	var csxIssueStore serverstore.CSXIssueStore
	if candidate, ok := store.(serverstore.CSXIssueStore); ok {
		csxIssueStore = candidate
	}
	// Only the PostgreSQL store has a pool to report; the fake has none,
	// and a panel of zeros would read as a healthy pool rather than as no
	// pool at all.
	var poolStats admin.PoolStatsReader
	if candidate, ok := store.(admin.PoolStatsReader); ok {
		poolStats = candidate
	}
	var anonymousStats serverstore.AnonymousAnalyticsStore
	if candidate, ok := store.(serverstore.AnonymousAnalyticsStore); ok {
		anonymousStats = candidate
	}
	admin.Register(inner, admin.Deps{
		Store:         newAdminStore(store),
		TokenSHA256:   cfg.AdminTokenSHA256,
		PublicURL:     cfg.PublicURL,
		Version:       adminVersion(build),
		StartedAt:     processStartedAt,
		AccessMetrics: accessMetrics,
		Anonymous:     anonymousStats,
		Authoring:     authoringStore,
		AdminTokens:   adminTokenStore,
		Farm:          farmStats,
		Anomalies:     anomalyStore,
		CSXIssues:     csxIssueStore,
		PoolStats:     poolStats,
		Instances:     configuredInstances(),
	})
	// GET /v1/ops/pool-metrics (CSX-454): the machine-readable
	// counterpart to the /admin dashboard's pool panel, reusing the exact
	// operator authentication /admin already enforces (admin.AdminAuth)
	// rather than a second auth mechanism. Registered only when that
	// authentication can actually be built -- the same "absent config
	// makes the route look like 404, not merely unauthorized" rule
	// admin.Register itself applies to /admin.
	if opsAuth, ok := admin.AdminAuth(cfg.AdminTokenSHA256, adminTokenStore); ok {
		opsMetrics := &httpapi.OpsMetricsHandler{
			Pool:       poolStats,
			FarmIngest: farmStats,
			Host:       hostpressure.NewSampler(),
		}
		inner.Handle("GET /v1/ops/pool-metrics", opsAuth(opsMetrics))
	}
	websiteStore := &webStore{s: store, blobs: deps.Blobs}
	websiteStore.prewarm()
	web.Register(inner, web.Deps{
		Store:     websiteStore,
		PublicURL: cfg.PublicURL,
		Build:     build,
		DistDir:   os.Getenv("CSX_DIST_DIR"),
	})
	outer := http.NewServeMux()
	// The database budget is the outermost wrapper: it has to be in place
	// before any handler reaches the store, and it has to still be there
	// when the handler returns so the request can report what the pool cost
	// it. Package cache-miss admission lives inside webStore so warm responses
	// never consume a DB-load slot.
	// Network fingerprints no longer serve as analytics identities. The
	// existing API rate limiter still uses the trusted address for abuse control.
	outer.Handle("/", withDBBudget(inner))
	return outer, activityTracker
}

// prewarm starts the small set of whole-site caches that otherwise put
// PostgreSQL checkout or full-inventory ranking on the first public request.
// Every lane owns a background-class budget, and none can delay mux startup.
func (w *webStore) prewarm() {
	if w.s == nil {
		return
	}
	_, _ = w.LatestStatsJSON(context.Background())
	_, _ = w.HotPackages(context.Background(), 12)
	go func() {
		ctx := backgroundRefreshBudget(false)
		_, _, _ = w.RecordPackages(ctx, web.RecordFilter{}, 0, 0)
	}()
	go func() {
		ctx := backgroundRefreshBudget(false)
		_, _, _ = w.SamplesPage(ctx, 0, 24)
	}()
}

// primeWantedBeforeBuilder is the restart ordering boundary: public wanted
// data is captured while the database is idle, and only then may the
// in-process aggregation pipeline start consuming shared PostgreSQL
// resources.
//
// CSX_BUILDER_MODE=standalone (CSX-451) skips start entirely: a separate
// cmd/csx-builder process owns the aggregation pipeline against its own
// connection pool, and this process must never run two copies of it.
func primeWantedBeforeBuilder(ctx context.Context, cfg serverstore.ServerConfig, store serverstore.Store, start func(context.Context, serverstore.ServerConfig, serverstore.Store)) (*httpapi.WantedSnapshot, error) {
	snapshot, err := httpapi.LoadWantedSnapshot(ctx, store)
	if err != nil {
		return nil, err
	}
	if cfg.BuilderMode != serverstore.BuilderModeStandalone {
		start(ctx, cfg, store)
	}
	return snapshot, nil
}

// adminVersion is the one line the private dashboard shows for "what is
// running". The operator surface wants the immutable commit -- it is what a
// deploy log, an image label and a rollback all name -- and falls back to the
// build version only when nothing stamped a revision.
func adminVersion(build buildinfo.Info) string {
	if build.Revision != "" {
		return build.Revision
	}
	if build.Version != "" {
		return build.Version
	}
	return "dev"
}

// StartBuilder launches the aggregation pipeline (snapshots, failure
// clusters, shards, matrix jobs, daily stats) on the CSX_SNAPSHOT_INTERVAL
// cadence. It returns immediately; the loop stops when ctx is canceled.
//
// It runs under the same named builder_lease (CSX-451) a standalone
// cmd/csx-builder process contends for, under its own owner identity
// (serverstore.NewProcessLeaseOwner("csx-server-inprocess")). That is
// deliberate even though CSX_BUILDER_MODE=inprocess is meant to mean "no
// separate Builder process exists": the deploy topology (docker-compose.yml)
// defines a `builder` service regardless of which mode a given csx-server
// instance is running under, and CSX_BUILDER_MODE is a rollout flag an
// operator can flip without redeploying every process in lockstep. Without
// this lease, a `builder` container merely being started -- during the
// cutover, or by a compose `up` that does not list services explicitly --
// would run a second, un-coordinated copy of the aggregation pipeline
// against an in-process Builder that had no idea it existed.
func StartBuilder(ctx context.Context, cfg serverstore.ServerConfig, store serverstore.Store) {
	startAnonymousMaintenance(ctx, store)
	b, leader := newInProcessBuilder(cfg, store)
	go leader.Run(ctx, func(leaderCtx context.Context) {
		b.RunLoop(leaderCtx, cfg.SnapshotInterval)
	})
}

// newInProcessBuilder assembles the in-process Builder and the Leader that
// governs it. It is separate from StartBuilder so that what the pieces are
// wired to is testable without starting goroutines -- notably the pause gate,
// which is one assignment whose absence no other test would notice.
func newInProcessBuilder(cfg serverstore.ServerConfig, store serverstore.Store) (*compatibility.Builder, *compatibility.Leader) {
	b := &compatibility.Builder{Store: store, PassTimeout: cfg.SnapshotPassTimeout}
	leader := &compatibility.Leader{
		Store: store,
		Cfg:   compatibility.LeaseConfig{Owner: serverstore.NewProcessLeaseOwner("csx-server-inprocess")},
	}
	// #454: the resource governor pauses through the lease, so the
	// in-process Builder obeys it exactly as the standalone process does. A
	// pause that only reached cmd/csx-builder would do nothing at all on a
	// deployment still running CSX_BUILDER_MODE=inprocess, which is the
	// default until #455's rollout completes.
	b.Paused = leader.PauseGate()
	return b, leader
}
