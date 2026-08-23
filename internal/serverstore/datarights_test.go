package serverstore

// What a date-bounded rights grant can and cannot be applied to.
//
// R2C-63 decided the rights model for the public evidence and compatibility
// data, under an owner principle that reads: do not extend rights
// retroactively over contributions that were already made. That principle is
// a statement about the data, so before any document repeats it, this file
// measures whether the data can carry it.
//
// It cannot, past thirty days. Observation evidence is not stored as
// contributions: `evidence_agg` is keyed on the coordinate
// (purl, symbol, env_hash, stage, result, error_fp) and every reporter that
// ever hit that coordinate merges into the one row, with `observation_count`
// a running sum. The only per-epoch record of who contributed what is
// `evidence_dedup`, and that ledger is deliberately purged on the retention
// window (goal.md §14.4) while the aggregate keeps the counts it contributed.
//
// So a cutoff date inside the retention window is expressible, and a cutoff
// date outside it is not: after the purge there is nothing left that says
// which part of a count arrived before it. `first_seen` and `last_seen`
// describe the ROW — when the coordinate was first and last seen by anyone —
// and neither one attributes a count to a date.
//
// This is not a defect to fix here. It is the privacy design working: the
// ledger is purged precisely so a rotating anonymous id cannot be followed
// across epochs. The consequence for the rights model is recorded in
// docs/data-rights.md, and this test is what that document rests on.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// TestEvidenceContributionsStopBeingSeparableWhenTheDedupLedgerIsPurged
// files two contributions to one coordinate — one long before a cutoff, one
// after it — and measures what remains attributable to a date at each step.
func TestEvidenceContributionsStopBeingSeparableWhenTheDedupLedgerIsPurged(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	// Epochs are computed from now so the retention arithmetic below holds
	// in any year: one contribution far outside the 30-day window, one
	// inside it.
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

	// Both contributions land in ONE row. That is the first half of the
	// finding: there is no per-contribution row to attach a date to.
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

	// While the ledger is intact, the split IS recoverable: this is the
	// query a cutoff inside the retention window would have to run.
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
		t.Fatalf("before the purge, the recent epoch's ledger holds %d, want 5", got)
	}

	// The retention purge is not hypothetical: it is what the deployment
	// runs on the evidence ledger (goal.md §14.4).
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

	// ...while the aggregate still counts it. Seven of these twelve
	// observations were contributed before the cutoff and nothing in the
	// database can now say which seven.
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

	// first_seen/last_seen are the row's, not a contribution's. They bracket
	// the merge and attribute nothing: both writes happened just now, so
	// neither timestamp recovers the epoch the observations were filed for.
	if rows[0].FirstSeen.Before(now.AddDate(0, 0, -1)) {
		t.Errorf("first_seen = %s: expected the row's own creation time, not the "+
			"epoch of the contribution (%s)", rows[0].FirstSeen, oldEpoch)
	}
}
