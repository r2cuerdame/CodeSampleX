package mcp

import (
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func TestRecordedVerifiedOfferRequiresReusableContractPass(t *testing.T) {
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	record := func(id string, result domain.SearchResult) {
		t.Helper()
		result.SampleID = id
		result.ExactFailureMatched = true
		recordSearchOutcome(t.Context(), db, nil, nil, domain.SearchRequest{}, domain.SearchResponse{
			SchemaVersion: 1, Results: []domain.SearchResult{result},
		})
	}
	record("sha256:adapt", domain.SearchResult{
		Grade:      domain.GradeAdaptationRequired,
		Adaptation: []string{"change imports"},
		Evidence:   domain.EvidenceSummary{ContractPasses: 1},
	})
	record("sha256:different", domain.SearchResult{
		Grade:     domain.GradeCompatible,
		Different: []string{"Sample uses linux"},
		Evidence:  domain.EvidenceSummary{ContractPasses: 1},
	})
	record("sha256:unverified", domain.SearchResult{Grade: domain.GradeExact})
	record("sha256:verified", domain.SearchResult{
		Grade:    domain.GradeCompatible,
		Evidence: domain.EvidenceSummary{ContractPasses: 1},
	})

	stats, err := db.InterventionSummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.ExactFailureMatches != 4 {
		t.Errorf("exactFailureMatches = %d, want 4", stats.ExactFailureMatches)
	}
	if stats.VerifiedDetoursOffered != 1 {
		t.Errorf("verifiedDetoursOffered = %d, want only the reusable contract PASS", stats.VerifiedDetoursOffered)
	}
	queued, err := db.QueuePending(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Errorf("local intervention recording changed upload queue: %+v", queued)
	}
}
