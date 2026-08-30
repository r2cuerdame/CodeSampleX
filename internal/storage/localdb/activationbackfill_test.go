package localdb

import (
	"context"
	"testing"
	"time"
)

// An install that has been searching for a fortnight is not a new install.
//
// The funnel stamps are write-once and were only ever written going forward,
// so on every store that already existed they were all empty — and the next
// run then stamped firstRunAt as TODAY, labelled the 4,735th hit "first
// answer" and the 110th adoption "first adoption". Measured on this
// workstation's real store: 4,734 hits with the oldest at 2026-08-15, 109
// adoptions, and a completely empty funnel. A panel that reports an install's
// first day as today, on a store two weeks old, is worse than a panel that
// says it does not know.
//
// What the data can answer, it answers from the data. What it cannot, it
// leaves unmeasured — §6 renders that as an em dash, and an em dash is the
// honest reading.
func TestTheFunnelIsDerivedFromWhatTheStoreAlreadyHolds(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	oldest := time.Date(2026, 8, 15, 3, 17, 45, 0, time.UTC)
	newer := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	for _, ts := range []time.Time{newer, oldest, newer.Add(time.Hour)} {
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO hits(ts, query, grade, sample_id, adopted) VALUES(?,?,?,?,0)`,
			ts.Format(time.RFC3339), "q", "HIT", "sha256:aa"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO interventions(ts, sample_id, applied) VALUES(?,?,1)`,
		newer.Format(time.RFC3339), "sha256:aa"); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if err := db.BackfillActivation(ctx, now); err != nil {
		t.Fatal(err)
	}
	a, err := db.ActivationLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !a.FirstHitAt.Equal(oldest) {
		t.Errorf("firstHitAt = %v, want the oldest hit %v", a.FirstHitAt, oldest)
	}
	if !a.FirstAdoptionAt.Equal(newer) {
		t.Errorf("firstAdoptionAt = %v, want the oldest applied intervention %v", a.FirstAdoptionAt, newer)
	}

	// Nothing in the store records when this install first ran or when it was
	// initialised, so the backfill must not invent them — and must stop a
	// later run from inventing them either.
	if err := db.StampFirst(ctx, StatFirstRunAt, now); err != nil {
		t.Fatal(err)
	}
	a, err = db.ActivationLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !a.FirstRunAt.IsZero() {
		t.Errorf("firstRunAt = %v on a store that predates the stamps; it should read as unmeasured", a.FirstRunAt)
	}
	if !a.InitAt.IsZero() {
		t.Errorf("initAt = %v, invented for an install that never recorded one", a.InitAt)
	}
}

// A store with nothing in it IS a new install, and the stamps must work
// normally there — the backfill must not mark a fresh install unmeasured.
func TestAFreshStoreStillStampsGoingForward(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	if err := db.BackfillActivation(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := db.StampFirst(ctx, StatFirstRunAt, now); err != nil {
		t.Fatal(err)
	}
	a, err := db.ActivationLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !a.FirstRunAt.Equal(now) {
		t.Errorf("firstRunAt = %v on a fresh store, want %v", a.FirstRunAt, now)
	}
}
