package localdb

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// The stamps are the only record that this install ever reached a stage, and
// the whole point of the S2→S6 duration (docs/activation-funnel.md §5) is
// that it is measured from the FIRST occurrence. A stamp that moved forward
// on every later run would silently turn "time to first useful answer" into
// "time since the most recent search", which is a number that always looks
// good and means nothing.
//
// mcpLastReadyAt is the deliberate exception: the readiness panel needs the
// most recent completed handshake to say the MCP path still works today, not
// only that it once did.
func TestFirstStampsAreWriteOnceAndTheLastReadyStampIsNot(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	early := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	late := time.Date(2026, 8, 29, 17, 30, 0, 0, time.UTC)

	for _, key := range FirstStampKeys {
		if err := db.StampFirst(ctx, key, early); err != nil {
			t.Fatalf("StampFirst(%s, early): %v", key, err)
		}
		if err := db.StampFirst(ctx, key, late); err != nil {
			t.Fatalf("StampFirst(%s, late): %v", key, err)
		}
	}
	if err := db.Stamp(ctx, StatMCPLastReadyAt, early); err != nil {
		t.Fatalf("Stamp(mcpLastReadyAt, early): %v", err)
	}
	if err := db.Stamp(ctx, StatMCPLastReadyAt, late); err != nil {
		t.Fatalf("Stamp(mcpLastReadyAt, late): %v", err)
	}

	led, err := db.ActivationLedger(ctx)
	if err != nil {
		t.Fatalf("ActivationLedger: %v", err)
	}
	for name, got := range map[string]time.Time{
		"firstRunAt":      led.FirstRunAt,
		"initAt":          led.InitAt,
		"firstSyncAt":     led.FirstSyncAt,
		"mcpFirstReadyAt": led.MCPFirstReadyAt,
		"firstHitAt":      led.FirstHitAt,
		"firstAdoptionAt": led.FirstAdoptionAt,
	} {
		if !got.Equal(early) {
			t.Errorf("%s = %s, want the first stamp %s", name, got, early)
		}
	}
	if !led.MCPLastReadyAt.Equal(late) {
		t.Errorf("mcpLastReadyAt = %s, want the latest stamp %s", led.MCPLastReadyAt, late)
	}
}

// An install that has never reached a stage has no time for it, and the
// difference between "not yet" and "at the zero instant" is the difference
// between the panel printing an em dash and printing 1970 (§6: a gap renders
// as —, never as a measurement).
func TestAnUnreachedStageHasNoTimeRatherThanAZeroOne(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	led, err := db.ActivationLedger(ctx)
	if err != nil {
		t.Fatalf("ActivationLedger: %v", err)
	}
	if !led.FirstRunAt.IsZero() || !led.FirstHitAt.IsZero() || !led.MCPFirstReadyAt.IsZero() {
		t.Fatalf("a fresh store already claims stages: %+v", led)
	}
	if d, ok := led.TimeToFirstAnswer(); ok {
		t.Fatalf("time to first answer = %s on a store with neither endpoint", d)
	}

	// Only both endpoints make the duration measurable, and it is measured
	// from init (the consent choice) rather than from the first execution:
	// §5 names S2→S6.
	initAt := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	if err := db.StampFirst(ctx, StatInitAt, initAt); err != nil {
		t.Fatal(err)
	}
	if led, err = db.ActivationLedger(ctx); err != nil {
		t.Fatal(err)
	}
	if d, ok := led.TimeToFirstAnswer(); ok {
		t.Fatalf("time to first answer = %s with no first hit", d)
	}
	if err := db.StampFirst(ctx, StatFirstHitAt, initAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if led, err = db.ActivationLedger(ctx); err != nil {
		t.Fatal(err)
	}
	d, ok := led.TimeToFirstAnswer()
	if !ok || d != 2*time.Hour {
		t.Fatalf("time to first answer = %s ok=%v, want 2h", d, ok)
	}
}

// S6 in the funnel table is "first hit", and the hit writer is
// RecordSearchOffer — the only place a hits row is born. Stamping anywhere
// else (the search entry point, say) would count a search that returned
// nothing as a first answer.
func TestTheHitWriterStampsTheFirstAnswerAndLaterHitsDoNotMoveIt(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	const sampleA = "sha256:" + "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	const sampleB = "sha256:" + "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"

	first := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	if _, err := db.RecordSearchOffer(ctx,
		HitRow{TS: first, Query: "q", SampleID: sampleA},
		InterventionRow{TS: first, SampleID: sampleA}); err != nil {
		t.Fatalf("RecordSearchOffer: %v", err)
	}
	led, err := db.ActivationLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !led.FirstHitAt.Equal(first) {
		t.Fatalf("firstHitAt = %s, want the hit's own ts %s", led.FirstHitAt, first)
	}

	if _, err := db.RecordSearchOffer(ctx,
		HitRow{TS: first.Add(24 * time.Hour), Query: "q2", SampleID: sampleB},
		InterventionRow{TS: first.Add(24 * time.Hour), SampleID: sampleB}); err != nil {
		t.Fatalf("second RecordSearchOffer: %v", err)
	}
	if led, err = db.ActivationLedger(ctx); err != nil {
		t.Fatal(err)
	}
	if !led.FirstHitAt.Equal(first) {
		t.Fatalf("firstHitAt moved to %s after a later hit", led.FirstHitAt)
	}
}

// An adoption report that says "I did NOT apply this" is a completed report,
// not an adoption: the store already writes it as -1 and every surface shows
// adopted=false. S7 is the first ADOPTION, so an explicit not-applied report
// must leave the stage unreached rather than stamping it.
func TestOnlyAnAppliedAdoptionReportStampsTheFirstAdoption(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	const sample = "sha256:" + "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"

	ts := time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC)
	declined, err := db.RecordSearchOffer(ctx,
		HitRow{TS: ts, Query: "q", SampleID: sample},
		InterventionRow{TS: ts, SampleID: sample})
	if err != nil {
		t.Fatalf("RecordSearchOffer: %v", err)
	}
	if _, err := db.CorrelateInterventionAdoption(ctx, declined, sample, false, sql.NullBool{}, ""); err != nil {
		t.Fatalf("not-applied report: %v", err)
	}
	led, err := db.ActivationLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !led.FirstAdoptionAt.IsZero() {
		t.Fatalf("a not-applied report stamped firstAdoptionAt = %s", led.FirstAdoptionAt)
	}

	applied, err := db.RecordSearchOffer(ctx,
		HitRow{TS: ts, Query: "q", SampleID: sample},
		InterventionRow{TS: ts, SampleID: sample})
	if err != nil {
		t.Fatalf("second RecordSearchOffer: %v", err)
	}
	if _, err := db.CorrelateInterventionAdoption(ctx, applied, sample, true,
		sql.NullBool{Bool: true, Valid: true}, ""); err != nil {
		t.Fatalf("applied report: %v", err)
	}
	if led, err = db.ActivationLedger(ctx); err != nil {
		t.Fatal(err)
	}
	if led.FirstAdoptionAt.IsZero() {
		t.Fatal("an applied adoption report left firstAdoptionAt unstamped")
	}
}
