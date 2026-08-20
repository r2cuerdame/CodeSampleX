package httpapi

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func res(id string, score float64, grade domain.MatchGrade, purl, sym string) domain.SearchResult {
	return domain.SearchResult{
		SampleID: id, Score: score, Grade: grade,
		Case: &domain.Case{Packages: []string{purl}, Symbols: []string{sym}},
	}
}

// Two samples for the same coordinate are the same answer twice. Returned
// side by side they burn the caller's result budget and read as corroboration
// — two independent sources agreeing — when they are one coordinate measured
// twice. The corpus carried 37% of these before the work queue was fixed, and
// the ones already published do not disappear on their own.
func TestSearchReturnsOneSamplePerCoordinate(t *testing.T) {
	got := dedupeByCoordinate([]domain.SearchResult{
		res("sha256:a", 0.91, domain.GradeExact, "pkg:npm/axios@1.6.0", "axios.post"),
		res("sha256:b", 0.88, domain.GradeExact, "pkg:npm/axios@1.6.0", "axios.post"),
		res("sha256:c", 0.70, domain.GradeCompatible, "pkg:npm/axios@1.6.0", "axios.get"),
	})
	if len(got) != 2 {
		t.Fatalf("results = %d, want one per coordinate: %+v", len(got), got)
	}
	if got[0].SampleID != "sha256:a" {
		t.Errorf("kept %q; the higher-scoring duplicate is the one to keep", got[0].SampleID)
	}
	if got[1].SampleID != "sha256:c" {
		t.Errorf("a different symbol is a different coordinate and must survive: %+v", got[1])
	}
}

// A sample with no declared coordinate cannot be compared to anything, and
// dropping it would silently lose an answer. It passes through.
func TestUncoordinatedSamplesAreNeverFoldedTogether(t *testing.T) {
	got := dedupeByCoordinate([]domain.SearchResult{
		{SampleID: "sha256:x", Score: 0.9},
		{SampleID: "sha256:y", Score: 0.8},
	})
	if len(got) != 2 {
		t.Errorf("results = %d, want both kept: %+v", len(got), got)
	}
}
