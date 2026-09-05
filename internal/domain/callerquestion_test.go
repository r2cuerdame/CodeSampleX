package domain

import (
	"strings"
	"testing"

	searchrelevance "github.com/r2cuerdame/codesamplex/internal/search/relevance"
)

// goTestFailureHeader is what run_observed_command writes about its own run,
// verbatim. It is identical on every Go test failure anywhere.
//
// TestARealGoTestFailureDoesNotPromoteAnUnrelatedSample in internal/mcp drives
// the real producer, so this literal cannot drift away from it in silence.
const goTestFailureHeader = "failureEvent: stage=PROJECT_TEST toolchain=go/test outer=go test evidence=test-runner-diagnostic gap=\n" +
	"termination: exit:1\n" +
	"evidenceQuality: complete"

// grpcInterceptorCase is the sample a `go test` failure about an integer was
// answered with, reduced to the parts the gate reads. The bufconn package is
// the whole mechanism: its import path carries the segment "test".
func grpcInterceptorCase() SearchResult {
	return SearchResult{
		Grade:      GradeCompatible,
		Confidence: "HIGH",
		SampleID:   "sha256:2f1c0a44d1e37b90",
		Case: &Case{
			Goal: "Add a field in a unary client interceptor without dropping metadata the caller already attached",
			Packages: []string{
				"pkg:golang/google.golang.org/grpc@1.68.0",
				"pkg:golang/google.golang.org/grpc/test/bufconn@1.68.0",
			},
			Symbols: []string{
				"google.golang.org/grpc/metadata.NewOutgoingContext",
				"google.golang.org/grpc/test/bufconn.Listen",
			},
			Contract: []string{"the outgoing metadata carries both the caller's field and the interceptor's"},
		},
	}
}

// A query nobody generated is nobody's business but the caller's.
func TestCallerQuestionLeavesAnOrdinaryQuestionAlone(t *testing.T) {
	for _, query := range []string{
		"src/index.ts(12,5): error TS2352: Conversion of type 'string' to type 'number'",
		"how do I round currency half to even",
		"",
	} {
		if got := (SearchRequest{Query: query}).CallerQuestion(); got != query {
			t.Errorf("CallerQuestion(%q) = %q, want it untouched", query, got)
		}
	}
}

func TestCallerQuestionDropsOurOwnFailureCoordinates(t *testing.T) {
	spoken := "--- FAIL: TestDeliberateFailure · proven = 1, want 0"
	got := (SearchRequest{Query: goTestFailureHeader + "\n" + spoken}).CallerQuestion()
	if got != spoken {
		t.Fatalf("CallerQuestion = %q, want %q", got, spoken)
	}
}

// A command that printed no diagnosable words asked no question, and a
// candidate promoted by what is left has been promoted by our own telemetry.
func TestAFailureThatPrintedNothingButCoordinatesAsksNothing(t *testing.T) {
	if got := (SearchRequest{Query: goTestFailureHeader}).CallerQuestion(); got != "" {
		t.Fatalf("CallerQuestion = %q, want empty", got)
	}
	if got := (SearchRequest{Query: "errorCode: TS2352"}).CallerQuestion(); got != "" {
		t.Fatalf("single-line coordinate survived as a question: %q", got)
	}
}

// The dogfood case from #88, at the seam where it was decided.
//
// A `go test` failure whose only words were about an integer promoted a gRPC
// interceptor sample to REUSE_VERIFIED, justified as "your question names its
// package or symbol". Nothing the caller said named gRPC. The word "test" in
// our own toolchain= field met the "test" segment in
// google.golang.org/grpc/test/bufconn, and that was the entire link.
func TestOurOwnFailureHeaderCannotPromoteACandidate(t *testing.T) {
	argv := []string{"go", "test", "./..."}
	top := grpcInterceptorCase()
	req := SearchRequest{
		SchemaVersion: 2,
		Query: goTestFailureHeader + "\n" +
			"--- FAIL: TestDeliberateFailure (0.00s) · proven_test.go:<n>: proven = 1, want 0 · FAIL example.com/gatefixture 0.150s",
	}

	// The caller's own words survived, so what follows is a judgement about
	// a real question rather than about an empty one.
	if question := req.CallerQuestion(); !strings.Contains(question, "want 0") {
		t.Fatalf("the failure text was stripped along with the coordinates: %q", question)
	}
	if signals := top.RelevanceSignals(req, argv); len(signals) != 0 {
		t.Errorf("an unrelated gRPC sample was linked to a plain assertion failure: %v", signals)
	}
	if line := top.RelevanceLine(req, argv); line != "" {
		t.Errorf("a justification was printed for a candidate with no link: %q", line)
	}
	if reason := top.SuppressionReason(req, argv); reason != SuppressedInsufficientGoalOverlap {
		t.Errorf("SuppressionReason = %q, want %q", reason, SuppressedInsufficientGoalOverlap)
	}
	resp, suppressed := GateNormalOutput(req, SearchResponse{Results: []SearchResult{top}}, argv)
	if !resp.Miss || len(resp.Results) != 0 {
		t.Errorf("the candidate still reached normal output: %+v", resp.Results)
	}
	if len(suppressed) != 1 || suppressed[0].SampleID != top.SampleID {
		t.Fatalf("the demotion was not observable: %+v", suppressed)
	}
	if len(suppressed[0].Signals) != 0 {
		t.Errorf("the suppressed record still claims a link: %v", suppressed[0].Signals)
	}
}

// Both halves of the fix are load-bearing, and each is pinned where it lives.
// Neither the coordinate lines nor the "test" path segment may name a subject
// on its own.
func TestNeitherHalfOfTheHeaderLinkSurvivesAlone(t *testing.T) {
	top := grpcInterceptorCase()
	names := []string{"google.golang.org/grpc", "google.golang.org/grpc/test/bufconn"}

	if searchrelevance.NamesSubject(goTestFailureHeader, names, top.Case.Symbols) {
		t.Error("our own coordinate header still names the sample's subject")
	}
	if searchrelevance.NamesSubject("this test asserts an integer", names, top.Case.Symbols) {
		t.Error("the word \"test\" still names google.golang.org/grpc/test/bufconn")
	}
	// And a caller who really did name it is still heard.
	if !searchrelevance.NamesSubject("bufconn.Listen never accepts", names, top.Case.Symbols) {
		t.Error("naming the package by its own word no longer promotes it")
	}
}

// PROJECT_TEST and PROJECT_COMPILE have the exact shape of a structured error
// code, so our header could share a "diagnostic" with any sample that
// mentions one.
func TestOurOwnStageNamesAreNotTheCallersErrorCode(t *testing.T) {
	req := SearchRequest{Query: goTestFailureHeader}
	if codes := requestDiagnostics(req.CallerQuestion(), req.ErrorCode); len(codes) != 0 {
		t.Fatalf("our own stage names were read as the caller's error codes: %v", codes)
	}
	// The caller's declared code is still taken at its word.
	req = SearchRequest{Query: goTestFailureHeader, ErrorCode: "TS2352"}
	if codes := requestDiagnostics(req.CallerQuestion(), req.ErrorCode); len(codes) != 1 || codes[0] != "TS2352" {
		t.Fatalf("the declared error code was lost: %v", codes)
	}
}
