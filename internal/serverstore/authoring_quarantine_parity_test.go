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
		out = append(out, fmt.Sprintf("withheld %s@%s/%s reason=%q needsOperator=%v noOutput=%d impossible=%d attempts=%d",
			row.Name, row.Version, row.Symbol, row.QuarantineReason,
			row.ReopensAt.IsZero(), row.NoOutput, row.SessionsMeasuringImpossible, row.Attempts))
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
