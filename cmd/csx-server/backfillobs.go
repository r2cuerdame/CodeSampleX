package main

import (
	"context"
	"flag"
	"fmt"
	"io"
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
			for _, receipt := range receipts {
				stats.Receipts++
				batches := httpapi.ObservationsFromReceipt(receipt)
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

	stats, err := backfillObservations(ctx, pg, *apply, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "csx-server backfill-observations: %v\n", err)
		return 1
	}
	if *apply {
		fmt.Fprintf(stdout, "%d samples, %d receipts, %d observations recorded, %d refused\n",
			stats.Samples, stats.Receipts, stats.Accepted, stats.Rejected)
		if stats.Rejected > 0 {
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "%d samples, %d receipts, %d observations would be recorded (re-run with -apply)\n",
		stats.Samples, stats.Receipts, stats.Would)
	return 0
}
