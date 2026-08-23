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
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/httpapi"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const usage = `usage: csx-server <migrate|serve|quarantine|seeder-create|recompute-status>

  migrate      apply schema migrations to $CSX_DSN and exit
  serve        apply migrations, then serve HTTP on $CSX_LISTEN (default :8080)
  quarantine   hide a published sample from every serving read (operator only)
               csx-server quarantine <sampleId> --reason "…"   [--release]
  seeder-create
               mint a seeder identity and its api token (operator only)
               csx-server seeder-create <login>

  recompute-status
               re-derive every sample status from its receipts under the
               current rules; corrects statuses granted under an older rule
               csx-server recompute-status [--apply]
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// strandedReconcileLimit bounds one boot's reconcile. It is a backlog, not a
// queue: draining it over a few restarts is fine, and flooding the verifier
// queue in one pass would bury the fresh work the network is waiting on.
const strandedReconcileLimit = 200

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
	case "quarantine":
		return runQuarantine(cfg, args[1:], stdout, stderr)
	case "seeder-create":
		return runSeederCreate(cfg, args[1:], stdout, stderr)
	case "recompute-status":
		return runRecomputeStatus(cfg, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "csx-server: unknown subcommand %q\n%s", args[0], usage)
		return 2
	}
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
	pg, ok := openMigrated(ctx, cfg, stderr)
	if !ok {
		return 1
	}
	defer pg.Close()

	// Aggregation pipeline: snapshots/shards/stats on CSX_SNAPSHOT_INTERVAL.
	StartBuilder(ctx, cfg, pg)

	// Wake authoring drafts that have nothing left to wait for.
	//
	// A verifier that cannot resolve dependencies files a SKIPPED receipt,
	// which closes the sample's only cross job without measuring anything.
	// The receipt path queues another attempt now, but the drafts stranded
	// before that existed have no future event to reach them — production
	// held 159, verified by nobody and waiting on nothing. Boot is a good
	// enough clock for a finite backlog, and it keeps this off every
	// request path.
	if woken, err := httpapi.ReconcileStrandedDrafts(ctx, pg, strandedReconcileLimit); err != nil {
		fmt.Fprintf(stderr, "csx-server: stranded draft reconcile failed: %v\n", err)
	} else if woken > 0 {
		fmt.Fprintf(stdout, "csx-server: requeued %d stranded authoring drafts\n", woken)
	}

	// Timeouts bound what one slow client can hold. Without ReadTimeout a
	// trickled request body pins a goroutine and, once a handler starts, a
	// connection out of a pool of 8 — on a 2GB instance a handful of those
	// is the whole server. WriteTimeout sits above the slowest legitimate
	// response (a 256KB artifact over a bad link), and IdleTimeout reaps
	// keep-alive connections Caddy no longer needs.
	handler, activityTracker := buildMuxWithTracker(context.Background(), cfg, pg)
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
		trackerCancel()
		shutdownDone <- errors.Join(shutdownErr, trackerErr)
	}()

	fmt.Fprintf(stdout, "csx-server: listening on %s\n", listenAddr)
	err := srv.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		trackerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = activityTracker.Close(trackerCtx)
		cancel()
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
