package serverstore

// What a date-bounded rights grant can and cannot be applied to.
//
// R2C-63 decided the rights model for the public evidence and compatibility
// data, under an owner principle that reads: do not extend rights
// retroactively over contributions that were already made. That principle is
// a statement about the data, so before any document repeats it, this file
// measures whether the data can carry it.
//
// It cannot today. Observation evidence is not stored as contributions:
// `evidence_agg` is keyed on the coordinate
// (purl, symbol, env_hash, stage, result, error_fp) and every reporter that
// ever hit that coordinate merges into the one row, with `observation_count`
// a running sum. `evidence_dedup` groups those contributions by a caller-
// supplied observation epoch, but records neither the server receipt time nor
// the terms version accepted for the contribution.
//
// A client can durably queue observations and upload them later, so its epoch
// is not a legal acceptance boundary. `first_seen` and `last_seen` describe
// the aggregate ROW — when the coordinate was first and last seen by anyone —
// and neither one dates an individual contribution.
//
// The store also exposes the bounded-ledger purge intended by goal.md §14.4,
// but the repository has no production caller. This test invokes the method
// directly to measure the additional loss if it is scheduled; it does not
// claim that deployed maintenance already runs it. The consequence for the
// rights model is recorded in docs/data-rights.md.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// TestEvidenceRightsCutoffIsNotRepresentedByClientEpochs files two
// contributions in the same server transaction while their client-supplied
// observation days are far apart. It proves that the day buckets are not
// server receipt timestamps, then measures the optional purge separately.
func TestEvidenceRightsCutoffIsNotRepresentedByClientEpochs(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	// Both values are accepted in one server request. Their distance therefore
	// says nothing about when the server received either contribution.
	now := time.Now().UTC()
	oldEpoch := now.AddDate(0, 0, -120).Format("2006-01-02")
	recentEpoch := now.AddDate(0, 0, -1).Format("2006-01-02")

	const purl = "pkg:npm/axios@1.12.0"
	contribution := func(epoch, anon string, count int) domain.ObservationBatch {
		b := obsBatch(anon, "proj-"+anon, count)
		b.Epoch = epoch
		b.Package = purl
		return b
	}

	if _, _, err := pg.IngestBatches(ctx, []domain.ObservationBatch{
		contribution(oldEpoch, "peer-before", 7),
		contribution(recentEpoch, "peer-after", 5),
	}); err != nil {
		t.Fatal(err)
	}

	// Both contributions land in ONE row. There is no per-contribution row to
	// which the server could attach a receipt or accepted terms version.
	rows, err := pg.EvidenceForTarget(ctx, purl, "axios.post")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("two contributions to one coordinate produced %d rows, want 1 merged row", len(rows))
	}
	if got := rows[0].ObservationCount; got != 12 {
		t.Fatalf("merged observation_count = %d, want 12 (7 before + 5 after)", got)
	}

	// The intact ledger recovers counts by the days the caller asserted. That
	// is useful for deduplication, but it is not evidence of receipt or consent.
	countForEpoch := func(epoch string) int64 {
		t.Helper()
		var n int64
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			return c.QueryRow(ctx, `
				SELECT COALESCE(SUM(count), 0) FROM evidence_dedup
				WHERE bucket_kind='peer' AND epoch=$1`, epoch).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := countForEpoch(oldEpoch); got != 7 {
		t.Fatalf("before the purge, the old epoch's ledger holds %d, want 7", got)
	}
	if got := countForEpoch(recentEpoch); got != 5 {
		t.Fatalf("the recent client epoch's ledger holds %d, want 5", got)
	}

	// The ledger has no timestamp column and no terms-version dimension. If a
	// future migration adds either, this assertion fails so the rights map has
	// to be revisited instead of continuing to describe the old schema.
	var receiptTimestampColumns, termsVersionColumns int
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE data_type IN ('timestamp with time zone', 'timestamp without time zone')),
				COUNT(*) FILTER (WHERE column_name = 'terms_version')
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'evidence_dedup'`).
			Scan(&receiptTimestampColumns, &termsVersionColumns)
	}); err != nil {
		t.Fatal(err)
	}
	if receiptTimestampColumns != 0 || termsVersionColumns != 0 {
		t.Fatalf("evidence_dedup now has %d timestamp and %d terms-version columns; update the rights cutoff analysis",
			receiptTimestampColumns, termsVersionColumns)
	}

	// Invoke the library capability directly. Repo-wide production/deployment
	// code has no caller today, so this measures a hypothetical scheduled purge
	// rather than claiming one is live.
	if _, err := pg.PurgeDedupOlderThan(ctx, 30); err != nil {
		t.Fatal(err)
	}

	// After it, the pre-cutoff contribution has no record of its own...
	if got := countForEpoch(oldEpoch); got != 0 {
		t.Fatalf("after the purge, the old epoch's ledger still holds %d, want 0", got)
	}
	if got := countForEpoch(recentEpoch); got != 5 {
		t.Fatalf("the purge took the in-window epoch too: ledger holds %d, want 5", got)
	}

	// ...while the aggregate still counts it. Nothing in the database can now
	// recover even the caller-asserted day for seven of these twelve counts.
	rows, err = pg.EvidenceForTarget(ctx, purl, "axios.post")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("EvidenceForTarget returned %d rows after the purge, want 1", len(rows))
	}
	if got := rows[0].ObservationCount; got != 12 {
		t.Fatalf("observation_count = %d after the purge, want 12: the purge must not "+
			"shrink the aggregate, and this test's premise is that it does not", got)
	}

	// first_seen/last_seen are the row's, not a contribution's. Both writes
	// happened just now, so neither timestamp recovers a receipt boundary for
	// the individual contributions.
	if rows[0].FirstSeen.Before(now.AddDate(0, 0, -1)) {
		t.Errorf("first_seen = %s: expected the row's own creation time, not the "+
			"epoch of the contribution (%s)", rows[0].FirstSeen, oldEpoch)
	}
}
