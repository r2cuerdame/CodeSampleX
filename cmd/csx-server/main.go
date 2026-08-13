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
// CSX_GITHUB_CLIENT_ID, CSX_GITHUB_CLIENT_SECRET.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const usage = `usage: csx-server <migrate|serve>

  migrate   apply schema migrations to $CSX_DSN and exit
  serve     apply migrations, then serve HTTP on $CSX_LISTEN (default :8080)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

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
	pg, err := serverstore.Open(ctx, cfg.DSN)
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

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           BuildMux(cfg, pg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(stdout, "csx-server: listening on %s\n", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "csx-server: %v\n", err)
		return 1
	}
	return 0
}
