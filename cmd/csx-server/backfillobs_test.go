package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func seedReceipt(t *testing.T, store *serverstore.Fake, sampleID, receiptID, result string) {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     sampleID,
		ManifestJSON: `{"packages":["pkg:npm/axios@1.12.0"],"symbols":["axios.get"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"schemaVersion":    2,
		"stages":           map[string]string{"resolve": "PASS", "load": "PASS", "contract": result},
		"resolvedPackages": []string{"pkg:npm/axios@1.12.0"},
		"environment": map[string]any{
			"schemaVersion": 1, "ecosystem": "npm", "os": "linux", "arch": "x64",
			"runtime": "node", "runtimeVersion": "22",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID: receiptID, SampleID: sampleID, PeerID: "peer-farm-1",
		ContractResult: result, ReceiptJSON: string(body),
		CreatedAt: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
}

// Every receipt the server already holds is a run that happened, and until now
// none of them was recorded as one. The conversion is live for new receipts;
// the ones already stored need this pass or their coordinates keep reading
// "never measured" about work this network did itself.
func TestBackfillRecordsTheRunsAlreadyStored(t *testing.T) {
	store := serverstore.NewFake()
	seedReceipt(t, store, "sha256:one", "receipt:1", "PASS")
	seedReceipt(t, store, "sha256:two", "receipt:2", "FAIL")

	var out bytes.Buffer
	stats, err := backfillObservations(context.Background(), store, true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Receipts != 2 {
		t.Errorf("read %d receipts, want both", stats.Receipts)
	}
	if stats.Accepted == 0 {
		t.Error("recorded nothing at all")
	}
	if stats.Rejected != 0 {
		t.Errorf("ingest refused %d batches", stats.Rejected)
	}
}

// Ingest is idempotent per (bucket, aggregate, epoch), so running the pass
// twice must not inflate anything. An operator has to be able to re-run it
// after a failure without wondering what it doubled.
func TestBackfillIsSafeToRunTwice(t *testing.T) {
	store := serverstore.NewFake()
	seedReceipt(t, store, "sha256:one", "receipt:1", "PASS")
	ctx := context.Background()

	var out bytes.Buffer
	first, err := backfillObservations(ctx, store, true, &out)
	if err != nil {
		t.Fatal(err)
	}
	second, err := backfillObservations(ctx, store, true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if second.Receipts != first.Receipts {
		t.Errorf("second pass read %d receipts, first read %d", second.Receipts, first.Receipts)
	}
	if second.Rejected != 0 {
		t.Errorf("second pass was refused %d batches", second.Rejected)
	}
}

// Without -apply the pass reports and writes nothing. An operator points a
// backfill at production to find out what it would do first.
func TestBackfillWritesNothingWithoutApply(t *testing.T) {
	store := serverstore.NewFake()
	seedReceipt(t, store, "sha256:one", "receipt:1", "PASS")

	var out bytes.Buffer
	stats, err := backfillObservations(context.Background(), store, false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Receipts != 1 {
		t.Errorf("read %d receipts", stats.Receipts)
	}
	if stats.Accepted != 0 {
		t.Errorf("a report-only pass wrote %d observations", stats.Accepted)
	}
	if stats.Would == 0 {
		t.Error("a report-only pass did not say what it would have written")
	}
}

// A pass that refuses everything must say WHY. The first production run
// reported "9,883 refused" and nothing else, so the reason -- a project
// bucket seven bytes over the limit -- had to be found by reading the
// validator instead of by reading the output.
func TestBackfillReportsWhyItWasRefused(t *testing.T) {
	store := serverstore.NewFake()
	seedReceipt(t, store, "sha256:one", "receipt:1", "PASS")

	var out bytes.Buffer
	stats, err := backfillObservations(context.Background(), refusing{store}, true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rejected == 0 {
		t.Fatal("nothing was refused, so there is nothing to report")
	}
	if len(stats.Reasons) == 0 {
		t.Fatal("refused batches were counted but never explained")
	}
	if _, ok := stats.Reasons["projectBucket longer than 64 bytes"]; !ok {
		t.Errorf("reasons = %v, want the store's own words", stats.Reasons)
	}
}

// refusing is a store that accepts nothing, the way production did.
type refusing struct{ *serverstore.Fake }

func (refusing) IngestBatches(_ context.Context, batches []domain.ObservationBatch) (int, []serverstore.RejectedBatch, error) {
	out := make([]serverstore.RejectedBatch, 0, len(batches))
	for i := range batches {
		out = append(out, serverstore.RejectedBatch{Index: i, Reason: "projectBucket longer than 64 bytes"})
	}
	return 0, out, nil
}
