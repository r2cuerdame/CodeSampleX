package daemon

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/search"
)

// POST /local/v1/adoption used to answer 409 for any candidate but the first
// one the search listed, because the offer recorded Results[0] only (#344).
// The CLI shows every result and the human picks; the pick must correlate.
func TestLocalAdoptionAcceptsTheSecondSearchResult(t *testing.T) {
	home := newTestHome(t, nil)
	d, c := startDaemon(t, home)
	ctx := context.Background()
	seedSample(t, d, "sha256:multi-first")
	// A second answer at a different symbol coordinate, so the engine's
	// one-per-coordinate fold keeps both.
	second := seedSample(t, d, "sha256:multi-second")
	second.Case.Symbols = []string{"axios.put"}
	second.Symbols = []string{"axios.put"}
	second.Case.Goal = "upload multipart form with axios put"
	if err := search.SeedSampleDoc(ctx, d.DB, second, "sha256:multi-second", "LOCAL_PASS"); err != nil {
		t.Fatalf("seed second sample: %v", err)
	}

	resp, err := c.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "upload multipart form with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   testEnv(),
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Miss || len(resp.Results) < 2 {
		t.Fatalf("precondition: want two candidates, got miss=%v results=%d", resp.Miss, len(resp.Results))
	}
	if resp.OfferID == "" {
		t.Fatal("search returned no offerId")
	}

	pass := true
	chosen := resp.Results[1].SampleID
	if err := c.Adopt(ctx, AdoptionRequest{OfferID: resp.OfferID, SampleID: chosen, Applied: true, BuildPass: &pass}); err != nil {
		t.Fatalf("adopting the second result under the search's offerId: %v", err)
	}
	hits, err := d.DB.ListHits(ctx, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("ListHits = %+v, %v; want one hit for one search", hits, err)
	}
	if !hits[0].Adopted || hits[0].SampleID != chosen {
		t.Fatalf("hit history should name the adopted candidate, got %+v", hits[0])
	}
	// Replaying the same report is refused; the offer is one-use per candidate.
	if err := c.Adopt(ctx, AdoptionRequest{OfferID: resp.OfferID, SampleID: chosen, Applied: true, BuildPass: &pass}); err == nil {
		t.Fatal("replayed adoption of the same candidate was accepted")
	}
}
