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

// failureLookupRecorder wires the failure auto-lookup the way NewDeps does:
// retrieval and recording as separate phases, with Search left to fail the
// test if the lookup goes back to recording before the gate.
func failureLookupRecorder(t *testing.T, retrieved domain.SearchResponse, recorded *[]domain.SearchResponse) *Server {
	t.Helper()
	return lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TYPECHECK", "FAIL", []string{"errorCode: TS2352"},
				commandOutput{Stdout: "src/index.ts(12,5): error TS2352"}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			t.Error("the failure lookup recorded through Search before gating")
			return retrieved, "offer-unfiltered"
		}
		d.SearchRaw = func(context.Context, domain.SearchRequest) domain.SearchResponse {
			return retrieved
		}
		d.RecordSearchOutcome = func(_ context.Context, _ domain.SearchRequest, resp domain.SearchResponse) string {
			*recorded = append(*recorded, resp)
			return "offer-visible"
		}
	})
}

func TestFailureLookupRecordsMissWhenEverythingSuppressed(t *testing.T) {
	var recorded []domain.SearchResponse
	s := failureLookupRecorder(t, dartCryptoHit(), &recorded)
	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	recommendation, _ := structured(t, out)["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != domain.RecommendationNoRelevantMatch {
		t.Fatalf("status = %v, want NO_RELEVANT_MATCH: %s", got, mustJSON(t, recommendation))
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded %d outcomes, want exactly 1", len(recorded))
	}
	if !recorded[0].Miss || len(recorded[0].Results) != 0 {
		t.Fatalf("a suppressed candidate was recorded as a hit: %#v", recorded[0])
	}
}

func TestFailureLookupRecordsOnlyTheRenderedCandidate(t *testing.T) {
	first := typescriptHit().Results[0]
	first.SampleID = "sha256:relevantsecond0000"
	second := typescriptHit().Results[0]
	second.SampleID = "sha256:relevantthird00000"
	retrieved := dartCryptoHit()
	retrieved.Results = append(retrieved.Results, first, second)

	var recorded []domain.SearchResponse
	s := failureLookupRecorder(t, retrieved, &recorded)
	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	recommendation, _ := structured(t, out)["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != "FOUND" {
		t.Fatalf("status = %v, want FOUND: %s", got, mustJSON(t, recommendation))
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded %d outcomes, want exactly 1", len(recorded))
	}
	if recorded[0].Miss || len(recorded[0].Results) != 1 || recorded[0].Results[0].SampleID != first.SampleID {
		t.Fatalf("recorded %#v, want only the rendered %s", recorded[0].Results, first.SampleID)
	}
}

func TestFailureLookupRecordsAnEmptyRetrievalAsAMiss(t *testing.T) {
	var recorded []domain.SearchResponse
	s := failureLookupRecorder(t, domain.SearchResponse{SchemaVersion: 2, Miss: true}, &recorded)
	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	recommendation, _ := structured(t, out)["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != "NO_SAFE_MATCH" {
		t.Fatalf("status = %v, want NO_SAFE_MATCH: %s", got, mustJSON(t, recommendation))
	}
	if len(recorded) != 1 || !recorded[0].Miss {
		t.Fatalf("an empty retrieval was not recorded as a miss: %#v", recorded)
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
