package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
)

func init() {
	Register(Command{
		Name:    "sync",
		Summary: "warm compatibility shards and flush the evidence upload queue now",
		Run:     syncMain,
	})
}

// syncMain implements `csx sync`: POST /local/v1/sync when a daemon is
// running, otherwise a direct one-shot sync over the same wiring. Both
// paths are offline-tolerant: failures are reported, local state is safe
// (goal.md §3.9, §25.F).
//
// The mode gate is here, in front of both, and not only inside SyncNow.
//
// It used to be only inside SyncNow, which is a complete no-op outside
// community mode — but syncMain reaches that function only on the fallback
// path. It prefers the daemon, and the daemon it found was not necessarily
// this home's: daemon.BaseURLFor falls back to the configured DaemonPort for
// a home that has never written a daemon.addr, and that port is the same
// default on every home on the machine. A local-only install therefore
// handed "sync now" to a community daemon belonging to a different home, and
// reported its counters as its own. Measured against the shipped v0.1.43
// binary and a fresh local-only CSX_HOME: 807 warmed shard keys, 119
// set-aside reports, none of them this home's.
//
// "Local-only mode never sends anything" is the promise the mode exists for,
// so the check has to come before anything that could reach a socket —
// including the probe that looks for a daemon at all.
func syncMain(ctx context.Context, args []string) int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
	}

	cfg, err := config.Load(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: sync: %v\n", err)
		return 1
	}
	if cfg.Mode != config.ModeCommunity {
		printSyncResult(&daemon.SyncResult{})
		fmt.Printf("\nNothing was synced and no request was made: this install is %s.\n",
			modeLabel(cfg.Mode))
		fmt.Println("Community mode is what warms the shard cache and uploads evidence:")
		fmt.Println("  csx init --community")
		return 0
	}

	res, err := syncViaDaemon(ctx, home)
	if err != nil {
		// Daemon down: run the sync directly in-process.
		d, derr := daemon.New(home)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "csx: sync: %v\n", derr)
			return 1
		}
		defer d.Close()
		r := d.SyncNow(ctx)
		res = &r
	}

	printSyncResult(res)
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "csx: sync (non-fatal): %s\n", e)
	}
	return 0
}

// printSyncResult writes the counters. A non-community run prints them too,
// all zero: a user who runs `csx sync` and gets only an explanation cannot
// tell a working no-op from a command that failed before it started.
func printSyncResult(res *daemon.SyncResult) {
	fmt.Printf("warmed shard keys:  %d\n", res.WarmedKeys)
	fmt.Printf("uploaded batches:   %d\n", res.UploadedBatches)
	fmt.Printf("adoption reports:   %d\n", res.UploadedReports)
	if res.ReconcileNote != "" {
		fmt.Printf("note:               %s\n", res.ReconcileNote)
	}
	if res.SetAsideReports > 0 {
		fmt.Printf("set aside:          %d (the server rejected these; they are kept, not sent)\n",
			res.SetAsideReports)
	}
}

// modeLabel names the mode the way `csx init` asked about it, rather than by
// its config value: "uninitialized" is a state a user never chose and would
// not recognize as an answer to anything.
func modeLabel(mode string) string {
	if mode == config.ModeUninitialized {
		return "not initialized yet (run `csx init`)"
	}
	return "in " + mode + " mode"
}

func syncViaDaemon(ctx context.Context, home string) (*daemon.SyncResult, error) {
	c, err := daemon.NewClient(home)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, err = c.Status(pctx)
	cancel()
	if err != nil {
		return nil, err
	}
	res, err := c.Sync(ctx)
	if err != nil {
		return nil, err
	}
	return &res, nil
}
