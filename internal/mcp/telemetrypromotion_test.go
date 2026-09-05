package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// grpcInterceptorHit is the sample this repo's own `go test ./...` came back
// with, for a deliberately failing test that asserts an integer.
//
// Nothing about it is wrong on its own: the contract ran, and it is a Go
// sample answering a Go command. What it has never been is evidence about an
// assertion on an int — and it arrived as REUSE_VERIFIED / MATCH: COMPATIBLE,
// justified by "your question names its package or symbol". The caller never
// named gRPC. Our own "toolchain=go/test" header did, through the "test"
// segment of google.golang.org/grpc/test/bufconn.
func grpcInterceptorHit() domain.SearchResponse {
	return domain.SearchResponse{Results: []domain.SearchResult{{
		Grade:      domain.GradeCompatible,
		Confidence: "HIGH",
		SampleID:   "sha256:2f1c0a44d1e37b90",
		Case: &domain.Case{
			Goal:     "Add a field in a unary client interceptor without dropping metadata the caller already attached",
			Believed: "A unary client interceptor can call metadata.NewOutgoingContext without affecting metadata the caller attached",
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
		Evidence: domain.EvidenceSummary{ContractPasses: 1, IndependentCrossPeers: 1},
	}}}
}

// goTestAssertionFailure is the sanitized shape run_observed_command really
// produces for a failing Go assertion. TestARealGoTestFailureDoesNotPromote
// AnUnrelatedSample below drives the producer itself, so this stays honest.
func goTestAssertionFailure() []string {
	return []string{
		"failureEvent: stage=PROJECT_TEST toolchain=go/test outer=go test evidence=test-runner-diagnostic gap=",
		"fingerprint: sha256:c55ed370383e87ae18527cde8e16e235ab504ace82a323d43ac01b40c5146ef3",
		"termination: exit:1",
		"evidenceQuality: complete",
		"--- FAIL: TestDeliberateFailure (0.00s) · proven_test.go:<n>: proven = 1, want 0 · FAIL example.com/gatefixture 0.150s",
	}
}

// The dogfood case from #88, end to end.
//
// The failure is the answer here, and a same-ecosystem sample about something
// else must not be rendered on top of it as a decision.
func TestAGoAssertionFailureIsNotAnsweredWithAnUnrelatedGoSample(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TEST", "FAIL", goTestAssertionFailure(),
				commandOutput{Stdout: "--- FAIL: TestDeliberateFailure (0.00s)\n    proven_test.go:8: proven = 1, want 0\nFAIL"}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			return grpcInterceptorHit(), "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "go", "test", "./..."))
	m := structured(t, out)
	recommendation, _ := m["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != "NO_RELEVANT_MATCH" {
		t.Errorf("status = %v, want NO_RELEVANT_MATCH: %s", got, mustJSON(t, recommendation))
	}
	if got := recommendation["advisoryOnly"]; got != true {
		t.Errorf("advisoryOnly = %v, want true: %s", got, mustJSON(t, recommendation))
	}
	if got := recommendation["suppressedReason"]; got != domain.SuppressedInsufficientGoalOverlap {
		t.Errorf("suppressedReason = %v, want %q", got, domain.SuppressedInsufficientGoalOverlap)
	}
	body, _ := recommendation["text"].(string)
	for _, forbidden := range []string{
		"REUSE_VERIFIED", "MATCH: COMPATIBLE",
		"your question names its package or symbol",
		"unary client interceptor",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("an unrelated sample is still presented as %q:\n%s", forbidden, body)
		}
	}
	if answer, _ := m["networkAnswer"].(string); strings.Contains(answer, "REUSE_VERIFIED") {
		t.Errorf("networkAnswer still promotes the unrelated sample:\n%s", answer)
	}
	// The local failure is untouched and first.
	if stdout, _ := m["stdout"].(string); !strings.Contains(stdout, "proven = 1, want 0") {
		t.Errorf("the local failure was not preserved verbatim: %q", stdout)
	}
	// Demoted is not deleted: an agent that wants to look still can.
	if !strings.Contains(body, "sha256:2f1c0a44d1e37b90") {
		t.Errorf("the candidate was erased rather than demoted:\n%s", body)
	}
}

// The keys stripped from the caller's question are written here, by the
// producer, and read there, by domain.SearchRequest.CallerQuestion. Renaming
// one on this side without the other silently reopens the promotion, so this
// test compiles a real module, fails a real `go test`, and asks the real gate.
func TestARealGoTestFailureDoesNotPromoteAnUnrelatedSample(t *testing.T) {
	project := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/gatefixture\n\ngo 1.26.5\n",
		"proven_test.go": "package gatefixture\n\nimport \"testing\"\n\n" +
			"func TestDeliberateFailure(t *testing.T) {\n\tproven := 1\n\tif proven != 0 {\n" +
			"\t\tt.Fatalf(\"proven = %d, want 0\", proven)\n\t}\n}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(project, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	argv := []string{"go", "test", "./..."}
	_, _, result, sanitized, _, err := runObserved(
		context.Background(), nil, nil, cfg, nil, nil, argv, project)
	if err != nil {
		t.Fatalf("runObserved: %v", err)
	}
	if result != "FAIL" || len(sanitized) == 0 {
		t.Fatalf("the fixture did not fail as expected: result=%s sanitized=%v", result, sanitized)
	}

	// Exactly what lookupAfterFailure asks.
	query, errorCode := failureQuestion(sanitized)
	req := domain.SearchRequest{SchemaVersion: 2, Query: query, ErrorCode: errorCode}
	req.Environment.Ecosystem = domain.CommandEcosystem(argv)
	req.EnvironmentProvenance = domain.SearchProvenanceContext

	question := req.CallerQuestion()
	if strings.Contains(question, "toolchain=") || strings.Contains(question, "evidenceQuality:") {
		t.Fatalf("a coordinate key drifted and is being read as the caller's words: %q", question)
	}
	if !strings.Contains(question, "want 0") {
		t.Fatalf("the failure text was stripped along with the coordinates: %q", question)
	}

	top := grpcInterceptorHit().Results[0]
	if signals := top.RelevanceSignals(req, argv); len(signals) != 0 {
		t.Errorf("a real Go test failure linked an unrelated gRPC sample: %v", signals)
	}
	if reason := top.SuppressionReason(req, argv); reason != domain.SuppressedInsufficientGoalOverlap {
		t.Errorf("SuppressionReason = %q, want %q", reason, domain.SuppressedInsufficientGoalOverlap)
	}
}
