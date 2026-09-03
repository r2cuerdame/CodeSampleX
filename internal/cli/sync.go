package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
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

	res, probeErr, err := syncViaDaemon(ctx, home, stderrIsTerminal())
	if err != nil && fallBackInProcess(probeErr) {
		// No daemon to talk to: run it here. Never for a daemon that merely
		// did not answer in time -- that daemon is still working, and a
		// second sync on the same database is what Farm#18 measured.
		d, derr := daemon.New(home)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "csx: sync: %v\\n", derr)
			return 1
		}
		defer d.Close()
		r := d.SyncNow(ctx)
		res, err = &r, nil
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: sync: %v\\n", err)
		return 1
	}

	printSyncResult(res)
	for _, e := range collapseErrors(res.Errors) {
		fmt.Fprintf(os.Stderr, "csx: sync (non-fatal): %s\\n", e)
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

// syncViaDaemon asks the daemon to sync and waits. probeErr is the status
// probe's failure, kept apart from the sync's own so the caller can tell
// "no daemon" from "daemon busy". With showProgress the wait is narrated on
// stderr, one rewritable line, from the progress the daemon publishes.
func syncViaDaemon(ctx context.Context, home string, showProgress bool) (res *daemon.SyncResult, probeErr, err error) {
	c, err := daemon.NewClient(home)
	if err != nil {
		return nil, err, err
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, probeErr = c.Status(pctx)
	cancel()
	if probeErr != nil {
		return nil, probeErr, probeErr
	}
	stop := make(chan struct{})
	if showProgress {
		go narrateSync(ctx, c, stop)
	}
	r, err := c.Sync(ctx)
	close(stop)
	if showProgress {
		fmt.Fprint(os.Stderr, "\\r\\033[K")
	}
	if err != nil {
		return nil, nil, err
	}
	return &r, nil, nil
}

// narrateSync rewrites one stderr line every second until stop closes.
func narrateSync(ctx context.Context, c *daemon.Client, stop <-chan struct{}) {
	started := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var p *daemon.SyncProgress
		sctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		if st, err := c.Status(sctx); err == nil {
			p = st.Sync
		}
		cancel()
		fmt.Fprint(os.Stderr, "\\r\\033[K"+progressLine(p, time.Since(started)))
	}
}

// progressLine is what the wait looks like: the daemon's stage and count
// when it reports one, and the elapsed time always.
func progressLine(p *daemon.SyncProgress, elapsed time.Duration) string {
	e := elapsed.Round(time.Second)
	clock := fmt.Sprintf("%02d:%02d", int(e.Minutes()), int(e.Seconds())%60)
	if p == nil {
		return "csx sync: waiting on the daemon · " + clock
	}
	if p.Total > 0 {
		return fmt.Sprintf("csx sync: %s %d/%d · %s", p.Stage, p.Done, p.Total, clock)
	}
	return fmt.Sprintf("csx sync: %s · %s", p.Stage, clock)
}

// collapseErrors folds repeated messages into one line each, in first-seen
// order: thirteen identical "context deadline exceeded" lines said less
// than "13× context deadline exceeded" does.
func collapseErrors(errs []string) []string {
	counts := map[string]int{}
	var order []string
	for _, e := range errs {
		if counts[e] == 0 {
			order = append(order, e)
		}
		counts[e]++
	}
	out := make([]string, 0, len(order))
	for _, e := range order {
		if counts[e] > 1 {
			out = append(out, fmt.Sprintf("%d× %s", counts[e], e))
		} else {
			out = append(out, e)
		}
	}
	return out
}

// fallBackInProcess reports whether the CLI may run the sync itself: only
// when there is no daemon to reach. A probe that timed out, or a daemon that
// answered with an error, is a daemon that exists and is working.
func fallBackInProcess(probeErr error) bool {
	if probeErr == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(probeErr, &opErr) {
		return true
	}
	return errors.Is(probeErr, syscall.ECONNREFUSED) || errors.Is(probeErr, os.ErrNotExist)
}

// stderrIsTerminal reports whether progress should be narrated: only to a
// person, never into a script's or a farm worker's captured output.
func stderrIsTerminal() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
