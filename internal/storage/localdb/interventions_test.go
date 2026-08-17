package localdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestExistingDBGainsOfferCorrelationAndLegacyRowsStayNeutral(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "csx.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `DROP TABLE interventions`); err != nil {
		t.Fatal(err)
	}
	// Recreate the table exactly as the first failure-detour build did, then
	// leave a flattering completed row and a pre-upgrade hit behind.
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE interventions(
		ts TEXT NOT NULL, sample_id TEXT NOT NULL,
		exact_failure_matched INTEGER NOT NULL DEFAULT 0,
		verified_offer INTEGER NOT NULL DEFAULT 0,
		applied INTEGER, build_pass INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO interventions
		(ts, sample_id, exact_failure_matched, verified_offer, applied, build_pass)
		VALUES('2026-08-16T00:00:00Z', 'sha256:legacy', 1, 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordHit(ctx, HitRow{SampleID: "sha256:legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetStat(ctx, "survivesMigration", "yes"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("open existing database: %v", err)
	}
	defer db.Close()
	if got, ok, err := db.GetStat(ctx, "survivesMigration"); err != nil || !ok || got != "yes" {
		t.Fatalf("existing row after migration = %q, %v, %v", got, ok, err)
	}
	var legacyOffer sql.NullString
	var legacyHit sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, `SELECT offer_id, hit_id FROM interventions WHERE sample_id='sha256:legacy'`).Scan(&legacyOffer, &legacyHit); err != nil {
		t.Fatal(err)
	}
	if legacyOffer.Valid || legacyHit.Valid {
		t.Fatalf("legacy row was given invented correlation: offer=%+v hit=%+v", legacyOffer, legacyHit)
	}
	if _, err := db.CorrelateInterventionAdoption(ctx, "", "sha256:legacy", true, sql.NullBool{Bool: true, Valid: true}, ""); !errors.Is(err, ErrOfferIDRequired) {
		t.Fatalf("legacy no-token report error = %v, want explicit re-search", err)
	}
	stats, err := db.InterventionSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (InterventionStats{}) {
		t.Fatalf("pre-upgrade intervention was credited: %+v", stats)
	}
	hits, err := db.ListHits(ctx, 10)
	if err != nil || len(hits) != 1 || hits[0].Adopted {
		t.Fatalf("legacy hit changed after neutral report: %+v, %v", hits, err)
	}

	if _, err := db.RecordSearchOffer(ctx, HitRow{SampleID: "sha256:migrated"}, InterventionRow{SampleID: "sha256:migrated"}); err != nil {
		t.Fatalf("new correlation columns unavailable after reopen: %v", err)
	}
}

func TestConcurrentLegacyMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "csx.db")
	seed, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.sql.ExecContext(ctx, `DROP TABLE interventions`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.sql.ExecContext(ctx, `CREATE TABLE interventions(
		ts TEXT NOT NULL, sample_id TEXT NOT NULL,
		exact_failure_matched INTEGER NOT NULL DEFAULT 0,
		verified_offer INTEGER NOT NULL DEFAULT 0,
		applied INTEGER, build_pass INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.sql.ExecContext(ctx, `INSERT INTO interventions
		(ts, sample_id, exact_failure_matched, verified_offer, applied, build_pass)
		VALUES('2026-08-16T00:00:00Z', 'sha256:legacy-race', 1, 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	dbs := make([]*DB, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range dbs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			dbs[i], errs[i] = Open(path)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Open %d: %v", i, err)
		}
		defer dbs[i].Close()
	}

	var offer sql.NullString
	var hit sql.NullInt64
	if err := dbs[0].sql.QueryRowContext(ctx, `
		SELECT offer_id, hit_id FROM interventions WHERE sample_id='sha256:legacy-race'`).Scan(&offer, &hit); err != nil {
		t.Fatal(err)
	}
	if offer.Valid || hit.Valid {
		t.Fatalf("concurrent migration invented legacy correlation: offer=%+v hit=%+v", offer, hit)
	}
	stats, err := dbs[1].InterventionSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (InterventionStats{}) {
		t.Fatalf("legacy row earned credit after concurrent migration: %+v", stats)
	}
}

func TestInterventionCorrelationUsesOfferAndExactHit(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	sample := "sha256:same-sample"
	record := func(id string, exact, verified bool) string {
		t.Helper()
		offerID, err := db.RecordSearchOffer(ctx,
			HitRow{Query: "q", Grade: "EXACT", SampleID: id},
			InterventionRow{SampleID: id, ExactFailureMatched: exact, VerifiedOffer: verified})
		if err != nil {
			t.Fatal(err)
		}
		if len(offerID) != 32 {
			t.Fatalf("offerId length = %d, want 32 hex chars (128 bits)", len(offerID))
		}
		if _, err := hex.DecodeString(offerID); err != nil {
			t.Fatalf("offerId is not lowercase hex: %q", offerID)
		}
		return offerID
	}

	older := record(sample, true, true)
	newer := record(sample, true, true)
	if older == newer {
		t.Fatal("two offers reused the same 128-bit offerId")
	}
	// A token cannot be moved to another sample, and the failed attempt must
	// leave the real offer eligible.
	if _, err := db.CorrelateInterventionAdoption(ctx, older, "sha256:other", true, sql.NullBool{}, ""); !errors.Is(err, ErrNoEligibleIntervention) {
		t.Fatalf("cross-sample correlation error = %v", err)
	}

	pass := sql.NullBool{Bool: true, Valid: true}
	// Deliberately report the older token first. Recency would update the
	// wrong hit; capability + exact hit_id updates the intended one.
	out, err := db.CorrelateInterventionAdoption(ctx, older, sample, true, pass, "")
	if err != nil || !out.ReportedFailureAvoided() {
		t.Fatalf("older exact correlation = %+v, %v", out, err)
	}
	if _, err := db.CorrelateInterventionAdoption(ctx, newer, sample, false, sql.NullBool{}, ""); err != nil {
		t.Fatalf("newer correlation: %v", err)
	}
	if _, err := db.CorrelateInterventionAdoption(ctx, older, sample, true, pass, ""); !errors.Is(err, ErrNoEligibleIntervention) {
		t.Fatalf("replayed token error = %v, want ErrNoEligibleIntervention", err)
	}

	rows, err := db.sql.QueryContext(ctx, `
		SELECT i.offer_id, i.hit_id, h.id, h.adopted
		FROM interventions i JOIN hits h ON h.id=i.hit_id
		WHERE i.sample_id=? ORDER BY i.rowid`, sample)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotOffers []string
	var hitStates []int
	for rows.Next() {
		var offer string
		var interventionHit, hitID int64
		var state int
		if err := rows.Scan(&offer, &interventionHit, &hitID, &state); err != nil {
			t.Fatal(err)
		}
		if interventionHit != hitID {
			t.Fatalf("intervention hit_id=%d joined hit id=%d", interventionHit, hitID)
		}
		gotOffers = append(gotOffers, offer)
		hitStates = append(hitStates, state)
	}
	if !reflect.DeepEqual(gotOffers, []string{older, newer}) || !reflect.DeepEqual(hitStates, []int{1, -1}) {
		t.Fatalf("offer/hit correlation = %v / %v", gotOffers, hitStates)
	}
	if n, err := db.CountAdoptions(ctx); err != nil || n != 1 {
		t.Fatalf("CountAdoptions = %d, %v", n, err)
	}

	recordAndReport := func(id string, exact, verified, applied bool, build sql.NullBool) {
		t.Helper()
		token := record(id, exact, verified)
		if _, err := db.CorrelateInterventionAdoption(ctx, token, id, applied, build, ""); err != nil {
			t.Fatal(err)
		}
	}
	recordAndReport("sha256:reverify", true, false, true, pass)
	recordAndReport("sha256:reuse", false, true, true, pass)
	recordAndReport("sha256:failed", true, true, true, sql.NullBool{Bool: false, Valid: true})
	recordAndReport("sha256:unknown", true, true, true, sql.NullBool{})

	stats, err := db.InterventionSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := InterventionStats{
		ExactFailureMatches:     5,
		VerifiedDetoursOffered:  4,
		Applied:                 3,
		PostHitPass:             1,
		PostHitFail:             1,
		PostHitUnknown:          1,
		ReportedFailuresAvoided: 1,
	}
	if !reflect.DeepEqual(stats, want) {
		t.Errorf("summary = %+v, want %+v", stats, want)
	}
}

func TestRecordAndCorrelateAreAtomic(t *testing.T) {
	ctx := context.Background()
	t.Run("insert", func(t *testing.T) {
		db := openTemp(t)
		if _, err := db.sql.ExecContext(ctx, `CREATE TRIGGER reject_intervention BEFORE INSERT ON interventions BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.RecordSearchOffer(ctx, HitRow{SampleID: "sha256:x"}, InterventionRow{SampleID: "sha256:x"}); err == nil {
			t.Fatal("forced intervention failure unexpectedly succeeded")
		}
		if n, err := db.CountHits(ctx); err != nil || n != 0 {
			t.Fatalf("failed offer left %d hit(s), err=%v", n, err)
		}
	})

	t.Run("correlate", func(t *testing.T) {
		db := openTemp(t)
		token, err := db.RecordSearchOffer(ctx, HitRow{SampleID: "sha256:x"}, InterventionRow{
			SampleID: "sha256:x", ExactFailureMatched: true, VerifiedOffer: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.sql.ExecContext(ctx, `DELETE FROM hits`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.CorrelateInterventionAdoption(ctx, token, "sha256:x", true, sql.NullBool{Bool: true, Valid: true}, ""); !errors.Is(err, ErrInterventionHitMismatch) {
			t.Fatalf("corrupt hit correlation error = %v", err)
		}
		var applied sql.NullBool
		if err := db.sql.QueryRowContext(ctx, `SELECT applied FROM interventions WHERE offer_id=?`, token).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if applied.Valid {
			t.Fatalf("failed correlation committed intervention state: %+v", applied)
		}
	})
}

func TestCommunityOutboxFailureRollsBackAndTokenCanRetry(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	const sampleID = "sha256:outbox-retry"
	offerID, err := db.RecordSearchOffer(ctx, HitRow{SampleID: sampleID}, InterventionRow{
		SampleID: sampleID, ExactFailureMatched: true, VerifiedOffer: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `
		CREATE TRIGGER reject_adoption_outbox BEFORE INSERT ON upload_queue
		WHEN NEW.kind = 'adoption'
		BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	payload := `{"schemaVersion":1,"sampleId":"sha256:outbox-retry"}`
	pass := sql.NullBool{Bool: true, Valid: true}
	if _, err := db.CorrelateInterventionAdoption(ctx, offerID, sampleID, true, pass, payload); err == nil {
		t.Fatal("forced outbox failure unexpectedly succeeded")
	}

	var applied, buildPass, postBuildPass sql.NullBool
	var adopted int
	if err := db.sql.QueryRowContext(ctx, `
		SELECT i.applied, i.build_pass, h.adopted, h.post_build_pass
		FROM interventions i JOIN hits h ON h.id = i.hit_id
		WHERE i.offer_id = ?`, offerID).Scan(&applied, &buildPass, &adopted, &postBuildPass); err != nil {
		t.Fatal(err)
	}
	if applied.Valid || buildPass.Valid || adopted != 0 || postBuildPass.Valid {
		t.Fatalf("failed enqueue spent token: applied=%+v build=%+v adopted=%d post=%+v",
			applied, buildPass, adopted, postBuildPass)
	}
	if queued, err := db.QueuePending(ctx, 10); err != nil || len(queued) != 0 {
		t.Fatalf("failed enqueue left queue rows: %+v, %v", queued, err)
	}
	if _, err := db.sql.ExecContext(ctx, `DROP TRIGGER reject_adoption_outbox`); err != nil {
		t.Fatal(err)
	}
	outcome, err := db.CorrelateInterventionAdoption(ctx, offerID, sampleID, true, pass, payload)
	if err != nil {
		t.Fatalf("retry after outbox recovery: %v", err)
	}
	if !outcome.ReportedFailureAvoided() || !outcome.UploadQueued {
		t.Fatalf("retry outcome = %+v", outcome)
	}
	queued, err := db.QueuePending(ctx, 10)
	if err != nil || len(queued) != 1 || queued[0].Kind != "adoption" || queued[0].Payload != payload {
		t.Fatalf("retry queue = %+v, %v", queued, err)
	}
}

func TestConcurrentSameOfferAcrossDBHandlesSucceedsOnceAndQueuesAtMostOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "csx.db")
	db1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	const sampleID = "sha256:concurrent-offer"
	offerID, err := db1.RecordSearchOffer(ctx, HitRow{SampleID: sampleID}, InterventionRow{
		SampleID: sampleID, ExactFailureMatched: true, VerifiedOffer: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"schemaVersion":1,"sampleId":"sha256:concurrent-offer"}`
	dbs := []*DB{db1, db2}
	outcomes := make([]InterventionOutcome, len(dbs))
	errs := make([]error, len(dbs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, db := range dbs {
		wg.Add(1)
		go func(i int, db *DB) {
			defer wg.Done()
			<-start
			outcomes[i], errs[i] = db.CorrelateInterventionAdoption(ctx, offerID, sampleID, true,
				sql.NullBool{Bool: true, Valid: true}, payload)
		}(i, db)
	}
	close(start)
	wg.Wait()

	successes := 0
	for i, err := range errs {
		if err == nil {
			successes++
			if !outcomes[i].UploadQueued {
				t.Errorf("successful outcome %d did not report queued upload: %+v", i, outcomes[i])
			}
			continue
		}
		if !errors.Is(err, ErrNoEligibleIntervention) {
			t.Errorf("losing correlation %d error = %v", i, err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successes = %d, want exactly 1; errors=%v", successes, errs)
	}
	queued, err := db1.QueuePending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Kind != "adoption" || queued[0].Payload != payload {
		t.Fatalf("concurrent queue = %+v, want exactly one adoption upload", queued)
	}
	if n, err := db2.CountAdoptions(ctx); err != nil || n != 1 {
		t.Fatalf("CountAdoptions = %d, %v", n, err)
	}
}

func TestInterventionTableIsLocalOnlyAndNarrow(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if _, err := db.RecordSearchOffer(ctx, HitRow{SampleID: "sha256:private"}, InterventionRow{
		SampleID: "sha256:private", ExactFailureMatched: true, VerifiedOffer: true,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.sql.QueryContext(ctx, `PRAGMA table_info(interventions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	want := []string{"ts", "offer_id", "hit_id", "sample_id", "exact_failure_matched", "verified_offer", "applied", "build_pass"}
	if !reflect.DeepEqual(columns, want) {
		t.Fatalf("intervention columns = %v, want only %v", columns, want)
	}
	queued, err := db.QueuePending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("recording a local intervention queued upload data: %+v", queued)
	}
}

func TestReportedFailureAvoidedRequiresEveryStage(t *testing.T) {
	pass := sql.NullBool{Bool: true, Valid: true}
	fail := sql.NullBool{Bool: false, Valid: true}
	tests := []struct {
		name string
		row  InterventionOutcome
		want bool
	}{
		{"all four", InterventionOutcome{ExactFailureMatched: true, VerifiedOffer: true, Applied: true, BuildPass: pass}, true},
		{"not an exact failure", InterventionOutcome{VerifiedOffer: true, Applied: true, BuildPass: pass}, false},
		{"not a verified offer", InterventionOutcome{ExactFailureMatched: true, Applied: true, BuildPass: pass}, false},
		{"applied false", InterventionOutcome{ExactFailureMatched: true, VerifiedOffer: true, BuildPass: pass}, false},
		{"post-hit fail", InterventionOutcome{ExactFailureMatched: true, VerifiedOffer: true, Applied: true, BuildPass: fail}, false},
		{"post-hit unknown", InterventionOutcome{ExactFailureMatched: true, VerifiedOffer: true, Applied: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.row.ReportedFailureAvoided(); got != tc.want {
				t.Errorf("ReportedFailureAvoided() = %v, want %v", got, tc.want)
			}
		})
	}
}
