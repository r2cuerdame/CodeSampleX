package serverstore

import (
	"encoding/json"
	"testing"
	"time"
)

// budgetFixture builds one authoring_attempts row the way the store writes
// it: the ledger's own transitions, driven with explicit timestamps.
type budgetFixture struct {
	t      *testing.T
	ledger *authoringLedger
}

func newBudgetFixture(t *testing.T, name, symbol string) *budgetFixture {
	return &budgetFixture{t: t, ledger: newAuthoringLedger("golang", name, "v1.0.0", symbol)}
}

func (f *budgetFixture) handout(session string, at time.Time) *budgetFixture {
	f.ledger.handout("WANTED", AuthoringAxisSample, session, at)
	return f
}

func (f *budgetFixture) report(session string, outcome AuthoringOutcome, at time.Time) *budgetFixture {
	f.ledger.report(session, outcome, "", at)
	return f
}

func (f *budgetFixture) authored(session string, at time.Time) *budgetFixture {
	f.ledger.authored(session, at)
	return f
}

func (f *budgetFixture) row() AuthoringBudgetRow {
	raw, err := json.Marshal(f.ledger)
	if err != nil {
		f.t.Fatal(err)
	}
	return AuthoringBudgetRow{Ecosystem: f.ledger.Ecosystem, Name: f.ledger.Name, Version: f.ledger.Version,
		Symbol: f.ledger.Symbol, Ledger: raw}
}

var budgetT0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func budgetAt(minutes float64) time.Time {
	return budgetT0.Add(time.Duration(minutes * float64(time.Minute)))
}

// Three slots of one machine, as on csx-farm-linux-1: the older sessions
// carry no computer name and are attributed through the label.
var budgetSessions = []AuthoringBudgetSession{
	{SessionID: "s1", Label: "farm-1-slot1", ComputerName: "farm-1"},
	{SessionID: "s2", Label: "farm-1-slot2"},
	{SessionID: "s3", Label: "farm-1-slot3", ComputerName: "farm-1"},
	{SessionID: "p2", Label: "farm-2-slot1", ComputerName: "farm-2"},
}

func measureBudget(t *testing.T, rows ...AuthoringBudgetRow) AuthoringBudgetReport {
	t.Helper()
	rep, err := MeasureAuthoringBudget(rows, budgetSessions, DefaultAuthoringBudgetOptions())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func budgetOption(t *testing.T, rep AuthoringBudgetReport, name string) AuthoringBudgetOption {
	t.Helper()
	for _, o := range rep.Options {
		if o.Name == name {
			return o
		}
	}
	t.Fatalf("no option %q in %+v", name, rep.Options)
	return AuthoringBudgetOption{}
}

// The #149 shape: one writer times out twice, a second session of the SAME
// machine picks the coordinate up and authors it. The success counts as a
// second-writer success, the two writers as one peer, and the timeout-like
// attempts are recorded as having been followed by a success rather than as
// evidence against the coordinate.
func TestAuthoringBudgetSecondSessionOfOneMachine(t *testing.T) {
	row := newBudgetFixture(t, "example.com/hard", "Hard").
		handout("s1", budgetAt(0)).
		handout("s1", budgetAt(50)).
		handout("s2", budgetAt(101)).
		authored("s2", budgetAt(113)).
		row()
	rep := measureBudget(t, row)

	if rep.Episodes != 1 || rep.Overall.Succeeded != 1 {
		t.Fatalf("episodes=%d succeeded=%d, want 1/1", rep.Episodes, rep.Overall.Succeeded)
	}
	if got := rep.Overall.AttemptsToSuccess[3]; got != 1 {
		t.Fatalf("attemptsToSuccess = %v, want one success on attempt 3", rep.Overall.AttemptsToSuccess)
	}
	if rep.SuccessByWriterOrdinal[2] != 1 {
		t.Fatalf("successByWriterOrdinal = %v, want the second writer", rep.SuccessByWriterOrdinal)
	}
	if rep.MultiWriterEpisodes != 1 || rep.MultiWriterEpisodesSinglePeer != 1 || rep.PeersPerEpisode[1] != 1 {
		t.Fatalf("multi=%d singlePeer=%d peers=%v, want two sessions of one machine",
			rep.MultiWriterEpisodes, rep.MultiWriterEpisodesSinglePeer, rep.PeersPerEpisode)
	}
	if rep.SlotsPerEpisode[2] != 1 {
		t.Fatalf("slotsPerEpisode = %v, want two local slots", rep.SlotsPerEpisode)
	}
	// s1's first attempt ran 50 minutes into its own next handout; the
	// second is closed by another writer, so only an upper bound is known.
	if rep.TimeoutLikeAttempts != 2 || rep.TimeoutLikeThenSucceeded != 1 {
		t.Fatalf("timeoutLike=%d thenSucceeded=%d, want 2/1", rep.TimeoutLikeAttempts, rep.TimeoutLikeThenSucceeded)
	}
	if rep.AttemptEnds[budgetEndSameSession] != 1 || rep.AttemptEnds[budgetEndOtherSession] != 1 || rep.AttemptEnds[budgetEndAuthored] != 1 {
		t.Fatalf("attemptEnds = %v", rep.AttemptEnds)
	}
	// Observed slot time: 50 (s1→s1) + 12 (s2 authored). The s1→s2 close is
	// not observed and does not count.
	if got := rep.Overall.SuccessSlotMinutes.Max; got != 62 {
		t.Fatalf("success slot minutes = %v, want 62", got)
	}
	if rep.Peers["farm-1"] != 3 || rep.Peers["farm-2"] != 1 {
		t.Fatalf("peers = %v, want label fallback to attribute s2 to farm-1", rep.Peers)
	}
}

// A 60 slot-minute budget stops the episode above before its third attempt
// and so loses the success; peer independence does not, because the
// machine had only two charged handouts. The initial-timeout option never
// loses a success, and pays for the long first attempt only where it
// authored nothing.
func TestAuthoringBudgetOptionsPriceTheSameHistory(t *testing.T) {
	success := newBudgetFixture(t, "example.com/hard", "Hard").
		handout("s1", budgetAt(0)).
		handout("s1", budgetAt(50)).
		handout("s2", budgetAt(101)).
		authored("s2", budgetAt(113)).
		row()
	// Four charged handouts on one machine, success on the fourth.
	fourth := newBudgetFixture(t, "example.com/fourth", "F").
		handout("s1", budgetAt(0)).
		handout("s1", budgetAt(10)).
		handout("s1", budgetAt(20)).
		handout("s2", budgetAt(30)).
		authored("s2", budgetAt(35)).
		row()
	// Never authored: six 50-minute no-output attempts across two sessions
	// until the no-output quarantine withholds it.
	hopeless := newBudgetFixture(t, "example.com/hopeless", "H")
	for i := 0; i < 6; i++ {
		session := "s1"
		if i >= 3 {
			session = "s3"
		}
		hopeless.handout(session, budgetAt(float64(i*50)))
	}
	if !hopeless.ledger.Withheld(budgetAt(301)) {
		t.Fatal("fixture: six no-output handouts must withhold the coordinate")
	}
	rep := measureBudget(t, success, fourth, hopeless.row())

	current := budgetOption(t, rep, "current: no-output quarantine at 6 charged handouts, 3 per session")
	if current.SuccessesLost != 0 {
		t.Fatalf("current thresholds replayed over their own history lost %d", current.SuccessesLost)
	}
	peer := budgetOption(t, rep, "peer independence: 3 charged handouts per machine, park when every machine is exhausted")
	if peer.SuccessesLost != 1 || peer.LostExamples[0] != "golang:example.com/fourth@v1.0.0#F" {
		t.Fatalf("peer independence lost %d %v, want only the fourth-attempt success", peer.SuccessesLost, peer.LostExamples)
	}
	// It parks the hopeless coordinate after three: 3 × 50 saved, of which
	// 100 were observed (the last attempt has no close) — upper bound 150.
	if peer.SlotMinutesSaved != 150 || peer.SlotMinutesSavedObserved != 100 {
		t.Fatalf("peer saved %v (observed %v), want 150 (100)", peer.SlotMinutesSaved, peer.SlotMinutesSavedObserved)
	}
	budget60 := budgetOption(t, rep, "slot budget: 60 slot-minutes per episode")
	if budget60.SuccessesLost != 1 || budget60.SuccessesLostBeyondCurrent != 1 || budget60.LostExamples[0] != "golang:example.com/hard@v1.0.0#Hard" {
		t.Fatalf("60-minute budget lost %d (beyond current %d) %v", budget60.SuccessesLost, budget60.SuccessesLostBeyondCurrent, budget60.LostExamples)
	}
	if budget60.MaxEpisodeSlotHours > 2 {
		t.Fatalf("60-minute budget still spends %v slot-hours on one episode", budget60.MaxEpisodeSlotHours)
	}
	initial := budgetOption(t, rep, "initial timeout 15m, one escalation to 50m")
	// hard: first attempt 50m, no sample → saves 35; hopeless: same → 35;
	// fourth: first attempt 10m ≤ 15m → nothing.
	if initial.SuccessesLost != 0 || initial.SlotMinutesSaved != 70 || initial.SlotMinutesSavedObserved != 70 {
		t.Fatalf("initial timeout: %+v", initial)
	}
	// (6 charged + 4 excused) × 50 minutes.
	if rep.CurrentPolicyCeilingSlotHours != 8.3 {
		t.Fatalf("policy ceiling = %v slot-hours, want 8.3", rep.CurrentPolicyCeilingSlotHours)
	}
	if rep.WithheldByReason["repeated no output"] != 1 {
		t.Fatalf("withheldByReason = %v", rep.WithheldByReason)
	}
	if len(rep.Expensive) == 0 || rep.Expensive[0].Coordinate != "example.com/hopeless@v1.0.0#H" || rep.Expensive[0].Outcome != "WITHHELD" {
		t.Fatalf("most expensive = %+v", rep.Expensive)
	}
}

// A writer's own failure is refunded and is not a charge against the
// coordinate, so it neither counts toward a stop rule nor as a timeout.
func TestAuthoringBudgetRefundedAttemptsAreNotCharged(t *testing.T) {
	row := newBudgetFixture(t, "example.com/infra", "I").
		handout("s1", budgetAt(0)).
		report("s1", AuthoringInfrastructure, budgetAt(49)).
		handout("s1", budgetAt(50)).
		handout("s1", budgetAt(55)).
		handout("s1", budgetAt(60)).
		authored("s1", budgetAt(65)).
		row()
	rep := measureBudget(t, row)
	if rep.TimeoutLikeAttempts != 0 {
		t.Fatalf("a refunded 49-minute attempt counted as a timeout")
	}
	peer := budgetOption(t, rep, "peer independence: 3 charged handouts per machine, park when every machine is exhausted")
	if peer.SuccessesLost != 0 {
		t.Fatalf("peer rule charged the refunded attempt: lost %d", peer.SuccessesLost)
	}
	if rep.AttemptEnds["REPORTED_INFRASTRUCTURE"] != 1 {
		t.Fatalf("attemptEnds = %v", rep.AttemptEnds)
	}
}

// History is bounded. Handouts that fell out of the window are attributed to
// the first episode only when no success fell out with them; otherwise the
// episode is inexact and stays out of the attempts-to-success histogram.
func TestAuthoringBudgetTruncatedHistory(t *testing.T) {
	exact := newBudgetFixture(t, "example.com/long", "L")
	for i := 0; i < AuthoringHistoryDepth+2; i++ {
		exact.handout("s1", budgetAt(float64(i*6)))
		if i%3 == 2 {
			// Keep the fixture below the withholding thresholds.
			exact.report("s1", AuthoringTransient, budgetAt(float64(i*6+1)))
		}
	}
	exact.authored("s1", budgetAt(200))
	if len(exact.ledger.History) != AuthoringHistoryDepth {
		t.Fatalf("fixture: history %d, want it truncated to %d", len(exact.ledger.History), AuthoringHistoryDepth)
	}
	rep := measureBudget(t, exact.row())
	if rep.TruncatedRows != 1 || rep.InexactEpisodes != 0 {
		t.Fatalf("truncated=%d inexact=%d, want 1/0", rep.TruncatedRows, rep.InexactEpisodes)
	}
	if rep.Overall.AttemptsToSuccess[exact.ledger.Attempts] != 1 {
		t.Fatalf("attemptsToSuccess = %v, want the full %d from the counter", rep.Overall.AttemptsToSuccess, exact.ledger.Attempts)
	}

	// A success that fell out of the window makes the first episode inexact.
	lost := newBudgetFixture(t, "example.com/twice", "T").
		handout("s1", budgetAt(0)).
		authored("s1", budgetAt(3))
	for i := 0; i < AuthoringHistoryDepth; i++ {
		lost.handout("s1", budgetAt(float64(10+i*6)))
	}
	rep = measureBudget(t, lost.row())
	if rep.InexactEpisodes != 1 || len(rep.Overall.AttemptsToSuccess) != 0 {
		t.Fatalf("inexact=%d attemptsToSuccess=%v, want the partial episode excluded", rep.InexactEpisodes, rep.Overall.AttemptsToSuccess)
	}

	// The window opens on the success itself: its handout fell out, and it
	// must not be charged to the episode that follows.
	opensOnSuccess := newBudgetFixture(t, "example.com/opens", "O").
		handout("s1", budgetAt(0)).
		authored("s1", budgetAt(3))
	for i := 0; i < AuthoringHistoryDepth-1; i++ {
		opensOnSuccess.handout("s3", budgetAt(float64(10+i*6)))
	}
	if first := opensOnSuccess.ledger.History[0]; first.Outcome != AuthoringAuthored {
		t.Fatalf("fixture: history opens on %s, want AUTHORED", first.Outcome)
	}
	rep = measureBudget(t, opensOnSuccess.row())
	if rep.Episodes != 2 || rep.Overall.Succeeded != 1 || rep.InexactEpisodes != 1 {
		t.Fatalf("episodes=%d succeeded=%d inexact=%d, want 2/1/1", rep.Episodes, rep.Overall.Succeeded, rep.InexactEpisodes)
	}
	if rep.WritersPerEpisode[0] != 0 || rep.Overall.SuccessSlotMinutes.N != 0 {
		t.Fatalf("an episode with no handout in the window was counted: writers=%v successMinutes=%+v",
			rep.WritersPerEpisode, rep.Overall.SuccessSlotMinutes)
	}
	if len(rep.Expensive) == 0 || rep.Expensive[0].Attempts != AuthoringHistoryDepth-1 || rep.Expensive[0].HistoryIncomplete {
		t.Fatalf("following episode = %+v, want exactly its own %d handouts", rep.Expensive, AuthoringHistoryDepth-1)
	}
}

// The ledger records AUTHORED only for Sample drafts. An Evidence or
// Dependency episode that looks open is not a failure the ledger can see,
// so it is reported by axis and kept out of the priced options.
func TestAuthoringBudgetOnlyPricesSampleEpisodes(t *testing.T) {
	l := newAuthoringLedger("npm", "left-pad", "1.3.0", "")
	l.handout("DEPENDENCY", AuthoringAxisDependency, "s1", budgetAt(0))
	l.handout("DEPENDENCY", AuthoringAxisDependency, "s1", budgetAt(50))
	l.handout("DEPENDENCY", AuthoringAxisDependency, "s1", budgetAt(100))
	raw, _ := json.Marshal(l)
	rep := measureBudget(t, AuthoringBudgetRow{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", Ledger: raw})
	if rep.ByAxis[AuthoringAxisDependency].Open != 1 {
		t.Fatalf("byAxis = %+v", rep.ByAxis)
	}
	for _, o := range rep.Options {
		if o.SlotMinutesSaved != 0 || o.SuccessesLost != 0 {
			t.Fatalf("option %q priced a Dependency episode: %+v", o.Name, o)
		}
	}
}

func TestAuthoringBudgetDistributionNearestRank(t *testing.T) {
	d := budgetDistribution([]float64{5, 1, 4, 2, 3, 6, 7, 8, 9, 10})
	if d.N != 10 || d.P50 != 5 || d.P90 != 9 || d.P99 != 10 || d.Max != 10 {
		t.Fatalf("distribution = %+v", d)
	}
	if (budgetDistribution(nil) != AuthoringBudgetDistribution{}) {
		t.Fatal("empty distribution must be zero")
	}
}

func TestAuthoringBudgetRejectsCorruptLedger(t *testing.T) {
	_, err := MeasureAuthoringBudget([]AuthoringBudgetRow{{Ecosystem: "npm", Name: "x", Version: "1", Ledger: json.RawMessage(`{"attempts":"many"}`)}},
		nil, DefaultAuthoringBudgetOptions())
	if err == nil {
		t.Fatal("a ledger that does not decode must fail the report, not be skipped")
	}
}
