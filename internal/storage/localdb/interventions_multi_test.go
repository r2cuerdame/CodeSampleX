package localdb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// search_known_solution returns a ranked list, and an agent is free to
// apply the second candidate because it matches its framework dialect
// better than the first. The offer used to record Results[0] only, so that
// report came back ErrNoEligibleIntervention and re-searching could never
// fix it: the new offer recorded Results[0] again.
func TestMultiCandidateOfferAcceptsAdoptionOfAnyReturnedCandidate(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	top, second, third := "sha256:top", "sha256:second", "sha256:third"
	offerID, err := db.RecordSearchOffers(ctx,
		HitRow{Query: "q", Grade: "EXACT", SampleID: top},
		[]InterventionRow{
			{SampleID: top, ExactFailureMatched: true, VerifiedOffer: true},
			{SampleID: second, ExactFailureMatched: true, VerifiedOffer: true},
			{SampleID: third, ExactFailureMatched: false, VerifiedOffer: true},
			// A candidate the ranking listed twice is one offer row, not a
			// collision that makes the whole search fail to record.
			{SampleID: second, ExactFailureMatched: true, VerifiedOffer: true},
		})
	if err != nil {
		t.Fatal(err)
	}
	// One search is one hit, however many candidates it listed.
	if n, err := db.CountHits(ctx); err != nil || n != 1 {
		t.Fatalf("CountHits = %d, %v; want 1 hit row for one search", n, err)
	}
	var candidates int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM interventions WHERE offer_id = ?`, offerID).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 3 {
		t.Fatalf("intervention rows under one offer = %d, want 3 distinct candidates", candidates)
	}

	pass := sql.NullBool{Bool: true, Valid: true}
	out, err := db.CorrelateInterventionAdoption(ctx, offerID, second, true, pass, "")
	if err != nil {
		t.Fatalf("adopting the second candidate: %v", err)
	}
	if !out.ReportedFailureAvoided() {
		t.Fatalf("second candidate carried its own exact+verified flags, got %+v", out)
	}
	hits, err := db.ListHits(ctx, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("ListHits = %+v, %v", hits, err)
	}
	if !hits[0].Adopted || hits[0].SampleID != second || !hits[0].PostBuildPass.Valid || !hits[0].PostBuildPass.Bool {
		t.Fatalf("hit history names the sample actually adopted: %+v", hits[0])
	}
	if n, err := db.CountAdoptions(ctx); err != nil || n != 1 {
		t.Fatalf("CountAdoptions = %d, %v", n, err)
	}

	// Each (offer, candidate) is reportable once; the other candidates of the
	// same offer are still open.
	if _, err := db.CorrelateInterventionAdoption(ctx, offerID, second, true, pass, ""); !errors.Is(err, ErrNoEligibleIntervention) {
		t.Fatalf("replayed second-candidate report error = %v, want ErrNoEligibleIntervention", err)
	}
	if _, err := db.CorrelateInterventionAdoption(ctx, offerID, top, false, sql.NullBool{}, ""); err != nil {
		t.Fatalf("rejecting the top candidate after adopting another: %v", err)
	}
	if _, err := db.CorrelateInterventionAdoption(ctx, offerID, "sha256:never-returned", true, pass, ""); !errors.Is(err, ErrNoEligibleIntervention) {
		t.Fatalf("unreturned candidate error = %v, want ErrNoEligibleIntervention", err)
	}
	// An explicit "did not use this" never downgrades the search's adoption.
	hits, _ = db.ListHits(ctx, 10)
	if len(hits) != 1 || !hits[0].Adopted || hits[0].SampleID != second {
		t.Fatalf("rejecting a sibling candidate changed the adopted hit: %+v", hits)
	}
	if n, _ := db.CountAdoptions(ctx); n != 1 {
		t.Fatalf("CountAdoptions after sibling rejection = %d, want 1", n)
	}

	// The funnel counts searches, not candidates: three candidate rows under
	// one offer are one exact match offered, one applied, one pass.
	stats, err := db.InterventionSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := InterventionStats{
		ExactFailureMatches:     1,
		VerifiedDetoursOffered:  1,
		Applied:                 1,
		PostHitPass:             1,
		ReportedFailuresAvoided: 1,
	}
	if !reflect.DeepEqual(stats, want) {
		t.Errorf("summary = %+v, want %+v", stats, want)
	}
}

func TestMultiCandidateOfferRejectsMalformedCandidateLists(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if _, err := db.RecordSearchOffers(ctx, HitRow{SampleID: "sha256:a"}, nil); err == nil {
		t.Fatal("empty candidate list recorded an offer")
	}
	if _, err := db.RecordSearchOffers(ctx, HitRow{SampleID: "sha256:a"},
		[]InterventionRow{{SampleID: "sha256:b"}, {SampleID: "sha256:a"}}); err == nil {
		t.Fatal("hit row naming a sample other than the first candidate was accepted")
	}
	if _, err := db.RecordSearchOffers(ctx, HitRow{SampleID: "sha256:a"},
		[]InterventionRow{{SampleID: "sha256:a"}, {SampleID: ""}}); err == nil {
		t.Fatal("empty candidate sampleId was accepted")
	}
	if n, _ := db.CountHits(ctx); n != 0 {
		t.Fatalf("a rejected offer left %d hit rows behind", n)
	}
}

// The funnel used to hold pass, fail and unknown apart per row. With several
// candidates per offer the search decides: a pass on any applied candidate is
// a pass for the search, a fail only when nothing applied passed, unknown
// only when nothing applied was measured.
func TestFunnelResolvesSeveralAppliedCandidatesPerSearchOnce(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	pass := sql.NullBool{Bool: true, Valid: true}
	fail := sql.NullBool{Bool: false, Valid: true}
	record := func(a, b string) string {
		t.Helper()
		id, err := db.RecordSearchOffers(ctx, HitRow{SampleID: a}, []InterventionRow{
			{SampleID: a, ExactFailureMatched: true, VerifiedOffer: true},
			{SampleID: b, ExactFailureMatched: true, VerifiedOffer: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	report := func(offer, sample string, build sql.NullBool) {
		t.Helper()
		if _, err := db.CorrelateInterventionAdoption(ctx, offer, sample, true, build, ""); err != nil {
			t.Fatal(err)
		}
	}
	// First candidate failed, second passed: one search, one pass.
	o1 := record("sha256:1a", "sha256:1b")
	report(o1, "sha256:1a", fail)
	report(o1, "sha256:1b", pass)
	// Both failed: one fail.
	o2 := record("sha256:2a", "sha256:2b")
	report(o2, "sha256:2a", fail)
	report(o2, "sha256:2b", fail)
	// One failed, one not yet measured: still a fail, not an unknown.
	o3 := record("sha256:3a", "sha256:3b")
	report(o3, "sha256:3a", fail)
	report(o3, "sha256:3b", sql.NullBool{})
	// Nothing measured: unknown.
	o4 := record("sha256:4a", "sha256:4b")
	report(o4, "sha256:4a", sql.NullBool{})

	stats, err := db.InterventionSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := InterventionStats{
		ExactFailureMatches:     4,
		VerifiedDetoursOffered:  4,
		Applied:                 4,
		PostHitPass:             1,
		PostHitFail:             2,
		PostHitUnknown:          1,
		ReportedFailuresAvoided: 1,
	}
	if !reflect.DeepEqual(stats, want) {
		t.Errorf("summary = %+v, want %+v", stats, want)
	}
}

// Databases created by the single-candidate build carry a UNIQUE index on
// offer_id alone, which refuses the second candidate of the same search.
// Reopening must replace it, not merely add the composite index beside it.
func TestExistingSingleCandidateIndexIsReplacedOnReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "csx.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS interventions_offer_sample_unique`,
		`DROP INDEX IF EXISTS interventions_hit_sample_unique`,
		`CREATE UNIQUE INDEX IF NOT EXISTS interventions_offer_id_unique
		 ON interventions(offer_id) WHERE offer_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS interventions_hit_id_unique
		 ON interventions(hit_id) WHERE hit_id IS NOT NULL`,
	} {
		if _, err := db.sql.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.RecordSearchOffers(ctx, HitRow{SampleID: "sha256:a"}, []InterventionRow{
		{SampleID: "sha256:a"}, {SampleID: "sha256:b"},
	}); err == nil {
		t.Fatal("precondition: the single-candidate index should refuse a second candidate")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if _, err := db.RecordSearchOffers(ctx, HitRow{SampleID: "sha256:a"}, []InterventionRow{
		{SampleID: "sha256:a"}, {SampleID: "sha256:b"},
	}); err != nil {
		t.Fatalf("second candidate still refused after reopen: %v", err)
	}
	for _, stale := range []string{"interventions_offer_id_unique", "interventions_hit_id_unique"} {
		var n int
		if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, stale).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("stale index %s survived migration", stale)
		}
	}
}
