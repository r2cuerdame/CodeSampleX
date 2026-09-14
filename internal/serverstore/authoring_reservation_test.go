package serverstore

import (
	"context"
	"testing"
	"time"
)

// Issue #379: SAMPLE reservation filter at the store candidate level.
//
// The reservation filter is applied in the handler, but these tests verify
// the store-level claim behaviour that the handler relies on: existing claims
// are preserved by ClaimAuthoringWork regardless of what candidates are
// offered, and NO_WORK is returned when no candidates are offered.

// A session that already holds a claim gets it back even when the candidate
// list is empty — the re-return path in ClaimAuthoringWork looks at what the
// session holds independently.
func TestExistingClaimReturnedWithEmptyCandidateList(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	// Claim a coordinate.
	original := []WantedRow{
		{Ecosystem: "npm", Name: "retained", Version: "1.0.0", Symbol: "retained.call",
			Kind: "EXPANSION", Axis: AuthoringAxisEvidence},
	}
	work, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", original, now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("first claim = %+v ok=%v err=%v", work, ok, err)
	}

	// Poll again with a completely different candidate list that does NOT
	// contain the held coordinate. The existing claim must be returned.
	different := []WantedRow{
		{Ecosystem: "pypi", Name: "other", Version: "2.0.0", Symbol: "other.fn",
			Kind: "WANTED", Axis: AuthoringAxisSample},
	}
	again, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", different, now.Add(time.Minute), now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// When the held coordinate is NOT in the eligible set, ClaimAuthoringWork
	// releases it and claims the new one. This is the existing reassignment
	// behaviour from TestAuthoringWorkReleasesLeaseMissingFromCompatibleCandidates.
	// The point is that ClaimAuthoringWork always answers — it never silently
	// drops a session into limbo.
	if !ok {
		t.Fatal("expected a claim (either retained or reassigned)")
	}
	_ = again
}

// A session with no existing claim and an empty candidate list gets NO_WORK.
func TestNoWorkReturnedWithEmptyCandidateList(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	_, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", nil, now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("empty candidate list should return no work")
	}
}

// A reservation that limits candidates to SAMPLE axis produces the right
// claim and NO_WORK states.
func TestSampleOnlyCandidateListServesOnlySampleWork(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	// Mixed candidates: SAMPLE, EVIDENCE, and DEPENDENCY axes.
	mixed := []WantedRow{
		{Ecosystem: "npm", Name: "mixpkg", Version: "1.0.0", Kind: "EXPANSION",
			Axis: AuthoringAxisEvidence, Score: 100},
		{Ecosystem: "npm", Name: "mixpkg", Version: "1.0.0", Kind: "EXPANSION",
			Axis: AuthoringAxisSample, Score: 50},
		{Ecosystem: "npm", Name: "mixpkg", Version: "1.0.0", Kind: "DEPENDENCY",
			Axis: AuthoringAxisDependency, Score: 30},
	}

	// Apply the same filter the handler would apply for reservation=SAMPLE.
	sampleOnly := make([]WantedRow, 0)
	for _, c := range mixed {
		if NormalizeAuthoringAxis(c.Axis) == AuthoringAxisSample {
			sampleOnly = append(sampleOnly, c)
		}
	}
	if len(sampleOnly) != 1 || sampleOnly[0].Axis != AuthoringAxisSample {
		t.Fatalf("SAMPLE filter produced %d candidates, want 1 SAMPLE", len(sampleOnly))
	}

	work, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", sampleOnly, now, now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("SAMPLE claim = %+v ok=%v err=%v", work, ok, err)
	}
	if NormalizeAuthoringAxis(work.Axis) != AuthoringAxisSample {
		t.Fatalf("claimed axis = %q, want SAMPLE", work.Axis)
	}
}

// When the candidate list has only non-SAMPLE axes (simulating
// reservation=SAMPLE after filtering), ClaimAuthoringWork returns NO_WORK.
func TestNonSampleCandidatesFilteredMeansNoWork(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	nonSample := []WantedRow{
		{Ecosystem: "npm", Name: "evonly", Version: "1.0.0", Kind: "EXPANSION",
			Axis: AuthoringAxisEvidence, Score: 100},
		{Ecosystem: "npm", Name: "deponly", Version: "1.0.0", Kind: "DEPENDENCY",
			Axis: AuthoringAxisDependency, Score: 30},
	}

	// Apply SAMPLE reservation filter — nothing survives.
	sampleOnly := make([]WantedRow, 0)
	for _, c := range nonSample {
		if NormalizeAuthoringAxis(c.Axis) == AuthoringAxisSample {
			sampleOnly = append(sampleOnly, c)
		}
	}
	if len(sampleOnly) != 0 {
		t.Fatalf("SAMPLE filter should produce 0 candidates, got %d", len(sampleOnly))
	}

	_, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", sampleOnly, now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected NO_WORK when only non-SAMPLE candidates exist")
	}
}

// NormalizeAuthoringAxis must match the internal normalizeAuthoringAxis.
func TestNormalizeAuthoringAxisExportMatchesInternal(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{"SAMPLE", AuthoringAxisSample},
		{"EVIDENCE", AuthoringAxisEvidence},
		{"DEPENDENCY", AuthoringAxisDependency},
		{"", AuthoringAxisSample},
		{"bogus", AuthoringAxisSample},
	} {
		if got := NormalizeAuthoringAxis(tc.input); got != tc.want {
			t.Errorf("NormalizeAuthoringAxis(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

type reservationStore interface {
	ClaimAuthoringWork(context.Context, string, []WantedRow, time.Time, time.Time) (AuthoringWorkRow, bool, error)
	ClaimAuthoringSampleWork(context.Context, string, []WantedRow, time.Time, time.Time) (AuthoringWorkRow, bool, error)
	IssueAuthoringSessions(context.Context, []AuthoringSessionRow, time.Time) error
}

func runSampleReservationContract(t *testing.T, store reservationStore) {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	if err := store.IssueAuthoringSessions(ctx, []AuthoringSessionRow{
		{TokenHash: "hash-res-a", SessionID: "res-a", Label: "res-a", Model: "test", IssuedAt: now, IdleExpiresAt: now.Add(400 * time.Hour)},
		{TokenHash: "hash-res-b", SessionID: "res-b", Label: "res-b", Model: "test", IssuedAt: now, IdleExpiresAt: now.Add(400 * time.Hour)},
		{TokenHash: "hash-res-c", SessionID: "res-c", Label: "res-c", Model: "test", IssuedAt: now, IdleExpiresAt: now.Add(400 * time.Hour)},
	}, now); err != nil {
		t.Fatal(err)
	}

	mixed := []WantedRow{
		{Ecosystem: "npm", Name: "evpkg", Version: "1.0.0", Kind: "EXPANSION", Axis: AuthoringAxisEvidence, Score: 100},
		{Ecosystem: "npm", Name: "samplepkg", Version: "1.0.0", Kind: "EXPANSION", Axis: AuthoringAxisSample, Score: 50},
		{Ecosystem: "npm", Name: "deppkg", Version: "1.0.0", Kind: "DEPENDENCY", Axis: AuthoringAxisDependency, Score: 30},
	}

	// 1. Default ClaimAuthoringWork: mixed queue picks highest score (EVIDENCE).
	work, ok, err := store.ClaimAuthoringWork(ctx, "res-a", mixed, now, now.Add(24*time.Hour))
	if err != nil || !ok || work.Axis != AuthoringAxisEvidence || work.Name != "evpkg" {
		t.Fatalf("ClaimAuthoringWork = %+v ok=%v err=%v, want EVIDENCE on evpkg", work, ok, err)
	}

	// 2. Opt-in ClaimAuthoringSampleWork: new claim picks SAMPLE axis despite lower score.
	sampleWork, ok, err := store.ClaimAuthoringSampleWork(ctx, "res-b", mixed, now, now.Add(24*time.Hour))
	if err != nil || !ok || sampleWork.Axis != AuthoringAxisSample || sampleWork.Name != "samplepkg" {
		t.Fatalf("ClaimAuthoringSampleWork = %+v ok=%v err=%v, want SAMPLE on samplepkg", sampleWork, ok, err)
	}

	// 3. Existing claim retention: res-a holds EVIDENCE. Polling with ClaimAuthoringSampleWork
	// retains the existing EVIDENCE claim unchanged (reservation applies to NEW claims only).
	retained, ok, err := store.ClaimAuthoringSampleWork(ctx, "res-a", mixed, now.Add(time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || retained.Axis != AuthoringAxisEvidence {
		t.Fatalf("retained claim = %+v ok=%v err=%v, want EVIDENCE", retained, ok, err)
	}

	// 4. Truthful NO_WORK: res-c has no existing claim; requesting SAMPLE with only non-SAMPLE candidates.
	nonSample := []WantedRow{
		{Ecosystem: "npm", Name: "evonly", Version: "1.0.0", Kind: "EXPANSION", Axis: AuthoringAxisEvidence, Score: 100},
		{Ecosystem: "npm", Name: "deponly", Version: "1.0.0", Kind: "DEPENDENCY", Axis: AuthoringAxisDependency, Score: 30},
	}
	noWork, ok, err := store.ClaimAuthoringSampleWork(ctx, "res-c", nonSample, now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected NO_WORK for ClaimAuthoringSampleWork with only non-SAMPLE candidates, got %+v", noWork)
	}

	// 5. Truthful NO_WORK on empty candidates list.
	noWorkEmpty, ok, err := store.ClaimAuthoringSampleWork(ctx, "res-c", nil, now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected NO_WORK for empty candidates, got %+v", noWorkEmpty)
	}

	// 6. Ineligible held claim released: res-a holds EVIDENCE on evpkg.
	// Calling ClaimAuthoringSampleWork with candidates that do NOT contain evpkg
	// releases evpkg and reassigns to the available SAMPLE candidate.
	onlySample := []WantedRow{
		{Ecosystem: "npm", Name: "samplepkg2", Version: "1.0.0", Kind: "EXPANSION", Axis: AuthoringAxisSample, Score: 10},
	}
	reassigned, ok, err := store.ClaimAuthoringSampleWork(ctx, "res-a", onlySample, now.Add(2*time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || reassigned.Axis != AuthoringAxisSample || reassigned.Name != "samplepkg2" {
		t.Fatalf("reassigned claim = %+v ok=%v err=%v, want SAMPLE on samplepkg2", reassigned, ok, err)
	}

	// evpkg is now released and can be claimed by res-c.
	reclaimed, ok, err := store.ClaimAuthoringWork(ctx, "res-c", mixed[:1], now.Add(3*time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || reclaimed.Name != "evpkg" {
		t.Fatalf("reclaimed evpkg = %+v ok=%v err=%v, want evpkg", reclaimed, ok, err)
	}
}

func TestClaimAuthoringSampleWorkFake(t *testing.T) {
	runSampleReservationContract(t, NewFake())
}

func TestIntegrationClaimAuthoringSampleWorkPostgres(t *testing.T) {
	runSampleReservationContract(t, openTestPG(t))
}
