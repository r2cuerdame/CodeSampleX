package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// search_known_solution returns a ranked list and the agent chooses. The
// offer used to record Results[0] only, so report_sample_adoption for the
// second candidate failed with ErrNoEligibleIntervention, and re-searching
// recorded Results[0] again (#344).
func TestReportAdoptionAcceptsAnyCandidateTheSearchReturned(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Mode: config.ModeLocalOnly}

	first := "sha256:" + strings.Repeat("a1", 32)
	second := "sha256:" + strings.Repeat("b2", 32)
	req := domain.SearchRequest{SchemaVersion: 1, Query: "upload JSON with axios"}
	resp := domain.SearchResponse{Results: []domain.SearchResult{
		{SampleID: first, Grade: domain.GradeExact, Evidence: domain.EvidenceSummary{ContractPasses: 1}},
		{SampleID: second, Grade: domain.GradeCompatible, Evidence: domain.EvidenceSummary{ContractPasses: 1}},
	}}
	offerID := recordSearchOutcome(ctx, db, ident, cfg, req, resp)
	if offerID == "" {
		t.Fatal("search with results returned no offerId")
	}
	if n, _ := db.CountHits(ctx); n != 1 {
		t.Fatalf("CountHits = %d, want 1: one search, however many candidates", n)
	}

	pass := true
	out, err := reportAdoption(ctx, db, ident, cfg, offerID, second, true, &pass)
	if err != nil {
		t.Fatalf("adopting Results[1] under the search's offerId: %v", err)
	}
	if !out.Applied || !out.VerifiedOffer {
		t.Fatalf("outcome carries the second candidate's own flags: %+v", out)
	}
	hits, err := db.ListHits(ctx, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("ListHits = %+v, %v", hits, err)
	}
	if !hits[0].Adopted || hits[0].SampleID != second {
		t.Fatalf("history names the adopted candidate, got %+v", hits[0])
	}

	// One-use per (offer, candidate): the same report cannot be replayed, the
	// sibling is still reportable, and a sample the search never returned is
	// still refused.
	if _, err := reportAdoption(ctx, db, ident, cfg, offerID, second, true, &pass); !errors.Is(err, localdb.ErrNoEligibleIntervention) {
		t.Fatalf("replayed report error = %v, want ErrNoEligibleIntervention", err)
	}
	if _, err := reportAdoption(ctx, db, ident, cfg, offerID, first, false, nil); err != nil {
		t.Fatalf("rejecting Results[0] after adopting Results[1]: %v", err)
	}
	never := "sha256:" + strings.Repeat("c3", 32)
	if _, err := reportAdoption(ctx, db, ident, cfg, offerID, never, true, &pass); !errors.Is(err, localdb.ErrNoEligibleIntervention) {
		t.Fatalf("unreturned sample error = %v, want ErrNoEligibleIntervention", err)
	}
}
