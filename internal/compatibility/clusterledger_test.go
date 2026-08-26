package compatibility

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The production reproduction, at the grain where it happened.
//
// Migration 0024 added the failure-evidence columns and deliberately left the
// existing derived rows in place. The next builder pass re-keyed the same
// failures: pre-contract fingerprints are provenance rather than failure
// identities, so they collapse into one explicit evidence-gap row with an
// empty fingerprint. That row does not overwrite the old one — it has a
// different unique key — so the cluster-observation ledger counted the same
// failures twice and production reported 35,488 where the FAIL total was
// still 16,755.
//
// The ledger is what the deploy transaction checks before it commits, so a
// rebuild over preserved rows must leave it describing the current clusters
// only.
func TestRebuildingOverPreservedLegacyRowsDoesNotDoubleTheClusterLedger(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 6, 0, 0, 0, time.UTC)
	const (
		purl    = "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"
		pkgName = "github.com/jackc/pgx/v5"
		symbol  = "ParseConfig"
		failures = 227
	)

	// What migration 0024 leaves behind: the pre-contract derived row, keyed
	// by a fingerprint the current builder will never write again.
	if err := store.UpsertFailureCluster(ctx, serverstore.ClusterRow{
		Ecosystem: "golang", PackageName: pkgName, Symbol: symbol, Stage: "PROJECT_TEST",
		ErrorFingerprint: "sha256:" + strings.Repeat("1", 64),
		EvidenceQuality:  string(domain.EvidenceLegacyIncomplete),
		ObservationCount: failures, FirstSeen: now.Add(-240 * time.Hour), LastSeen: now,
	}); err != nil {
		t.Fatal(err)
	}

	// The same failures as source evidence, still carrying only their
	// pre-contract hash: no termination, no normalized error.
	ingestLegacyFailure(t, store, purl, symbol, "sha256:"+strings.Repeat("1", 64), failures)

	b := &Builder{Store: store, Now: func() time.Time { return now }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListFailureClusters(ctx, pkgName)
	if err != nil {
		t.Fatal(err)
	}
	var ledger int64
	for _, c := range rows {
		ledger += c.ObservationCount
		if c.ErrorFingerprint != "" {
			t.Errorf("a preserved pre-contract fingerprint is being served as a current cluster: %+v", c)
		}
	}
	if ledger != failures {
		t.Fatalf("cluster-observation ledger = %d over %d rows, want %d — the rebuilt rows are being counted beside the preserved ones",
			ledger, len(rows), failures)
	}
}

func ingestLegacyFailure(t *testing.T, store *serverstore.Fake, purl, symbol, fp string, count int) {
	t.Helper()
	_, rejected, err := store.IngestBatches(context.Background(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-25", AnonID: "anon-legacy", ProjectBucket: "proj-legacy",
		Package: purl, Symbol: symbol, SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "golang", OS: "windows", Arch: "amd64",
			Runtime: "go", RuntimeVersion: "1.26",
		},
		Stage: domain.StageProjectTest, Result: domain.ResultFail,
		ErrorFingerprint: fp, ObservationCount: count,
	}})
	if err != nil || len(rejected) > 0 {
		t.Fatalf("ingest: %v %v", err, rejected)
	}
}
