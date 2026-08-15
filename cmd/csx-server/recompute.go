package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/httpapi"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// recomputeBatch bounds one pass; the pool is 8 connections on a 2GB host.
const recomputeBatch = 1000

// runRecomputeStatus re-derives every sample's status from its receipts
// under the current rules and reports what changed.
//
// The request path only ever upgrades a status, which is correct while the
// rules hold still. When a rule is corrected, statuses granted under the
// old one are simply wrong — and leaving them advertises verification the
// evidence does not support. This is the honesty pass for that case, run
// deliberately by an operator rather than silently on every request:
//
//	csx-server recompute-status            # report only
//	csx-server recompute-status --apply    # write the corrections
func runRecomputeStatus(cfg serverstore.ServerConfig, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("recompute-status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apply := fs.Bool("apply", false, "write the recomputed statuses (default: report only)")
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pg, ok := openMigrated(ctx, cfg, stderr)
	if !ok {
		return 1
	}
	defer pg.Close()

	// EVERY sample, which is what the command says it does.
	//
	// One capped call quietly examined the first 1000 and exited 0, leaving
	// the rest stale under the rule the operator had just fixed. Warning
	// about it was not enough either: the list is ordered newest-first, so
	// "re-run until the count drops" returned the identical page every time
	// and could never converge. It pages.
	var samples []serverstore.SampleRow
	for offset := 0; ; offset += recomputeBatch {
		page, perr := pg.ListSamplesPage(ctx, recomputeBatch, offset)
		if perr != nil {
			fmt.Fprintf(stderr, "csx-server recompute-status: %v\n", perr)
			return 1
		}
		samples = append(samples, page...)
		if len(page) < recomputeBatch {
			break
		}
	}

	now := time.Now().UTC()
	changed, downgraded := 0, 0
	counts := map[string]int{}
	for _, s := range samples {
		receipts, rerr := pg.ReceiptsForSample(ctx, s.SampleID)
		if rerr != nil {
			fmt.Fprintf(stderr, "csx-server recompute-status: receipts for %s: %v\n", s.SampleID, rerr)
			return 1
		}
		want := httpapi.RecomputeStatus(receipts, now)
		counts[want]++
		if want == s.Status {
			continue
		}
		changed++
		if statusIsLower(want, s.Status) {
			downgraded++
		}
		fmt.Fprintf(stdout, "  %s  %s -> %s\n", s.SampleID[:19], s.Status, want)
		if *apply {
			if err := pg.SetSampleStatus(ctx, s.SampleID, want); err != nil {
				fmt.Fprintf(stderr, "csx-server recompute-status: set %s: %v\n", s.SampleID, err)
				return 1
			}
		}
	}

	fmt.Fprintf(stdout, "\n%d samples examined, %d would change (%d downgrades)\n",
		len(samples), changed, downgraded)
	for _, st := range []string{"PUBLISHED", "CROSS_PASS", "MATRIX_PASS", "STABLE"} {
		if counts[st] > 0 {
			fmt.Fprintf(stdout, "  %-12s %d\n", st, counts[st])
		}
	}
	if changed > 0 && !*apply {
		fmt.Fprintln(stdout, "\nnothing written — re-run with --apply")
	}
	return 0
}

// statusIsLower reports whether a ranks below b in the C13 ladder.
func statusIsLower(a, b string) bool {
	rank := map[string]int{"PUBLISHED": 1, "CROSS_PASS": 2, "MATRIX_PASS": 3, "STABLE": 4}
	return rank[a] < rank[b]
}
