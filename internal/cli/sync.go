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
func syncMain(ctx context.Context, args []string) int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
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

	fmt.Printf("warmed shard keys:  %d\n", res.WarmedKeys)
	fmt.Printf("uploaded batches:   %d\n", res.UploadedBatches)
	fmt.Printf("adoption reports:   %d\n", res.UploadedReports)
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "csx: sync (non-fatal): %s\n", e)
	}
	return 0
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
