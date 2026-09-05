package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/httpapi"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// backfillBatch is how many samples are read per page. Receipts are fetched
// per sample, so this bounds memory rather than round trips.
const backfillBatch = 500

// backfillStats is what the pass reports. Would is what an unapplied pass
// would have written, so an operator can point it at production first.
type backfillStats struct {
	Samples  int
	Receipts int
	Would    int
	Accepted int
	Rejected int
	// Reasons counts the store's own words for every refusal. The first
	// production run reported "9,883 refused" and nothing else, so the cause
	// -- a project bucket seven bytes over the limit -- had to be found by
	// reading the validator instead of by reading the output. A pass that
	// refuses everything has to say why in the place the operator is looking.
	Reasons map[string]int
}

// backfillSource is the store surface this pass needs. It is written as an
// interface so the pass is tested against the Fake rather than against a
// database an operator has to have running.
type backfillSource interface {
	ListSamplesPage(ctx context.Context, limit, offset int) ([]serverstore.SampleRow, error)
	ReceiptsForSample(ctx context.Context, sampleID string) ([]serverstore.ReceiptRow, error)
	IngestBatches(ctx context.Context, batches []domain.ObservationBatch) (int, []serverstore.RejectedBatch, error)
}

// backfillObservations records the runs this network already performed.
//
// A contract run is an execution on a real machine in an environment we
// recorded rather than assumed, and until the receipt conversion went live it
// was kept only as a verification. Every receipt stored before that is a run
// nothing counted, so coordinates the farm built itself still read "never
// measured" about our own work.
//
// Ingest is idempotent per (bucket, aggregate, epoch), so this is safe to run
// again after a failure: a receipt already recorded contributes nothing the
// second time rather than doubling.
func backfillObservations(ctx context.Context, store backfillSource, apply bool, out io.Writer) (backfillStats, error) {
	var stats backfillStats
	for offset := 0; ; offset += backfillBatch {
		page, err := store.ListSamplesPage(ctx, backfillBatch, offset)
		if err != nil {
			return stats, fmt.Errorf("listing samples at %d: %w", offset, err)
		}
		for _, sample := range page {
			stats.Samples++
			receipts, err := store.ReceiptsForSample(ctx, sample.SampleID)
			if err != nil {
				return stats, fmt.Errorf("receipts for %s: %w", sample.SampleID, err)
			}
			var manifest domain.SampleManifest
			_ = json.Unmarshal([]byte(sample.ManifestJSON), &manifest)
			for _, receipt := range receipts {
				stats.Receipts++
				batches := httpapi.ObservationsFromReceipt(receipt, manifest.Symbols...)
				if len(batches) == 0 {
					continue
				}
				stats.Would += len(batches)
				if !apply {
					continue
				}
				accepted, rejected, err := store.IngestBatches(ctx, batches)
				if err != nil {
					return stats, fmt.Errorf("ingesting %s: %w", receipt.ReceiptID, err)
				}
				stats.Accepted += accepted
				stats.Rejected += len(rejected)
				for _, r := range rejected {
					if stats.Reasons == nil {
						stats.Reasons = map[string]int{}
					}
					stats.Reasons[r.Reason]++
				}
			}
		}
		if len(page) < backfillBatch {
			break
		}
		fmt.Fprintf(out, "  %d samples, %d receipts so far\n", stats.Samples, stats.Receipts)
	}
	return stats, nil
}

func runBackfillObservations(cfg serverstore.ServerConfig, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backfill-observations", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apply := fs.Bool("apply", false, "record the runs (default: report only)")
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pg, ok := openMigrated(ctx, cfg, stderr)
	if !ok {
		return 1
	}
	defer pg.Close()

	if code := runBackfillReport(ctx, pg, *apply, stdout); code != 0 {
		return code
	}
	return 0
}

// runBackfillReport is the pass and everything it tells the operator.
//
// It is separate from the command so a test can read what an operator would
// read. The first version of the refusal reporting gathered the reasons
// correctly and printed none of them, and the test passed anyway because it
// asserted on the struct instead of on the output.
func runBackfillReport(ctx context.Context, store backfillSource, apply bool, stdout io.Writer) int {
	stats, err := backfillObservations(ctx, store, apply, stdout)
	if err != nil {
		fmt.Fprintf(stdout, "csx-server backfill-observations: %v\n", err)
		return 1
	}
	if !apply {
		fmt.Fprintf(stdout, "%d samples, %d receipts, %d observations would be recorded (re-run with -apply)\n",
			stats.Samples, stats.Receipts, stats.Would)
		return 0
	}
	fmt.Fprintf(stdout, "%d samples, %d receipts, %d observations recorded, %d refused\n",
		stats.Samples, stats.Receipts, stats.Accepted, stats.Rejected)
	for _, reason := range sortedReasons(stats.Reasons) {
		fmt.Fprintf(stdout, "  refused %d: %s\n", stats.Reasons[reason], reason)
	}
	if stats.Rejected > 0 {
		return 1
	}
	return 0
}

// sortedReasons orders refusals by how many batches each one cost, so the
// operator reads the one that matters first.
func sortedReasons(reasons map[string]int) []string {
	out := make([]string, 0, len(reasons))
	for reason := range reasons {
		out = append(out, reason)
	}
	sort.Slice(out, func(i, j int) bool {
		if reasons[out[i]] != reasons[out[j]] {
			return reasons[out[i]] > reasons[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
