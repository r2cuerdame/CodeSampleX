// Command csx-server is the CodeSampleX central server: HTTP API, website
// and PostgreSQL-backed evidence/sample registry (plan contract C9).
//
// Subcommands:
//
//	csx-server migrate   apply embedded schema migrations and exit
//	csx-server serve     migrate, then serve HTTP on CSX_LISTEN
//
// Configuration is environment-only: CSX_DSN (required), CSX_LISTEN,
// CSX_BLOB_DIR, CSX_PUBLIC_URL, CSX_PUBLIC_CHECK, CSX_SNAPSHOT_INTERVAL,
// CSX_GITHUB_CLIENT_ID, CSX_GITHUB_CLIENT_SECRET, CSX_ACTIVITY_HASH_KEY.
//
// CSX_BUILDER_MODE (CSX-451) selects where the compatibility Builder runs:
// "inprocess" (default) keeps it a goroutine of this process, as it has
// always been; "standalone" disables that goroutine because a separate
// cmd/csx-builder process owns the aggregation pipeline instead. See
// docs/operations.md "Builder runtime topology".
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const usage = `usage: csx-server <migrate|serve|quarantine|seeder-create|recompute-status|backfill-observations|prestage-builder-indexes|authoring-budget-report>

  migrate      apply schema migrations to $CSX_DSN and exit
  serve        apply migrations, then serve HTTP on $CSX_LISTEN (default :8080)
  prestage-builder-indexes
               pre-create heavy compatibility builder indexes concurrently
               before activating migration 0036; safe to run while v0.1.149 is active
  quarantine   hide a published sample from every serving read (operator only)
               csx-server quarantine <sampleId> --reason "…"   [--release]
  seeder-create
               mint a seeder identity and its api token (operator only)
               csx-server seeder-create <login>

  recompute-status
               re-derive every sample status from its receipts under the
               current rules; corrects statuses granted under an older rule
               csx-server recompute-status [--apply]

  backfill-observations
               record the runs already stored as receipts; a contract run is
               an execution in an environment we recorded, and receipts kept
               before the conversion went live were never counted as one
               csx-server backfill-observations [--apply]

  authoring-budget-report
               replay read-only dumps of the authoring ledger into the
               attempts-to-success, duration and budget-option report (#149);
               reads files only, never the database
               csx-server authoring-budget-report --ledger F --sessions F
`

// dedupRetentionDays is the rotating-bucket retention window from
// goal.md 14.4. Named so the promise and the code cannot drift apart.
const dedupRetentionDays = 30

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// strandedReconcileLimit bounds one boot's reconcile. It is a backlog, not a
// queue: draining it over a few restarts is fine, and flooding the verifier
// queue in one pass would bury the fresh work the network is waiting on.
const strandedReconcileLimit = 200

// laneReconcileLimit bounds one boot's cross-job lane review. The whole open
// queue is a few dozen rows; this is a runaway guard, not a rate.
const laneReconcileLimit = 500

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cfg := serverstore.ConfigFromEnv()

	switch args[0] {
	case "migrate":
		return runMigrate(cfg, stdout, stderr)
	case "serve":
		return runServe(cfg, stdout, stderr)
	case "prestage-builder-indexes", "prepare-builder-indexes":
		return runPrestageBuilderIndexes(cfg, stdout, stderr)
	case "quarantine":
		return runQuarantine(cfg, args[1:], stdout, stderr)
	case "seeder-create":
		return runSeederCreate(cfg, args[1:], stdout, stderr)
	case "recompute-status":
		return runRecomputeStatus(cfg, args[1:], stdout, stderr)
	case "backfill-observations":
		return runBackfillObservations(cfg, args[1:], stdout, stderr)
	case "authoring-budget-report":
		return runAuthoringBudgetReport(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "csx-server: unknown subcommand %q\n%s", args[0], usage)
		return 2
	}
}

func runPrestageBuilderIndexes(cfg serverstore.ServerConfig, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if cfg.DSN == "" {
		fmt.Fprintln(stderr, "csx-server: CSX_DSN is not set")
		return 1
	}
	pg, err := serverstore.OpenWithPolicy(ctx, cfg.DSN, cfg.DBPool)
	if err != nil {
		fmt.Fprintf(stderr, "csx-server: %v\n", err)
		return 1
	}
	defer pg.Close()
	if err := pg.PrestageBuilderIndexes(ctx); err != nil {
		fmt.Fprintf(stderr, "csx-server: prestage builder indexes failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "csx-server: builder indexes prestaged and validated")
	return 0
}

func openMigrated(ctx context.Context, cfg serverstore.ServerConfig, stderr io.Writer) (*serverstore.PG, bool) {
	if cfg.DSN == "" {
		fmt.Fprintln(stderr, "csx-server: CSX_DSN is not set")
		return nil, false
	}
	pg, err := serverstore.OpenWithPolicy(ctx, cfg.DSN, cfg.DBPool)
	if err != nil {
		fmt.Fprintf(stderr, "csx-server: %v\n", err)
		return nil, false
	}
	if err := pg.Migrate(ctx); err != nil {
		pg.Close()
		fmt.Fprintf(stderr, "csx-server: %v\n", err)
		return nil, false
	}
	return pg, true
}

func runMigrate(cfg serverstore.ServerConfig, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	pg, ok := openMigrated(ctx, cfg, stderr)
	if !ok {
		return 1
	}
	defer pg.Close()
	fmt.Fprintln(stdout, "csx-server: migrations applied")
	return 0
}

func runServe(cfg serverstore.ServerConfig, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// The boot schedule (#250, bootschedule.go) records every step from here
	// to the Builder start and writes its lines where the rest of serve
	// writes.
	tl := bootRecord
	tl.logf = func(format string, args ...any) { fmt.Fprintf(stdout, format+"\n", args...) }
	pg, ok := openMigrated(ctx, cfg, stderr)
	if !ok {
		return 1
	}
	defer pg.Close()
	tl.mark(bootMarkMigrated)

	// Capture the public wanted feed before the aggregation pipeline starts.
	// The first live request after a restart must not run its whole aggregate
	// while the builder is consuming the same PostgreSQL CPU, I/O and pool.
	//
	// The Builder start handed in here is deferred: the mode decision
	// (inprocess vs standalone) is made now, inside
	// primeWantedBeforeBuilder, but the in-process Builder itself starts
	// only when the maintenance lane below has finished or run out of
	// budget. Before #250 it started here, and its first pass ran beside
	// four reconciles, the dedup purge and the mux prewarm on a database
	// that had just been restarted.
	budget := defaultBootMaintenanceBudget()
	sched := newBootSchedule(tl, budget)
	wantedSnapshot, err := primeWantedBeforeBuilder(ctx, cfg, pg, sched.deferBuilder)
	if err != nil {
		fmt.Fprintf(stderr, "csx-server: preload wanted snapshot: %v\n", err)
		return 1
	}
	tl.mark(bootMarkWantedPrimed)

	// Timeouts bound what one slow client can hold. Without ReadTimeout a
	// trickled request body pins a goroutine and, once a handler starts, a
	// connection out of a pool of 8 — on a 2GB instance a handful of those
	// is the whole server. WriteTimeout sits above the slowest legitimate
	// response (a 256KB artifact over a bad link), and IdleTimeout reaps
	// keep-alive connections Caddy no longer needs.
	handler, activityTracker, demandCollector := buildMuxWithTrackerAndWanted(context.Background(), cfg, pg, wantedSnapshot)
	closeCollectors := func() {
		trackerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = activityTracker.Close(trackerCtx)
		_ = demandCollector.Close(trackerCtx)
		cancel()
	}
	listenAddr, narrowed := resolveListenAddr(cfg.Listen, runtime.GOOS)
	if narrowed {
		fmt.Fprintln(stdout, narrowedListenNotice(cfg.Listen, listenAddr))
	}
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	// Bind before any maintenance runs. /healthz answers from this instant,
	// so the compose healthcheck, the deploy smoke and the rollback loop
	// measure a process that is serving, not one that is reconciling; a slow
	// database now costs the maintenance lane its budget, not the deploy its
	// health window (production 2026-09-08, v0.1.150).
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		closeCollectors()
		fmt.Fprintf(stderr, "csx-server: %v\n", err)
		return 1
	}
	tl.mark(bootMarkListen)
	fmt.Fprintf(stdout, "csx-server: listening on %s\n", listenAddr)

	// The resource governor (#454): from here on, a window in which
	// interactive readers are being refused -- or in which the hypervisor is
	// taking this VM's CPU -- pauses the Builder and Farm ingest, and a
	// window without one puts both back. It starts with the listener so its
	// first window measures the boot the maintenance lane is about to run,
	// under serving traffic, rather than a process that is not yet serving.
	if cfg.GovernorEnabled {
		startGovernor(ctx, pg, stdout)
	} else {
		fmt.Fprintln(stdout, "csx-server: resource governor disabled (CSX_GOVERNOR_ENABLED=off)")
	}

	// The maintenance lane: one goroutine, the boot-time reconciles and the
	// dedup purge one at a time under the budget above, then the Builder.
	fmt.Fprintf(stdout, "csx-server: boot maintenance budget %s\n", budget)
	go sched.run(ctx, bootMaintenanceSteps(cfg, pg, budget))

	shutdownDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		shutdownErr := srv.Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			// Stop lingering connections before closing the collector's admission
			// gate. A handler that survives Close is still counted as dropped if
			// it reaches observation after collector shutdown begins.
			_ = srv.Close()
		}
		trackerCtx, trackerCancel := context.WithTimeout(context.Background(), 10*time.Second)
		trackerErr := activityTracker.Close(trackerCtx)
		// The demand collector flushes its last thirty seconds the same
		// way; a deploy restart must not lose the window it is measuring.
		demandErr := demandCollector.Close(trackerCtx)
		trackerCancel()
		shutdownDone <- errors.Join(shutdownErr, trackerErr, demandErr)
	}()

	err = srv.Serve(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		closeCollectors()
		fmt.Fprintf(stderr, "csx-server: %v\n", err)
		return 1
	}
	if ctx.Err() != nil {
		if shutdownErr := <-shutdownDone; shutdownErr != nil {
			telemetry := activityTracker.Telemetry()
			fmt.Fprintf(stderr, "csx-server: shutdown incomplete: %v (activity pending=%d dropped=%d failures=%d)\n", shutdownErr, telemetry.Pending, telemetry.Dropped, telemetry.StoreFailures)
			return 1
		}
	}
	return 0
}
