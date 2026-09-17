// Command csx-builder is the standalone compatibility aggregation pipeline
// (CSX-451, milestone v0.1.197 Runtime Isolation): the same
// internal/compatibility.Builder that used to run as a goroutine inside
// csx-server, now its own OS process with its own PostgreSQL connection
// pool.
//
// Splitting it out means an overloaded or wedged aggregation pass can no
// longer take csx-server's connection pool down with it -- the two
// processes do not share a *pgx.Conn pool, only the same PostgreSQL server.
// A leader lease (internal/compatibility.Leader) keeps two csx-builder
// processes -- an old one not yet exited and a new one already started
// during a deploy -- from materializing the same shards at the same time.
//
// Configuration is environment-only: CSX_BUILDER_DSN (falls back to
// CSX_DSN), CSX_BUILDER_LISTEN (health/ready/progress, default ":8091"),
// CSX_SNAPSHOT_INTERVAL, CSX_SNAPSHOT_PASS_TIMEOUT, CSX_BUILDER_DB_*, and
// CSX_BUILDER_LEASE_*. See internal/serverstore.BuilderConfigFromEnv and
// docs/operations.md "Builder runtime topology".
//
// csx-server owns schema migrations; csx-builder never runs them, and must
// be started only once csx-server's own migrate step has completed.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const usage = `usage: csx-builder run

  run   connect to the database, acquire the compatibility-builder lease,
        and run the aggregation pipeline until the process is stopped.
        Serves /healthz, /readyz and /progress on CSX_BUILDER_LISTEN.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "run" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cfg := serverstore.BuilderConfigFromEnv()
	if cfg.DSN == "" {
		fmt.Fprintln(stderr, "csx-builder: neither CSX_BUILDER_DSN nor CSX_DSN is set")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pg, err := serverstore.OpenWithPolicy(ctx, cfg.DSN, cfg.DBPool)
	if err != nil {
		fmt.Fprintf(stderr, "csx-builder: %v\n", err)
		return 1
	}
	defer pg.Close()

	tracker := &passTracker{}
	builder, leader := newBuilder(pg, cfg, tracker)

	statusSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           newStatusMux(pg, leader, tracker, cfg.LeaseName),
		ReadHeaderTimeout: 10 * time.Second,
	}
	statusDone := make(chan error, 1)
	go func() { statusDone <- statusSrv.ListenAndServe() }()

	fmt.Fprintf(stdout, "csx-builder: listening on %s, lease=%s owner=%s interval=%s\n",
		cfg.Listen, cfg.LeaseName, cfg.LeaseOwner, cfg.SnapshotInterval)

	leader.Run(ctx, func(leaderCtx context.Context) {
		builder.RunLoop(leaderCtx, cfg.SnapshotInterval)
	})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := statusSrv.Shutdown(shutdownCtx); err != nil {
		_ = statusSrv.Close()
	}
	if err := <-statusDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "csx-builder: status server: %v\n", err)
	}
	fmt.Fprintln(stdout, "csx-builder: stopped")
	return 0
}

// newBuilder assembles this process's Builder and the Leader that governs
// it. It is separate from run so that what the pieces are wired to is
// testable without a database and without starting the pipeline -- notably
// the pause gate, which is one assignment whose absence would leave every
// other test in this repository green while the governor talked to nobody.
func newBuilder(store serverstore.Store, cfg serverstore.BuilderConfig, tracker *passTracker) (*compatibility.Builder, *compatibility.Leader) {
	builder := &compatibility.Builder{
		Store:       store,
		PassTimeout: cfg.SnapshotPassTimeout,
		OnPass:      tracker.record,
	}
	leader := &compatibility.Leader{
		Store: store,
		Cfg: compatibility.LeaseConfig{
			Name:       cfg.LeaseName,
			Owner:      cfg.LeaseOwner,
			TTL:        cfg.LeaseTTL,
			RenewEvery: cfg.LeaseRenewEvery,
			RetryEvery: cfg.LeaseRetryEvery,
		},
	}
	// #454: csx-server's resource governor pauses this pipeline through the
	// same lease row the Builder already reads, and the Builder skips a pass
	// rather than starting work it would be told to abandon. Resuming needs
	// nothing: the gate is re-asked every builderPausePoll, so the next pass
	// starts on its own once the pause clears. The gate is the Leader's own,
	// so this process and an in-process Builder answer the pause identically.
	builder.Paused = leader.PauseGate()
	return builder, leader
}
