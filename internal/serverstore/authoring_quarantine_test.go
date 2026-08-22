package serverstore

import (
	"context"
	"testing"
	"time"
)

// The incident this file exists for: one live worker was handed
// org.jetbrains.kotlin.plugin.serialization.gradle.plugin, refreshed its claim
// through 22 attempts across four hours, and every restart was handed the same
// coordinate again. Reclaim only ever released a claim whose SESSION had died,
// so a session that stayed alive held the slot until its 24-hour lease ran out.
//
// Enumerating impossible package shapes by hand — the Gradle marker, the
// pom-only BOM, the per-platform npm binary — will always lag the ecosystem.
// What generalises is the attempt itself: a coordinate that keeps being handed
// out and keeps producing nothing.

const hopelessName = "org.jetbrains.kotlin/kotlin-gradle-plugins-bom"

// quarantineCandidates puts the hopeless coordinate first, the way the real
// queue does, and leaves enough claimable work behind it that a writer moved
// off it always has somewhere to go.
func quarantineCandidates() []WantedRow {
	return []WantedRow{
		{Ecosystem: "maven", Name: hopelessName, Version: "2.2.20", Symbol: "", Kind: "EXPANSION"},
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Kind: "WANTED"},
		{Ecosystem: "npm", Name: "zod", Version: "4.1.0", Symbol: "z.object", Kind: "WANTED"},
		{Ecosystem: "pypi", Name: "httpx", Version: "0.28.1", Symbol: "httpx.get", Kind: "WANTED"},
	}
}

func hopeless() (string, string, string, string) {
	return "maven", hopelessName, "2.2.20", ""
}

// drainSession asks for work as this writer until it is handed something other
// than the hopeless coordinate, and reports how many times it got that one.
// It is how a real worker behaves: it keeps asking, and something else has to
// stop it.
func drainSession(t *testing.T, store *Fake, session string, now time.Time) (int, time.Time) {
	t.Helper()
	ctx := context.Background()
	handouts := 0
	for i := 0; i < 40; i++ {
		work, ok, err := store.ClaimAuthoringWork(ctx, session, quarantineCandidates(), now, now.Add(24*time.Hour))
		if err != nil {
			t.Fatalf("%s handout %d: %v", session, i, err)
		}
		if !ok || work.Name != hopelessName {
			return handouts, now
		}
		handouts++
		now = now.Add(AuthoringAttemptDebounce)
	}
	t.Fatalf("%s was never moved off %s", session, hopelessName)
	return handouts, now
}

// A live worker that keeps asking must be moved on. It is not enough to wait
// for the lease: the lease is 24 hours and the session refreshes.
func TestALiveWorkerIsMovedOffACoordinateItKeepsProducingNothingFor(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	lease := 24 * time.Hour

	var last AuthoringWorkRow
	for i := 0; i < AuthoringMaxSessionHandouts; i++ {
		work, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(lease))
		if err != nil || !ok {
			t.Fatalf("handout %d: ok=%v err=%v", i, ok, err)
		}
		if work.Name != "org.jetbrains.kotlin/kotlin-gradle-plugins-bom" {
			t.Fatalf("handout %d gave %q, want the hopeless coordinate first", i, work.Name)
		}
		last = work
		now = now.Add(AuthoringAttemptDebounce)
	}
	next, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(lease))
	if err != nil || !ok {
		t.Fatalf("after the bound: ok=%v err=%v", ok, err)
	}
	if next.Name == last.Name {
		t.Fatalf("worker was handed %q for the %dth time; the bound is %d",
			next.Name, AuthoringMaxSessionHandouts+1, AuthoringMaxSessionHandouts)
	}
	if next.Name != "axios" {
		t.Fatalf("moved-on worker got %q, want the next claimable coordinate", next.Name)
	}
}

// Polling is not attempting. A worker that asks twice in the same minute has
// not tried twice, and counting it as two would quarantine a coordinate nobody
// ever worked on.
func TestAskingAgainImmediatelyIsNotASecondAttempt(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		work, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour))
		if err != nil || !ok {
			t.Fatalf("poll %d: ok=%v err=%v", i, ok, err)
		}
		if work.Name != hopelessName {
			t.Fatalf("poll %d moved the worker off after no elapsed work: %q", i, work.Name)
		}
		now = now.Add(2 * time.Second)
	}
	state, found, err := store.AuthoringAttemptState(ctx, "maven", hopelessName, "2.2.20", "")
	if err != nil || !found {
		t.Fatalf("attempt state: found=%v err=%v", found, err)
	}
	if state.Attempts != 1 {
		t.Fatalf("attempts = %d after 40 polls inside one debounce window, want 1", state.Attempts)
	}
}

// Two independent workers producing nothing is the network's evidence, not one
// worker's opinion. The per-session bound is 3, so reaching the no-output
// threshold of 6 always takes two of them.
func TestACoordinateTwoWorkersCannotAuthorIsWithheld(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for _, session := range []string{"writer-a", "writer-b"} {
		var handouts int
		handouts, now = drainSession(t, store, session, now)
		if handouts != AuthoringMaxSessionHandouts {
			t.Fatalf("%s got %d handouts, want the per-writer bound of %d",
				session, handouts, AuthoringMaxSessionHandouts)
		}
	}
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-c", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("fresh worker: ok=%v err=%v", ok, err)
	}
	if work.Name == hopelessName {
		t.Fatalf("fresh worker was handed %q; the coordinate should be withheld", work.Name)
	}

	rows, err := store.ListAuthoringQuarantine(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("quarantine = %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Name != hopelessName {
		t.Fatalf("quarantined %q", row.Name)
	}
	if row.NoOutput < AuthoringNoOutputQuarantine {
		t.Fatalf("noOutput = %d, want at least %d", row.NoOutput, AuthoringNoOutputQuarantine)
	}
	if row.QuarantineReason == "" {
		t.Error("an operator cannot act on a withheld coordinate with no reason beside it")
	}
	if row.QuarantinedAt.IsZero() || row.FirstAttemptAt.IsZero() || row.LastAttemptAt.IsZero() {
		t.Errorf("age is unreadable: quarantinedAt=%v first=%v last=%v",
			row.QuarantinedAt, row.FirstAttemptAt, row.LastAttemptAt)
	}
	// Repeated no output is an inference, not a proof, so it lapses on its own.
	if row.ReopensAt.IsZero() {
		t.Error("an inferred quarantine with no lapse is a permanent exclusion")
	}
	if len(row.History) == 0 {
		t.Error("no attempt history: the evidence for the withholding is missing")
	}
	if len(row.History) > AuthoringHistoryDepth {
		t.Errorf("history = %d entries, want it bounded at %d", len(row.History), AuthoringHistoryDepth)
	}
}

// A registry that will not answer has not said no. An outage that lands on
// every coordinate at once must not empty the board permanently.
func TestATransientOutageDoesNotWithholdAnything(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for _, session := range []string{"writer-a", "writer-b"} {
		for i := 0; i < AuthoringMaxSessionHandouts; i++ {
			if _, ok, err := store.ClaimAuthoringWork(ctx, session, quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
				t.Fatalf("%s handout %d: ok=%v err=%v", session, i, ok, err)
			}
			if _, ok, err := store.ReportAuthoringOutcome(ctx, session, AuthoringTransient, "registry 503", now); err != nil || !ok {
				t.Fatalf("%s report %d: ok=%v err=%v", session, i, ok, err)
			}
			now = now.Add(AuthoringAttemptDebounce)
		}
	}
	rows, err := store.ListAuthoringQuarantine(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("an outage withheld %d coordinates: %+v", len(rows), rows)
	}
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-c", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok || work.Name != hopelessName {
		t.Fatalf("after the outage the coordinate was not offered again: %+v ok=%v err=%v", work, ok, err)
	}
}

// A worker whose own Docker daemon died has measured nothing about the
// coordinate. That failure belongs to the worker.
func TestAWorkerFailingOnItsOwnMachineIsNotEvidenceAboutTheCoordinate(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for i := 0; i < AuthoringExcusedAttempts; i++ {
		if _, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
			t.Fatalf("handout %d: ok=%v err=%v", i, ok, err)
		}
		if _, ok, err := store.ReportAuthoringOutcome(ctx, "writer-a", AuthoringInfrastructure, "docker daemon unreachable", now); err != nil || !ok {
			t.Fatalf("report %d: ok=%v err=%v", i, ok, err)
		}
		now = now.Add(AuthoringAttemptDebounce)
	}
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok || work.Name != hopelessName {
		t.Fatalf("a worker excused for its own failures lost the coordinate: %+v ok=%v err=%v", work, ok, err)
	}
}

// Excusing cannot be unbounded, or a worker stuck in a loop reporting the same
// excuse holds the network's attention forever.
func TestExcusesRunOut(t *testing.T) {
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
		if _, _, err := store.ReportAuthoringOutcome(ctx, "writer-a", AuthoringTransient, "registry 503", now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(AuthoringAttemptDebounce)
	}
	if handouts > AuthoringExcusedAttempts+AuthoringMaxSessionHandouts {
		t.Fatalf("a worker repeating one excuse was handed the coordinate %d times", handouts)
	}
}

// Evidence that nothing callable exists is a measurement of the artifact, not
// of the worker, so it needs far fewer attempts — but two workers, because one
// worker's report is one worker's opinion.
func TestTwoWorkersMeasuringNoCallableSymbolWithholdTheCoordinate(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for _, session := range []string{"writer-a", "writer-b"} {
		if _, ok, err := store.ClaimAuthoringWork(ctx, session, quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
			t.Fatalf("%s handout: ok=%v err=%v", session, ok, err)
		}
		if _, ok, err := store.ReportAuthoringOutcome(ctx, session, AuthoringNoCallableSymbol, "pom-only artifact: no jar, no classes", now); err != nil || !ok {
			t.Fatalf("%s report: ok=%v err=%v", session, ok, err)
		}
		now = now.Add(time.Minute)
	}
	rows, err := store.ListAuthoringQuarantine(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("quarantine = %d rows, want the measured-impossible coordinate withheld", len(rows))
	}
	// An artifact does not grow a jar later, so this one does not lapse on a
	// timer; only an operator lifts it.
	if !rows[0].ReopensAt.IsZero() {
		t.Errorf("a structural withholding lapses at %v; it should need an operator", rows[0].ReopensAt)
	}
}

// One worker saying it cannot be done is not the network saying so.
func TestOneWorkersOpinionDoesNotWithholdACoordinate(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	if _, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
		t.Fatalf("handout: ok=%v err=%v", ok, err)
	}
	if _, _, err := store.ReportAuthoringOutcome(ctx, "writer-a", AuthoringNoCallableSymbol, "could not find a symbol", now); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListAuthoringQuarantine(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("one session's report withheld %d coordinates", len(rows))
	}
	// It is still off that writer's board, though: offering it again would
	// only collect the same answer.
	now = now.Add(AuthoringAttemptDebounce)
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("after the report: ok=%v err=%v", ok, err)
	}
	if work.Name == hopelessName {
		t.Fatal("the writer that measured it impossible was handed it again")
	}
	// Another writer is not bound by that opinion.
	other, ok, err := store.ClaimAuthoringWork(ctx, "writer-b", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok || other.Name != hopelessName {
		t.Fatalf("a second writer was refused unmeasured work: %+v ok=%v err=%v", other, ok, err)
	}
}

// Withholding has to be undoable, and undoing it has to actually put the work
// back on the board rather than leaving a counter that re-withholds instantly.
func TestAnOperatorCanPutWithheldWorkBack(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for _, session := range []string{"writer-a", "writer-b"} {
		if _, ok, err := store.ClaimAuthoringWork(ctx, session, quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
			t.Fatalf("%s handout: ok=%v err=%v", session, ok, err)
		}
		if _, _, err := store.ReportAuthoringOutcome(ctx, session, AuthoringNoCallableSymbol, "no jar", now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	eco, name, version, symbol := hopeless()
	reopened, err := store.ReopenAuthoringQuarantine(ctx, eco, name, version, symbol, now)
	if err != nil || !reopened {
		t.Fatalf("reopen = %v err=%v", reopened, err)
	}
	rows, err := store.ListAuthoringQuarantine(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("still withheld after reopen: %+v", rows)
	}
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-c", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok || work.Name != name {
		t.Fatalf("reopened work was not offered: %+v ok=%v err=%v", work, ok, err)
	}
	// Reopening a second time when nothing is withheld is not an error, it is
	// simply false: an operator clicking twice must not see a failure.
	if again, err := store.ReopenAuthoringQuarantine(ctx, eco, name, version, symbol, now); err != nil || again {
		t.Fatalf("second reopen = %v err=%v, want false and no error", again, err)
	}
}

// The cooldown is the difference between "withheld" and "deleted".
func TestAnInferredWithholdingLapsesOnItsOwn(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for _, session := range []string{"writer-a", "writer-b"} {
		_, now = drainSession(t, store, session, now)
	}
	if rows, err := store.ListAuthoringQuarantine(ctx, now, 10); err != nil || len(rows) != 1 {
		t.Fatalf("before the cooldown: %d rows err=%v, want 1", len(rows), err)
	}
	later := now.Add(AuthoringQuarantineCooldown + time.Hour)
	rows, err := store.ListAuthoringQuarantine(ctx, later, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a lapsed withholding is still listed: %+v", rows)
	}
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-c", quarantineCandidates(), later, later.Add(24*time.Hour))
	if err != nil || !ok || work.Name != hopelessName {
		t.Fatalf("after the cooldown the coordinate was not offered again: %+v ok=%v err=%v", work, ok, err)
	}
	// The second chance is a real one: the counters that took it off the
	// board start again from zero.
	state, found, err := store.AuthoringAttemptState(ctx, "maven", hopelessName, "2.2.20", "")
	if err != nil || !found {
		t.Fatalf("attempt state: found=%v err=%v", found, err)
	}
	if state.NoOutput != 1 {
		t.Errorf("noOutput = %d after the lapse and one fresh handout, want 1", state.NoOutput)
	}
}

// A coordinate that produces a sample has answered the question. Its history
// stays for the audit trail; the counters that withhold work do not.
func TestAPublishedSampleClearsTheAttemptCount(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	work, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("handout: ok=%v err=%v", ok, err)
	}
	now = now.Add(AuthoringAttemptDebounce)
	if _, ok, err := store.ClaimAuthoringWork(ctx, "writer-a", quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
		t.Fatalf("second handout: ok=%v err=%v", ok, err)
	}
	if attached, err := store.AttachAuthoringWorkSample(ctx, "writer-a", work, "sha256:written", now); err != nil || !attached {
		t.Fatalf("attach = %v err=%v", attached, err)
	}
	state, found, err := store.AuthoringAttemptState(ctx, work.Ecosystem, work.Name, work.Version, work.Symbol)
	if err != nil || !found {
		t.Fatalf("attempt state: found=%v err=%v", found, err)
	}
	if state.NoOutput != 0 {
		t.Errorf("noOutput = %d after a sample was written, want 0", state.NoOutput)
	}
	if state.Authored != 1 {
		t.Errorf("authored = %d, want 1", state.Authored)
	}
	if len(state.History) == 0 {
		t.Error("the audit trail was cleared along with the counters")
	}
}

// The panel and the picker must not disagree about what is withheld: an
// operator reading "0 withheld" while the fleet is being refused work is the
// failure this whole ledger exists to make visible.
func TestTheOperatorCountMatchesWhatTheSelectorRefuses(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for _, session := range []string{"writer-a", "writer-b"} {
		if _, ok, err := store.ClaimAuthoringWork(ctx, session, quarantineCandidates(), now, now.Add(24*time.Hour)); err != nil || !ok {
			t.Fatalf("%s handout: ok=%v err=%v", session, ok, err)
		}
		if _, _, err := store.ReportAuthoringOutcome(ctx, session, AuthoringNoCallableSymbol, "no jar", now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	health, err := store.FarmHealthNow(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListAuthoringQuarantine(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if health.WithheldCoordinates != len(rows) {
		t.Fatalf("farm health says %d withheld, the list has %d", health.WithheldCoordinates, len(rows))
	}
	if health.WithheldCoordinates != 1 {
		t.Fatalf("withheld = %d, want 1", health.WithheldCoordinates)
	}
	if health.WithheldByReason[rows[0].QuarantineReason] != 1 {
		t.Errorf("withheld reasons = %v, want the structural reason counted", health.WithheldByReason)
	}
}

// A report can only speak for the work the session actually holds.
func TestAReportWithoutAClaimChangesNothing(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	if _, ok, err := store.ReportAuthoringOutcome(ctx, "writer-a", AuthoringNoCallableSymbol, "nothing", now); err != nil || ok {
		t.Fatalf("report without a claim = ok=%v err=%v, want false", ok, err)
	}
}
