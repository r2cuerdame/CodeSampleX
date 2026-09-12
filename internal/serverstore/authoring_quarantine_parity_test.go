package serverstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The Fake exists so the HTTP tests can assert what production would do. When
// the two stores disagree about the attempt ledger, a test can prove that a
// hopeless coordinate is withheld while production keeps handing it out — so
// the same script is run against both and the answers compared.
//
// The transition rules are shared Go code; what can actually drift is the
// storage around them, which is exactly what this exercises: PostgreSQL round-
// trips the ledger through JSONB, and a map that fails to survive that trip
// silently un-bounds every writer.

// quarantineStore is the slice of a store this parity check needs.
type quarantineStore interface {
	ClaimAuthoringWork(context.Context, string, []WantedRow, time.Time, time.Time) (AuthoringWorkRow, bool, error)
	ReportAuthoringOutcome(context.Context, string, AuthoringOutcome, string, time.Time) (AuthoringWorkRow, bool, error)
	ListAuthoringQuarantine(context.Context, time.Time, int) ([]AuthoringAttemptState, error)
	AuthoringAttemptState(context.Context, string, string, string, string) (AuthoringAttemptState, bool, error)
	ReopenAuthoringQuarantine(context.Context, string, string, string, string, time.Time) (bool, error)
	IssueAuthoringSessions(context.Context, []AuthoringSessionRow, time.Time) error
	// The operations panel reads this. It is in the script because the panel
	// agreeing with the picker is a requirement, not a nicety: an operator
	// reading "0 withheld" while the fleet is being refused work is the
	// failure the ledger exists to make visible.
	FarmHealthNow(context.Context, time.Time) (FarmHealth, error)
}

// quarantineStep is one thing a writer does.
type quarantineStep struct {
	session string
	// outcome empty means "just ask for work".
	outcome AuthoringOutcome
	detail  string
	// advance is how far the clock moves BEFORE the step.
	advance time.Duration
}

func parityCandidates() []WantedRow {
	return []WantedRow{
		{Ecosystem: "maven", Name: "org.jetbrains.kotlin/kotlin-gradle-plugins-bom", Version: "2.2.20", Symbol: "", Kind: "EXPANSION"},
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Kind: "WANTED"},
		{Ecosystem: "npm", Name: "zod", Version: "4.1.0", Symbol: "z.object", Kind: "WANTED"},
		{Ecosystem: "pypi", Name: "httpx", Version: "0.28.1", Symbol: "httpx.get", Kind: "WANTED"},
	}
}

// runQuarantineScript plays the steps and returns one line per step describing
// what the store handed out, then the withheld list.
func runQuarantineScript(t *testing.T, store quarantineStore, steps []quarantineStep, start time.Time) []string {
	t.Helper()
	ctx := context.Background()
	// PostgreSQL deletes assignments whose session has idled out, so the
	// sessions have to exist there. The Fake leaves an unknown session's claim
	// alone, so seeding them keeps the two asking the same question.
	sessions := map[string]bool{}
	var rows []AuthoringSessionRow
	for _, step := range steps {
		if sessions[step.session] {
			continue
		}
		sessions[step.session] = true
		rows = append(rows, AuthoringSessionRow{
			TokenHash: "hash-" + step.session, SessionID: step.session, Label: step.session,
			Model: "test", Reasoning: "low", IssuedAt: start,
			IdleExpiresAt: start.Add(400 * time.Hour),
		})
	}
	if err := store.IssueAuthoringSessions(ctx, rows, start); err != nil {
		t.Fatal(err)
	}

	now := start
	out := make([]string, 0, len(steps)+4)
	for i, step := range steps {
		now = now.Add(step.advance)
		if step.outcome != "" {
			work, ok, err := store.ReportAuthoringOutcome(ctx, step.session, step.outcome, step.detail, now)
			if err != nil {
				t.Fatalf("step %d report: %v", i, err)
			}
			out = append(out, fmt.Sprintf("%02d %s report %s -> ok=%v %s", i, step.session, step.outcome, ok, work.Name))
			continue
		}
		work, ok, err := store.ClaimAuthoringWork(ctx, step.session, parityCandidates(), now, now.Add(24*time.Hour))
		if err != nil {
			t.Fatalf("step %d claim: %v", i, err)
		}
		out = append(out, fmt.Sprintf("%02d %s claim -> ok=%v %s", i, step.session, ok, work.Name))
	}
	withheld, err := store.ListAuthoringQuarantine(ctx, now, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range withheld {
		out = append(out, fmt.Sprintf("withheld %s@%s/%s reason=%q needsOperator=%v noOutput=%d excused=%d impossible=%d unsupported=%d attempts=%d",
			row.Name, row.Version, row.Symbol, row.QuarantineReason,
			row.ReopensAt.IsZero(), row.NoOutput, row.Excused, row.SessionsMeasuringImpossible,
			row.SessionsMeasuringUnsupported, row.Attempts))
	}
	health, err := store.FarmHealthNow(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, fmt.Sprintf("panel withheld=%d listed=%d byReason=%v",
		health.WithheldCoordinates, len(withheld), health.WithheldByReason))
	return out
}

func TestIntegrationAuthoringQuarantineFakeMatchesPostgres(t *testing.T) {
	debounce := AuthoringAttemptDebounce
	scenarios := []struct {
		name  string
		steps []quarantineStep
	}{
		{
			// The incident: one live writer asking over and over. It must be
			// moved on, and the coordinate must survive one writer's failure.
			name: "one writer keeps asking",
			steps: []quarantineStep{
				{session: "a"}, {session: "a", advance: debounce}, {session: "a", advance: debounce},
				{session: "a", advance: debounce}, {session: "a", advance: debounce},
			},
		},
		{
			// Two writers producing nothing is the network's evidence.
			name: "two writers produce nothing",
			steps: []quarantineStep{
				{session: "a"}, {session: "a", advance: debounce}, {session: "a", advance: debounce},
				{session: "a", advance: debounce},
				{session: "b", advance: debounce}, {session: "b", advance: debounce},
				{session: "b", advance: debounce}, {session: "b", advance: debounce},
				{session: "c", advance: debounce},
			},
		},
		{
			// A registry that would not answer has not said no.
			name: "an outage is excused",
			steps: []quarantineStep{
				{session: "a"}, {session: "a", outcome: AuthoringTransient, detail: "registry 503"},
				{session: "b", advance: debounce}, {session: "b", outcome: AuthoringTransient, detail: "registry 503"},
				{session: "c", advance: debounce}, {session: "c", outcome: AuthoringInfrastructure, detail: "no docker"},
				{session: "a", advance: debounce},
			},
		},
		{
			// Two independent measurements that nothing callable exists.
			name: "measured impossible by two writers",
			steps: []quarantineStep{
				{session: "a"}, {session: "a", outcome: AuthoringNoCallableSymbol, detail: "pom-only: no jar"},
				{session: "b", advance: time.Minute}, {session: "b", outcome: AuthoringNoCallableSymbol, detail: "pom-only: no jar"},
				{session: "c", advance: time.Minute},
			},
		},
		{
			// #364: one writer repeating the same excuse is refunded once, not
			// every time. The per-writer refund map has to survive the JSONB
			// round trip or production keeps handing the coordinate back.
			name: "one writer repeats infrastructure",
			steps: []quarantineStep{
				{session: "a"}, {session: "a", outcome: AuthoringInfrastructure, detail: "pub@1 lacks Flutter"},
				{session: "a", advance: debounce}, {session: "a", outcome: AuthoringInfrastructure, detail: "pub@1 lacks Flutter"},
				{session: "a", advance: debounce}, {session: "a", outcome: AuthoringInfrastructure, detail: "pub@1 lacks Flutter"},
				{session: "a", advance: debounce}, {session: "a", outcome: AuthoringInfrastructure, detail: "pub@1 lacks Flutter"},
				{session: "a", advance: debounce}, {session: "a", outcome: AuthoringInfrastructure, detail: "pub@1 lacks Flutter"},
				{session: "b", advance: debounce},
			},
		},
		{
			// #364: two independent measurements that no verifier image can
			// build it. Not refunded, and the set of writers must round-trip.
			name: "measured unsupported by two writers",
			steps: []quarantineStep{
				{session: "a"}, {session: "a", outcome: AuthoringUnsupportedEnvironment, detail: "requires Flutter SDK"},
				{session: "a", advance: debounce},
				{session: "b", advance: time.Minute}, {session: "b", outcome: AuthoringUnsupportedEnvironment, detail: "requires Flutter SDK"},
				{session: "c", advance: time.Minute},
			},
		},
		{
			// One of each terminal measurement is not two writers agreeing.
			name: "impossible and unsupported do not pool",
			steps: []quarantineStep{
				{session: "a"}, {session: "a", outcome: AuthoringNoCallableSymbol, detail: "no jar"},
				{session: "b", advance: time.Minute}, {session: "b", outcome: AuthoringUnsupportedEnvironment, detail: "no Flutter"},
				{session: "c", advance: time.Minute},
			},
		},
		{
			// Once-unsupported coordinate is deprioritized behind clean work.
			// Session a measures bom as unsupported. Session b asks with clean
			// work (axios, zod, httpx) available and is handed axios rather than bom.
			name: "once-unsupported is deprioritized behind clean candidates",
			steps: []quarantineStep{
				{session: "a"}, {session: "a", outcome: AuthoringUnsupportedEnvironment, detail: "requires Flutter SDK"},
				{session: "b", advance: time.Minute},
			},
		},
	}
	start := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			pg := openTestPG(t)
			fake := NewFake()
			fakeOut := runQuarantineScript(t, fake, sc.steps, start)
			pgOut := runQuarantineScript(t, pg, sc.steps, start)
			if len(fakeOut) != len(pgOut) {
				t.Fatalf("line count differs: fake=%d pg=%d\n fake: %v\n pg:   %v",
					len(fakeOut), len(pgOut), fakeOut, pgOut)
			}
			for i := range pgOut {
				if fakeOut[i] != pgOut[i] {
					t.Errorf("line %d differs\n  fake: %s\n  pg:   %s", i, fakeOut[i], pgOut[i])
				}
			}
		})
	}
}

// Reopening has to work the same way in both, because it is the operator's
// only way back and the one action nobody gets to test twice.
func TestIntegrationAuthoringReopenFakeMatchesPostgres(t *testing.T) {
	pg := openTestPG(t)
	fake := NewFake()
	start := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	steps := []quarantineStep{
		{session: "a"}, {session: "a", outcome: AuthoringNoCallableSymbol, detail: "no jar"},
		{session: "b", advance: time.Minute}, {session: "b", outcome: AuthoringNoCallableSymbol, detail: "no jar"},
	}
	for _, store := range []quarantineStore{fake, pg} {
		runQuarantineScript(t, store, steps, start)
		now := start.Add(time.Hour)
		ctx := context.Background()
		reopened, err := store.ReopenAuthoringQuarantine(ctx, "maven",
			"org.jetbrains.kotlin/kotlin-gradle-plugins-bom", "2.2.20", "", now)
		if err != nil || !reopened {
			t.Fatalf("%T reopen = %v err=%v", store, reopened, err)
		}
		again, err := store.ReopenAuthoringQuarantine(ctx, "maven",
			"org.jetbrains.kotlin/kotlin-gradle-plugins-bom", "2.2.20", "", now)
		if err != nil || again {
			t.Fatalf("%T second reopen = %v err=%v, want false", store, again, err)
		}
		state, found, err := store.AuthoringAttemptState(ctx, "maven",
			"org.jetbrains.kotlin/kotlin-gradle-plugins-bom", "2.2.20", "")
		if err != nil || !found {
			t.Fatalf("%T attempt state: found=%v err=%v", store, found, err)
		}
		// The counters that took it off the board reset; the history stays.
		if state.NoOutput != 0 || state.SessionsMeasuringImpossible != 0 || !state.QuarantinedAt.IsZero() {
			t.Errorf("%T after reopen = %+v", store, state)
		}
		if len(state.History) == 0 || state.Attempts == 0 {
			t.Errorf("%T lost the audit trail: %+v", store, state)
		}
	}
}

// TestIntegrationAuthoringUnsupportedDeprioritizationParity verifies dispatch
// behavior for coordinates measured once as UNSUPPORTED_ENVIRONMENT on both Fake
// and PostgreSQL:
// 1. Clean work wins over once-unsupported coordinate even if unsupported has higher asks/score.
// 2. Once-unsupported is still assigned when it is the only eligible work (fallback).
// 3. Other axes (Evidence, Dependency) are not deprioritized by Sample unsupported evidence.
func TestIntegrationAuthoringUnsupportedDeprioritizationParity(t *testing.T) {
	for _, storeType := range []string{"fake", "pg"} {
		t.Run(storeType, func(t *testing.T) {
			newStore := func(t *testing.T) quarantineStore {
				if storeType == "fake" {
					return NewFake()
				}
				return openTestPG(t)
			}

			// 1. Clean work wins over once-unsupported coordinate even if unsupported has higher asks/score
			t.Run("clean work wins over once-unsupported coordinate", func(t *testing.T) {
				store := newStore(t)
				ctx := context.Background()
				start := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
				sessions := []AuthoringSessionRow{
					{SessionID: "s1", TokenHash: "h1", Label: "s1", Model: "m", Reasoning: "low", IssuedAt: start, IdleExpiresAt: start.Add(100 * time.Hour)},
					{SessionID: "s2", TokenHash: "h2", Label: "s2", Model: "m", Reasoning: "low", IssuedAt: start, IdleExpiresAt: start.Add(100 * time.Hour)},
				}
				if err := store.IssueAuthoringSessions(ctx, sessions, start); err != nil {
					t.Fatal(err)
				}
				candidates := []WantedRow{
					{Ecosystem: "pub", Name: "unsupported_coord", Version: "1.0.0", Symbol: "fn", Asks: 999, Score: 99999, Kind: "WANTED", Axis: AuthoringAxisSample},
					{Ecosystem: "npm", Name: "clean_coord", Version: "1.0.0", Symbol: "fn", Asks: 1, Score: 10, Kind: "WANTED", Axis: AuthoringAxisSample},
				}
				// Writer s1 claims work and gets unsupported_coord (higher asks/score)
				work1, ok, err := store.ClaimAuthoringWork(ctx, "s1", candidates, start, start.Add(24*time.Hour))
				if err != nil || !ok || work1.Name != "unsupported_coord" {
					t.Fatalf("initial claim: ok=%v name=%s err=%v", ok, work1.Name, err)
				}
				// Writer s1 reports UNSUPPORTED_ENVIRONMENT
				if _, ok, err := store.ReportAuthoringOutcome(ctx, "s1", AuthoringUnsupportedEnvironment, "requires Flutter SDK", start); err != nil || !ok {
					t.Fatalf("report: ok=%v err=%v", ok, err)
				}
				// Writer s2 claims work. clean_coord MUST win over unsupported_coord even though unsupported has higher asks/score
				now := start.Add(time.Minute)
				work2, ok, err := store.ClaimAuthoringWork(ctx, "s2", candidates, now, now.Add(24*time.Hour))
				if err != nil || !ok {
					t.Fatalf("claim: ok=%v err=%v", ok, err)
				}
				if work2.Name != "clean_coord" {
					t.Fatalf("expected clean_coord to win over once-unsupported coordinate, got %s", work2.Name)
				}
			})

			// 2. Once-unsupported is still assigned when it is the only eligible work
			t.Run("once-unsupported is assigned when only eligible work", func(t *testing.T) {
				store := newStore(t)
				ctx := context.Background()
				start := time.Date(2026, 8, 22, 11, 0, 0, 0, time.UTC)
				sessions := []AuthoringSessionRow{
					{SessionID: "s1", TokenHash: "h1", Label: "s1", Model: "m", Reasoning: "low", IssuedAt: start, IdleExpiresAt: start.Add(100 * time.Hour)},
					{SessionID: "s2", TokenHash: "h2", Label: "s2", Model: "m", Reasoning: "low", IssuedAt: start, IdleExpiresAt: start.Add(100 * time.Hour)},
					{SessionID: "s3", TokenHash: "h3", Label: "s3", Model: "m", Reasoning: "low", IssuedAt: start, IdleExpiresAt: start.Add(100 * time.Hour)},
				}
				if err := store.IssueAuthoringSessions(ctx, sessions, start); err != nil {
					t.Fatal(err)
				}
				candidates := []WantedRow{
					{Ecosystem: "pub", Name: "solo_unsupported", Version: "1.0.0", Symbol: "fn", Asks: 500, Score: 5000, Kind: "WANTED", Axis: AuthoringAxisSample},
				}
				// Writer s1 claims and reports UNSUPPORTED_ENVIRONMENT
				work1, ok, err := store.ClaimAuthoringWork(ctx, "s1", candidates, start, start.Add(24*time.Hour))
				if err != nil || !ok || work1.Name != "solo_unsupported" {
					t.Fatalf("initial claim: ok=%v name=%s err=%v", ok, work1.Name, err)
				}
				if _, ok, err := store.ReportAuthoringOutcome(ctx, "s1", AuthoringUnsupportedEnvironment, "missing Flutter SDK", start); err != nil || !ok {
					t.Fatalf("report: ok=%v err=%v", ok, err)
				}
				now := start.Add(time.Minute)
				// s1 is barred from repeating the measurement
				if work, ok, err := store.ClaimAuthoringWork(ctx, "s1", candidates, now, now.Add(24*time.Hour)); err != nil || ok {
					t.Fatalf("reporting writer was re-assigned work: ok=%v work=%+v err=%v", ok, work, err)
				}
				// Writer s2 claims work. solo_unsupported is once-unsupported, but is the ONLY eligible candidate.
				// Fallback must assign it.
				work2, ok, err := store.ClaimAuthoringWork(ctx, "s2", candidates, now, now.Add(24*time.Hour))
				if err != nil || !ok {
					t.Fatalf("fallback claim: ok=%v err=%v", ok, err)
				}
				if work2.Name != "solo_unsupported" {
					t.Fatalf("expected fallback assignment of solo_unsupported, got %s", work2.Name)
				}
				// When s2 confirms UNSUPPORTED_ENVIRONMENT, two-writer quarantine triggers
				if _, ok, err := store.ReportAuthoringOutcome(ctx, "s2", AuthoringUnsupportedEnvironment, "confirmed missing Flutter SDK", now); err != nil || !ok {
					t.Fatalf("s2 report: ok=%v err=%v", ok, err)
				}
				state, found, err := store.AuthoringAttemptState(ctx, "pub", "solo_unsupported", "1.0.0", "fn")
				if err != nil || !found {
					t.Fatalf("attempt state: found=%v err=%v", found, err)
				}
				if state.SessionsMeasuringUnsupported != 2 || state.QuarantinedAt.IsZero() {
					t.Fatalf("expected coordinate quarantined after 2 independent writers: %+v", state)
				}
				if state.QuarantineReason != AuthoringReasonUnsupportedEnvironment {
					t.Fatalf("expected quarantine reason %q, got %q", AuthoringReasonUnsupportedEnvironment, state.QuarantineReason)
				}
				// Writer s3 is not offered quarantined coordinate
				now = now.Add(time.Minute)
				if work3, ok, err := store.ClaimAuthoringWork(ctx, "s3", candidates, now, now.Add(24*time.Hour)); err != nil || ok {
					t.Fatalf("quarantined coordinate offered to s3: ok=%v work=%+v err=%v", ok, work3, err)
				}
			})

			// 3. Other axes are not deprioritized by Sample unsupported evidence
			t.Run("other axes not deprioritized by sample unsupported evidence", func(t *testing.T) {
				store := newStore(t)
				ctx := context.Background()
				start := time.Date(2026, 8, 22, 13, 0, 0, 0, time.UTC)
				sessions := []AuthoringSessionRow{
					{SessionID: "s1", TokenHash: "h1", Label: "s1", Model: "m", Reasoning: "low", IssuedAt: start, IdleExpiresAt: start.Add(100 * time.Hour)},
					{SessionID: "s2", TokenHash: "h2", Label: "s2", Model: "m", Reasoning: "low", IssuedAt: start, IdleExpiresAt: start.Add(100 * time.Hour)},
				}
				if err := store.IssueAuthoringSessions(ctx, sessions, start); err != nil {
					t.Fatal(err)
				}
				// s1 claims Sample axis for coord_multi and reports UNSUPPORTED_ENVIRONMENT
				sampleCandidate := []WantedRow{
					{Ecosystem: "npm", Name: "coord_multi", Version: "1.0.0", Symbol: "fn", Asks: 100, Score: 1000, Kind: "WANTED", Axis: AuthoringAxisSample},
				}
				work1, ok, err := store.ClaimAuthoringWork(ctx, "s1", sampleCandidate, start, start.Add(24*time.Hour))
				if err != nil || !ok || work1.Name != "coord_multi" {
					t.Fatalf("initial sample claim: ok=%v name=%s err=%v", ok, work1.Name, err)
				}
				if _, ok, err := store.ReportAuthoringOutcome(ctx, "s1", AuthoringUnsupportedEnvironment, "no image builds it", start); err != nil || !ok {
					t.Fatalf("report: ok=%v err=%v", ok, err)
				}
				now := start.Add(time.Minute)
				// Candidates contain:
				// - Evidence for coord_multi (score 500)
				// - Clean Sample for other_pkg (score 100)
				// Evidence for coord_multi must NOT inherit Sample's unsupported measurement.
				// Therefore Evidence for coord_multi is clean and wins over other_pkg on score.
				candidates := []WantedRow{
					{Ecosystem: "npm", Name: "coord_multi", Version: "1.0.0", Symbol: "", Kind: "EXPANSION", Asks: 50, Score: 500, Axis: AuthoringAxisEvidence},
					{Ecosystem: "npm", Name: "other_pkg", Version: "1.0.0", Symbol: "fn", Asks: 10, Score: 100, Kind: "WANTED", Axis: AuthoringAxisSample},
				}
				work2, ok, err := store.ClaimAuthoringWork(ctx, "s2", candidates, now, now.Add(24*time.Hour))
				if err != nil || !ok {
					t.Fatalf("evidence claim: ok=%v err=%v", ok, err)
				}
				if work2.Name != "coord_multi" || work2.Axis != AuthoringAxisEvidence {
					t.Fatalf("Evidence work was deprioritized by Sample unsupported: got %s axis=%s, want coord_multi axis=EVIDENCE", work2.Name, work2.Axis)
				}

				// Also check Dependency axis for a coordinate with Sample unsupported evidence
				depSampleCandidate := []WantedRow{
					{Ecosystem: "npm", Name: "coord_dep", Version: "1.0.0", Symbol: "fn", Asks: 100, Score: 1000, Kind: "WANTED", Axis: AuthoringAxisSample},
				}
				workDepSample, ok, err := store.ClaimAuthoringWork(ctx, "s1", depSampleCandidate, now, now.Add(24*time.Hour))
				if err != nil || !ok || workDepSample.Name != "coord_dep" {
					t.Fatalf("dep sample claim: ok=%v name=%s err=%v", ok, workDepSample.Name, err)
				}
				if _, ok, err := store.ReportAuthoringOutcome(ctx, "s1", AuthoringUnsupportedEnvironment, "no image builds it", now); err != nil || !ok {
					t.Fatalf("report: ok=%v err=%v", ok, err)
				}
				now = now.Add(time.Minute)
				depCandidates := []WantedRow{
					{Ecosystem: "npm", Name: "coord_dep", Version: "1.0.0", Symbol: "", Kind: "DEPENDENCY", Asks: 50, Score: 500, Axis: AuthoringAxisDependency},
					{Ecosystem: "npm", Name: "other_pkg2", Version: "1.0.0", Symbol: "fn", Asks: 10, Score: 100, Kind: "WANTED", Axis: AuthoringAxisSample},
				}
				work3, ok, err := store.ClaimAuthoringWork(ctx, "s2", depCandidates, now, now.Add(24*time.Hour))
				if err != nil || !ok {
					t.Fatalf("dependency claim: ok=%v err=%v", ok, err)
				}
				if work3.Name != "coord_dep" || work3.Axis != AuthoringAxisDependency {
					t.Fatalf("Dependency work was deprioritized by Sample unsupported: got %s axis=%s, want coord_dep axis=DEPENDENCY", work3.Name, work3.Axis)
				}
			})
		})
	}
}
