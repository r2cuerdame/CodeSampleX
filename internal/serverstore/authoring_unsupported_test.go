package serverstore

import (
	"context"
	"testing"
	"time"
)

// The incident this file exists for (#364): Flutter plugins on the Dart-only
// pub@1 verifier image and Android artifacts whose dependencies live on Google
// Maven sat at the top of /v1/wanted. No verifier image could build them, the
// only outcome a writer could honestly report was INFRASTRUCTURE, and that one
// is refunded to the coordinate AND to the reporting writer — so the same
// session was handed the same coordinate again after the five-minute
// debounce. Every pub row in the withheld ledger read attempts=10 excused=4,
// and 60–80 % of the farm's Wanted iterations were spent that way.
//
// Two rules fix it without weakening the independent-writer evidence:
// a writer is refunded a coordinate once, not every time it repeats itself;
// and a verifier-environment gap has its own terminal outcome, measured by two
// writers like NO_CALLABLE_SYMBOL, that is not refunded at all.

// A second identical INFRASTRUCTURE report from the same writer on the same
// coordinate is not new information. The coordinate is still excused — the
// writer measured nothing about it — but the writer's own look is spent.
func TestTheSameWriterIsNotRefundedTheSameCoordinateTwice(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	handouts := 0
	for i := 0; i < 40; i++ {
		work, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour))
		if err != nil || !ok {
			t.Fatalf("handout %d: ok=%v err=%v", i, ok, err)
		}
		if work.Name != hopelessName {
			break
		}
		handouts++
		if _, ok, err := store.ReportAuthoringOutcome(ctx, "writer-a", AuthoringInfrastructure, "pub@1 lacks the Flutter SDK", now); err != nil || !ok {
			t.Fatalf("report %d: ok=%v err=%v", i, ok, err)
		}
		now = now.Add(AuthoringAttemptDebounce)
	}
	want := AuthoringMaxSessionHandouts + AuthoringSessionRefunds
	if handouts != want {
		t.Fatalf("one writer repeating one excuse was handed the coordinate %d times, want %d (%d looks + %d refund)",
			handouts, want, AuthoringMaxSessionHandouts, AuthoringSessionRefunds)
	}
	state, found, err := store.AuthoringAttemptState(ctx, "maven", hopelessName, "2.2.20", "")
	if err != nil || !found {
		t.Fatalf("attempt state: found=%v err=%v", found, err)
	}
	// The coordinate itself was excused every time: a writer's failure is not
	// evidence about the artifact, so nothing here counts towards withholding.
	if state.NoOutput != 0 || !state.QuarantinedAt.IsZero() {
		t.Fatalf("a writer's own failures were counted against the coordinate: %+v", state)
	}
	if state.Excused != handouts {
		t.Fatalf("excused = %d, want every one of the %d reports refunded to the coordinate", state.Excused, handouts)
	}
	// And another writer is still offered it.
	other, ok, err := store.ClaimAuthoringWork(ctx, "writer-b", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok || other.Name != hopelessName {
		t.Fatalf("a second writer was refused unmeasured work: %+v ok=%v err=%v", other, ok, err)
	}
}

// The verifier image is the network's, not the writer's. A writer that
// measured "no image for this ecosystem can build this" has said its piece
// and is not handed the coordinate again; nothing is refunded.
func TestAnUnsupportedEnvironmentReportIsNotRefunded(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	if _, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
		t.Fatalf("handout: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.ReportAuthoringOutcome(ctx, "writer-a", AuthoringUnsupportedEnvironment, "pub@1 verifier lacks the Flutter SDK path_provider needs", now); err != nil || !ok {
		t.Fatalf("report: ok=%v err=%v", ok, err)
	}
	state, found, err := store.AuthoringAttemptState(ctx, "maven", hopelessName, "2.2.20", "")
	if err != nil || !found {
		t.Fatalf("attempt state: found=%v err=%v", found, err)
	}
	if state.Excused != 0 || state.NoOutput != 1 || state.Attempts != 1 {
		t.Fatalf("an environment measurement was refunded like a writer failure: %+v", state)
	}
	if state.SessionsMeasuringUnsupported != 1 || state.SessionsMeasuringImpossible != 0 {
		t.Fatalf("measurements: unsupported=%d impossible=%d, want 1/0", state.SessionsMeasuringUnsupported, state.SessionsMeasuringImpossible)
	}
	// One writer's measurement withholds nothing from the network...
	rows, err := store.ListAuthoringQuarantine(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("one session's report withheld %d coordinates", len(rows))
	}
	// ...but this writer is not asked to measure it again.
	now = now.Add(AuthoringAttemptDebounce)
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("after the report: ok=%v err=%v", ok, err)
	}
	if work.Name == hopelessName {
		t.Fatal("the writer that measured the environment unsupported was handed the coordinate again")
	}
	other, ok, err := store.ClaimAuthoringWork(ctx, "writer-b", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok || other.Name != hopelessName {
		t.Fatalf("a second writer was refused unmeasured work: %+v ok=%v err=%v", other, ok, err)
	}
}

// Two independent writers measuring the same environment gap is the network's
// evidence. The withholding needs an operator, because the thing that lifts
// it — a new verifier image — is an operator's act, and the panel must say so.
func TestTwoWritersMeasuringAnUnsupportedEnvironmentWithholdTheCoordinate(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for _, session := range []string{"writer-a", "writer-b"} {
		if _, ok, err := store.ClaimAuthoringWork(ctx, session, quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
			t.Fatalf("%s handout: ok=%v err=%v", session, ok, err)
		}
		if _, ok, err := store.ReportAuthoringOutcome(ctx, session, AuthoringUnsupportedEnvironment, "requires Flutter SDK, absent from pub@1", now); err != nil || !ok {
			t.Fatalf("%s report: ok=%v err=%v", session, ok, err)
		}
		now = now.Add(time.Minute)
	}
	rows, err := store.ListAuthoringQuarantine(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("quarantine = %d rows, want the unsupported coordinate withheld", len(rows))
	}
	row := rows[0]
	if row.QuarantineReason != AuthoringReasonUnsupportedEnvironment {
		t.Errorf("reason = %q, want %q", row.QuarantineReason, AuthoringReasonUnsupportedEnvironment)
	}
	if !row.ReopensAt.IsZero() {
		t.Errorf("an environment withholding lapses at %v; a missing verifier image does not heal on a timer", row.ReopensAt)
	}
	if row.SessionsMeasuringUnsupported != 2 || row.Attempts != 2 || row.Excused != 0 {
		t.Errorf("evidence = unsupported %d attempts %d excused %d, want 2/2/0", row.SessionsMeasuringUnsupported, row.Attempts, row.Excused)
	}
	if len(row.History) != 4 {
		t.Errorf("history = %d entries, want 2 handouts and 2 reports", len(row.History))
	}
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-c", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("fresh worker: ok=%v err=%v", ok, err)
	}
	if work.Name == hopelessName {
		t.Fatalf("fresh worker was handed %q; the coordinate should be withheld", work.Name)
	}
	health, err := store.FarmHealthNow(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if health.WithheldCoordinates != 1 || health.WithheldByReason[AuthoringReasonUnsupportedEnvironment] != 1 {
		t.Errorf("panel withheld=%d byReason=%v", health.WithheldCoordinates, health.WithheldByReason)
	}
}

// "Nothing callable" and "no image can build it" are different claims. One of
// each is one writer's opinion twice over, not two writers agreeing.
func TestDifferentTerminalMeasurementsDoNotPool(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	reports := []struct {
		session string
		outcome AuthoringOutcome
	}{
		{"writer-a", AuthoringNoCallableSymbol},
		{"writer-b", AuthoringUnsupportedEnvironment},
	}
	for _, r := range reports {
		if _, ok, err := store.ClaimAuthoringWork(ctx, r.session, quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
			t.Fatalf("%s handout: ok=%v err=%v", r.session, ok, err)
		}
		if _, ok, err := store.ReportAuthoringOutcome(ctx, r.session, r.outcome, "one line", now); err != nil || !ok {
			t.Fatalf("%s report: ok=%v err=%v", r.session, ok, err)
		}
		now = now.Add(time.Minute)
	}
	rows, err := store.ListAuthoringQuarantine(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("two different one-writer opinions withheld %d coordinates", len(rows))
	}
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-c", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok || work.Name != hopelessName {
		t.Fatalf("a third writer was refused work two writers disagreed about: %+v ok=%v err=%v", work, ok, err)
	}
}

// Reopening an environment withholding — after a verifier image gains the
// toolchain — must put the coordinate back for everybody, including the
// writers that measured it.
func TestReopeningAnEnvironmentWithholdingForgetsWhoMeasuredIt(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for _, session := range []string{"writer-a", "writer-b"} {
		if _, ok, err := store.ClaimAuthoringWork(ctx, session, quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
			t.Fatalf("%s handout: ok=%v err=%v", session, ok, err)
		}
		if _, _, err := store.ReportAuthoringOutcome(ctx, session, AuthoringUnsupportedEnvironment, "no Flutter", now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if reopened, err := store.ReopenAuthoringQuarantine(ctx, "maven", hopelessName, "2.2.20", "", now); err != nil || !reopened {
		t.Fatalf("reopen = %v err=%v", reopened, err)
	}
	state, _, err := store.AuthoringAttemptState(ctx, "maven", hopelessName, "2.2.20", "")
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionsMeasuringUnsupported != 0 || !state.QuarantinedAt.IsZero() {
		t.Fatalf("after reopen = %+v", state)
	}
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok || work.Name != hopelessName {
		t.Fatalf("reopened work was not offered to the writer that measured it: %+v ok=%v err=%v", work, ok, err)
	}
}

func TestUnsupportedEnvironmentIsAWriterReportableOutcome(t *testing.T) {
	if !ValidAuthoringOutcome(AuthoringUnsupportedEnvironment) {
		t.Fatal("UNSUPPORTED_ENVIRONMENT is refused from a writer")
	}
}
