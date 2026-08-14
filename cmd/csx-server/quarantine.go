package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Quarantine is deliberately an operator command on the host rather than an
// HTTP endpoint. Sample publishing is anonymous, so there is no account to
// authorize a takedown against, and inventing an admin API would add an
// authenticated write surface to defend for the sake of an action performed
// a handful of times. Whoever can reach the database can already do
// anything; requiring shell access is the honest boundary.
//
//	docker compose exec -T server csx-server quarantine sha256:… --reason "…"
//	docker compose exec -T server csx-server quarantine sha256:… --release
//
// The row is hidden from every serving read, never deleted: receipts and
// the case survive, so a mistake is reversible and a real takedown stays
// auditable.
func runQuarantine(cfg serverstore.ServerConfig, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("quarantine", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reason := fs.String("reason", "", "why it is being hidden (recorded on the row)")
	release := fs.Bool("release", false, "restore a previously quarantined sample")
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: csx-server quarantine <sampleId> [--reason R] [--release]")
		return 2
	}
	sampleID := fs.Arg(0)
	if !*release && *reason == "" {
		fmt.Fprintln(stderr, "csx-server quarantine: --reason is required (it is recorded on the row)")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, ok := openMigrated(ctx, cfg, stderr)
	if !ok {
		return 1
	}
	defer pg.Close()

	if err := pg.SetSampleQuarantine(ctx, sampleID, !*release, *reason); err != nil {
		fmt.Fprintf(stderr, "csx-server quarantine: %v\n", err)
		return 1
	}
	if *release {
		fmt.Fprintf(stdout, "released: %s is served again\n", sampleID)
	} else {
		fmt.Fprintf(stdout, "quarantined: %s is hidden from search, shards and the explorer\n", sampleID)
		fmt.Fprintln(stdout, "receipts and the case row are untouched; --release restores it")
	}
	fmt.Fprintln(stdout, "the next aggregation pass rebuilds the affected shards")
	return 0
}
