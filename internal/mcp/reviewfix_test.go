package mcp

import (
	"context"
	"encoding/json"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"strings"
	"testing"
)

// These regressions pin the four Codex review findings on PR #49: record only
// the filtered visible outcome, preserve NO_SAFE_MATCH semantics, and explain
// each surviving fact candidate independently.
func TestToolSearchRecordsFilteredVisibleTop(t *testing.T) {
	var recorded domain.SearchResponse
	s := &Server{Deps: &Deps{
		SearchRaw: func(context.Context, domain.SearchRequest) domain.SearchResponse {
			return domain.SearchResponse{Results: []domain.SearchResult{
				{SampleID: "hidden", Grade: domain.GradeCompatible, Confidence: "LOW", Case: &domain.Case{Goal: "format integers", Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"}}},
				{SampleID: "visible", Grade: domain.GradeCompatible, Confidence: "LOW", Case: &domain.Case{Goal: "deploy immutable commit", Packages: []string{"pkg:golang/example.com/deploy@v1.0.0"}}},
			}}
		},
		RecordSearchOutcome: func(_ context.Context, _ domain.SearchRequest, resp domain.SearchResponse) string {
			recorded = resp
			return "offer-visible"
		},
		MachineEnv: func(context.Context) domain.EnvironmentFingerprint {
			return domain.EnvironmentFingerprint{SchemaVersion: 1}
		},
	}}
	raw, _ := json.Marshal(searchArgs{Query: "deploy immutable commit", Packages: []string{"pkg:golang/example.com/deploy@v1.0.0"}})
	got := s.toolSearch(context.Background(), raw)
	if len(recorded.Results) != 1 || recorded.Results[0].SampleID != "visible" {
		t.Fatalf("recorded wrong response: %#v", recorded.Results)
	}
	structured, ok := got.StructuredContent.(localSearchStructured)
	if !ok || structured.OfferID != "offer-visible" {
		t.Fatalf("wrong offer: %#v", got.StructuredContent)
	}
}

func TestToolSearchRecordsMissWhenEverythingSuppressed(t *testing.T) {
	var recorded domain.SearchResponse
	s := &Server{Deps: &Deps{
		SearchRaw: func(context.Context, domain.SearchRequest) domain.SearchResponse {
			return domain.SearchResponse{Results: []domain.SearchResult{{SampleID: "hidden", Grade: domain.GradeCompatible, Confidence: "LOW", Case: &domain.Case{Goal: "format integers", Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"}}}}}
		},
		RecordSearchOutcome: func(_ context.Context, _ domain.SearchRequest, resp domain.SearchResponse) string {
			recorded = resp
			return ""
		},
		MachineEnv: func(context.Context) domain.EnvironmentFingerprint {
			return domain.EnvironmentFingerprint{SchemaVersion: 1}
		},
	}}
	raw, _ := json.Marshal(searchArgs{Query: "deploy immutable commit"})
	_ = s.toolSearch(context.Background(), raw)
	if !recorded.Miss || len(recorded.Results) != 0 {
		t.Fatalf("suppressed result recorded as hit: %#v", recorded)
	}
}

func TestRenderSearchResponseExplainsEachSurvivor(t *testing.T) {
	resp := domain.SearchResponse{Results: []domain.SearchResult{{Grade: domain.GradeCompatible, Confidence: "LOW"}, {Grade: domain.GradeReferenceOnly, Confidence: "LOW"}}}
	text := renderSearchResponseWithRelevance(resp, []string{"Relevance: first.", "Relevance: second."})
	alt := strings.Index(text, "--- alternative 2 ---")
	if alt < 0 || !strings.Contains(text[:alt], "Relevance: first.") || !strings.Contains(text[alt:], "Relevance: second.") {
		t.Fatalf("per-result relevance missing:\n%s", text)
	}
}
