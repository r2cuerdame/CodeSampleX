package serverstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
)

func fixCandidate(name, fixed, claim string) FixCandidateRow {
	return FixCandidateRow{Candidate: fixclaims.Candidate{
		SchemaVersion: 1, Ecosystem: "npm", Name: name,
		ClaimedBadVersion: "2.4.0", ClaimedFixedVersion: fixed,
		Claim: claim, SourceURL: "https://github.com/acme/" + name + "/releases/tag/v" + fixed,
		SourceType: fixclaims.SourceReleaseNote, Symbols: []string{"parse"}, Confidence: fixclaims.ConfidenceHigh,
	}}
}

// The queue contract, run against the fake and PostgreSQL: a candidate is
// born CLAIMED_FIX, only receipted runs move it, the lease is what admits a
// run, the capacity ceiling is independent of the authoring lane, and the
// attempt and run caps close a candidate without deleting anything.
func runFixClaimStoreContract(t *testing.T, store FixClaimStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	limits := FixClaimLimits{MaxLeases: 1, MaxAttempts: 2, MaxRuns: 4}
	lease := now.Add(time.Hour)
	fp := strings.Repeat("a", 64)

	high := fixCandidate("foo", "2.4.1", "Fixed crash when parse receives an empty buffer")
	high.Score = 100
	low := fixCandidate("bar", "2.4.1", "Fixed crash when parse receives an empty buffer")
	low.Score = 10
	upserts, err := store.UpsertFixCandidates(ctx, []FixCandidateRow{high, low}, 3, now)
	if err != nil || len(upserts) != 2 {
		t.Fatalf("upsert: %v %d", err, len(upserts))
	}
	for _, u := range upserts {
		if u.Duplicate || u.Row.ID == 0 || u.Row.Status != fixclaims.StatusClaimedFix || u.Row.Closed || u.Row.DedupKey == "" {
			t.Fatalf("born wrong: %+v", u)
		}
	}
	highID, lowID := upserts[0].Row.ID, upserts[1].Row.ID

	// The same claim again is a duplicate and resets nothing.
	again, err := store.UpsertFixCandidates(ctx, []FixCandidateRow{high}, 0, now.Add(time.Minute))
	if err != nil || len(again) != 1 || !again[0].Duplicate || again[0].Row.ID != highID {
		t.Fatalf("duplicate: %v %+v", err, again)
	}

	// Score orders the queue; the handout counts an attempt.
	row, status, err := store.ClaimFixWork(ctx, "writer-a", limits, now, lease)
	if err != nil || status != FixClaimAssigned || row.ID != highID || row.Attempts != 1 || !row.Leased(now) {
		t.Fatalf("claim: %v %s %+v", err, status, row)
	}
	// The same session asking again gets the same row, not a second one.
	same, status, err := store.ClaimFixWork(ctx, "writer-a", limits, now, lease)
	if err != nil || status != FixClaimAssigned || same.ID != highID || same.Attempts != 1 {
		t.Fatalf("reclaim: %v %s %+v", err, status, same)
	}
	// The capacity ceiling is the fleet's, not the session's.
	if _, status, err := store.ClaimFixWork(ctx, "writer-b", limits, now, lease); err != nil || status != FixClaimBudgetExhausted {
		t.Fatalf("budget: %v %s", err, status)
	}

	// A run needs the lease.
	runs := []fixclaims.Run{
		{Version: "2.4.0", Environment: fixclaims.DefaultEnvironment, Verdict: fixclaims.VerdictFail, FailureFingerprint: fp, ReceiptID: "r-bad", SampleID: "s-1", FarmSeconds: 30, ObservedAt: now},
		{Version: "2.4.1", Environment: fixclaims.DefaultEnvironment, Verdict: fixclaims.VerdictPass, ReceiptID: "r-fixed", SampleID: "s-1", FarmSeconds: 25, ObservedAt: now},
	}
	if _, err := store.RecordFixRuns(ctx, highID, "writer-b", runs, limits, now); !errors.Is(err, ErrFixLeaseMissing) {
		t.Fatalf("run without lease: %v", err)
	}
	if _, err := store.RecordFixRuns(ctx, 9999, "writer-a", runs, limits, now); !errors.Is(err, ErrFixCandidateMissing) {
		t.Fatalf("run on missing candidate: %v", err)
	}
	if _, err := store.SetFixReproducer(ctx, highID, "writer-a", fixclaims.ReproducerExistingSample, "s-1", now); err != nil {
		t.Fatal(err)
	}
	verified, err := store.RecordFixRuns(ctx, highID, "writer-a", runs, limits, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Status != fixclaims.StatusVerifiedFix || verified.PairOutcome != fixclaims.PairReproducedAndFixed {
		t.Fatalf("evaluated: %s/%s", verified.Status, verified.PairOutcome)
	}
	if verified.Leased(now) || verified.RunCount != 2 || verified.FarmSeconds != 55 || verified.ReproducerSource != fixclaims.ReproducerExistingSample || verified.ReproducerSampleID != "s-1" {
		t.Fatalf("state after runs: %+v", verified)
	}
	if verified.Evaluation.GoodVersion != "2.4.1" || len(verified.Evaluation.Evidence) != 2 || verified.EvaluatedAt.IsZero() {
		t.Fatalf("evaluation: %+v", verified.Evaluation)
	}
	if verified.Closed {
		t.Fatal("a verified fix under the run cap must stay open for expansion")
	}
	got, found, err := store.GetFixCandidate(ctx, highID)
	if err != nil || !found || got.Status != fixclaims.StatusVerifiedFix || got.Candidate.Name != "foo" {
		t.Fatalf("get: %v %v %+v", err, found, got)
	}
	stored, err := store.ListFixRuns(ctx, highID)
	if err != nil || len(stored) != 2 || stored[0].ReceiptID != "r-bad" || stored[0].SessionID != "writer-a" || stored[1].Verdict != fixclaims.VerdictPass {
		t.Fatalf("runs: %v %+v", err, stored)
	}

	// The lease is free again; the low row is next, and hands back without
	// runs. INFRASTRUCTURE refunds the attempt, NO_OUTPUT does not, and the
	// attempt cap closes the row.
	row, status, err = store.ClaimFixWork(ctx, "writer-b", limits, now, lease)
	if err != nil || status != FixClaimAssigned || row.ID != lowID {
		t.Fatalf("next: %v %s %+v", err, status, row)
	}
	released, err := store.ReleaseFixWork(ctx, lowID, "writer-b", FixOutcomeInfrastructure, "docker died", limits, now)
	if err != nil || released.Attempts != 0 || released.Closed || released.Leased(now) {
		t.Fatalf("refund: %v %+v", err, released)
	}
	// Fewest attempts first: the refunded row (0) is offered before the
	// verified one (1).
	row, status, err = store.ClaimFixWork(ctx, "writer-b", limits, now, lease)
	if err != nil || status != FixClaimAssigned || row.ID != lowID || row.Attempts != 1 {
		t.Fatalf("breadth first: %v %s %+v", err, status, row)
	}
	released, err = store.ReleaseFixWork(ctx, lowID, "writer-b", FixOutcomeNoOutput, "", limits, now)
	if err != nil || released.Closed || released.Attempts != 1 {
		t.Fatalf("under the cap: %v %+v", err, released)
	}
	capOne := limits
	capOne.MaxAttempts = 1
	if _, status, err := store.ClaimFixWork(ctx, "writer-b", limits, now, lease); err != nil || status != FixClaimAssigned {
		t.Fatalf("%v %s", err, status)
	}
	// Ties on attempts go to score, so the verified row was offered; hand it
	// back (a signal keeps it open whatever the count) and take the low one
	// under a cap of one.
	if kept, err := store.ReleaseFixWork(ctx, highID, "writer-b", FixOutcomeNoOutput, "", capOne, now); err != nil || kept.Closed || kept.Attempts != 2 {
		t.Fatalf("verified row closed by the attempt cap: %v %+v", err, kept)
	}
	row, status, err = store.ClaimFixWork(ctx, "writer-b", limits, now, lease)
	if err != nil || status != FixClaimAssigned || row.ID != lowID || row.Attempts != 2 {
		t.Fatalf("low row after tie: %v %s %+v", err, status, row)
	}
	released, err = store.ReleaseFixWork(ctx, lowID, "writer-b", FixOutcomeNoOutput, "", capOne, now)
	if err != nil {
		t.Fatal(err)
	}
	if !released.Closed || released.ClosedReason != FixClosedAttemptCap || released.Status != fixclaims.StatusClaimedFix {
		t.Fatalf("attempt cap: %+v", released)
	}
	// Closed rows are not handed out; the verified row still is (expansion).
	row, status, err = store.ClaimFixWork(ctx, "writer-c", limits, now, lease)
	if err != nil || status != FixClaimAssigned || row.ID != highID {
		t.Fatalf("closed row offered or verified row withheld: %v %s %+v", err, status, row)
	}
	// The run cap closes an expanding record, keeping its status.
	more := []fixclaims.Run{
		{Version: "2.3.9", Environment: fixclaims.DefaultEnvironment, Verdict: fixclaims.VerdictFail, FailureFingerprint: fp, ReceiptID: "r-3", ObservedAt: now},
		{Version: "2.4.2", Environment: fixclaims.DefaultEnvironment, Verdict: fixclaims.VerdictPass, ReceiptID: "r-4", ObservedAt: now},
	}
	capped, err := store.RecordFixRuns(ctx, highID, "writer-c", more, limits, now)
	if err != nil || !capped.Closed || capped.ClosedReason != FixClosedRunCap || capped.Status != fixclaims.StatusVerifiedFix || capped.RunCount != 4 {
		t.Fatalf("run cap: %v %+v", err, capped)
	}
	if capped.Evaluation.BadVersion != "2.3.9" {
		t.Fatalf("boundary did not move down: %+v", capped.Evaluation)
	}
	if _, status, err := store.ClaimFixWork(ctx, "writer-d", limits, now, lease); err != nil || status != FixClaimNoWork {
		t.Fatalf("everything closed: %v %s", err, status)
	}

	// Reads: by package and version (either side), by status, open only.
	list, err := store.ListFixCandidates(ctx, FixClaimQuery{Ecosystem: "NPM", Name: "foo", Version: "2.4.0"}, 10)
	if err != nil || len(list) != 1 || list[0].ID != highID {
		t.Fatalf("query by bad version: %v %+v", err, list)
	}
	list, err = store.ListFixCandidates(ctx, FixClaimQuery{Status: fixclaims.StatusClaimedFix}, 10)
	if err != nil || len(list) != 1 || list[0].ID != lowID {
		t.Fatalf("query by status: %v %+v", err, list)
	}
	list, err = store.ListFixCandidates(ctx, FixClaimQuery{Open: true}, 10)
	if err != nil || len(list) != 0 {
		t.Fatalf("open query: %v %d", err, len(list))
	}
	list, err = store.ListFixCandidates(ctx, FixClaimQuery{}, 10)
	if err != nil || len(list) != 2 || list[0].ID != highID {
		t.Fatalf("all, score first: %v %+v", err, list)
	}

	states, ingested, rejected, err := store.FixClaimStates(ctx)
	if err != nil || len(states) != 2 || ingested != 6 || rejected != 3 {
		t.Fatalf("states: %v %d ingested=%d rejected=%d", err, len(states), ingested, rejected)
	}
	m := fixclaims.Measure(states, ingested, rejected)
	if m.FixConfirmed != 1 || m.Exhausted != 2 || m.FarmSeconds != 55 || m.ReusedSample != 1 {
		t.Fatalf("metrics: %+v", m)
	}

	// An expired lease admits nothing and is not counted against capacity.
	later := lease.Add(time.Minute)
	if _, err := store.RecordFixRuns(ctx, highID, "writer-c", more, limits, later); !errors.Is(err, ErrFixLeaseMissing) {
		t.Fatalf("expired lease admitted a run: %v", err)
	}
}

func TestFakeFixClaimStoreContract(t *testing.T) {
	runFixClaimStoreContract(t, NewFake())
}

func TestIntegrationPGFixClaimStoreMatchesTheFake(t *testing.T) {
	runFixClaimStoreContract(t, openTestPG(t))
}

// A lease that lapses frees the row for another writer, and the fleet
// ceiling counts only live leases.
func runFixClaimLeaseExpiry(t *testing.T, store FixClaimStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	limits := FixClaimLimits{MaxLeases: 1, MaxAttempts: 5, MaxRuns: 12}
	if _, err := store.UpsertFixCandidates(ctx, []FixCandidateRow{fixCandidate("foo", "2.4.1", "Fixed crash when parse receives an empty buffer")}, 0, now); err != nil {
		t.Fatal(err)
	}
	row, status, err := store.ClaimFixWork(ctx, "writer-a", limits, now, now.Add(time.Hour))
	if err != nil || status != FixClaimAssigned {
		t.Fatalf("%v %s", err, status)
	}
	if _, status, _ := store.ClaimFixWork(ctx, "writer-b", limits, now.Add(time.Minute), now.Add(2*time.Hour)); status != FixClaimBudgetExhausted {
		t.Fatalf("live lease not counted: %s", status)
	}
	later := now.Add(2 * time.Hour)
	taken, status, err := store.ClaimFixWork(ctx, "writer-b", limits, later, later.Add(time.Hour))
	if err != nil || status != FixClaimAssigned || taken.ID != row.ID || taken.ClaimedBy != "writer-b" || taken.Attempts != 2 {
		t.Fatalf("lapsed lease not reclaimed: %v %s %+v", err, status, taken)
	}
	if _, err := store.ReleaseFixWork(ctx, row.ID, "writer-a", FixOutcomeNoOutput, "", limits, later); !errors.Is(err, ErrFixLeaseMissing) {
		t.Fatalf("old holder still spoke: %v", err)
	}
}

func TestFakeFixClaimLeaseExpiry(t *testing.T) { runFixClaimLeaseExpiry(t, NewFake()) }

func TestIntegrationPGFixClaimLeaseExpiry(t *testing.T) { runFixClaimLeaseExpiry(t, openTestPG(t)) }
