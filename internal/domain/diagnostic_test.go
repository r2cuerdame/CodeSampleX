package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticDependencyUnknownAndProvenNoneRemainDifferent(t *testing.T) {
	unknown := NewDiagnosticTrace(SearchRequest{SchemaVersion: 2})
	unknown.Evidence.DependencyState = DependencyUnknown
	provenNone := NewDiagnosticTrace(SearchRequest{SchemaVersion: 2})
	provenNone.Evidence.DependencyState = DependencyProvenNone

	var a, b bytes.Buffer
	RenderDiagnosticText(&a, unknown)
	RenderDiagnosticText(&b, provenNone)
	if !strings.Contains(a.String(), "dependency_state: unknown") {
		t.Fatalf("unknown dependency state disappeared: %s", a.String())
	}
	if !strings.Contains(b.String(), "dependency_state: proven-no-dependencies") {
		t.Fatalf("proven-none dependency state disappeared: %s", b.String())
	}
}

func TestDiagnosticCoordinateRejectsPathsAndKeepsPublicCoordinates(t *testing.T) {
	d := NewDiagnosticTrace(SearchRequest{
		SchemaVersion: 2,
		Packages: []string{
			"pkg:golang/github.com/jackc/pgx/v5@v5.10.0",
			`C:\Users\alice\private`,
		},
		Symbols: []string{"pgx.Conn.Exec", "tokio::spawn", `C:\Users\alice\token.txt`, "/home/alice/private.go"},
	})
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "github.com/jackc/pgx/v5") {
		t.Fatalf("public coordinate missing: %s", text)
	}
	if !strings.Contains(text, "tokio::spawn") {
		t.Fatalf("public Rust symbol missing: %s", text)
	}
	for _, forbidden := range []string{"alice", "private", "token.txt"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("diagnostic leaked %q: %s", forbidden, text)
		}
	}
}

func TestOutputGateDiagnosticUsesStableSuppressionReason(t *testing.T) {
	req := SearchRequest{SchemaVersion: 2, Debug: true, Query: "deploy immutable main SHA"}
	resp := SearchResponse{Results: []SearchResult{{
		SampleID: "sha256:unrelated", Grade: GradeAdaptationRequired, Score: .41,
		Case: &Case{Goal: "upload multipart form with axios", Packages: []string{"pkg:npm/axios@1.12.0"}},
	}}}
	resp, suppressed := GateNormalOutput(req, resp, nil)
	RecordOutputGateDiagnostic(&resp, req, 1, suppressed)
	if !resp.Miss || resp.Diagnostic == nil || resp.Diagnostic.Decision != string(GradeNoSafeMatch) {
		t.Fatalf("diagnosed gate = %+v", resp)
	}
	if len(resp.Diagnostic.Candidates) != 1 || len(resp.Diagnostic.Candidates[0].ReasonCodes) != 1 ||
		resp.Diagnostic.Candidates[0].ReasonCodes[0] != SuppressedInsufficientGoalOverlap {
		t.Fatalf("suppression reason missing: %+v", resp.Diagnostic.Candidates)
	}
}

func TestOutputGateDiagnosticExplainsSelectedCandidate(t *testing.T) {
	req := SearchRequest{SchemaVersion: 2, Debug: true, Query: "axios multipart upload"}
	resp := SearchResponse{Results: []SearchResult{{
		SampleID: "sha256:axios", Grade: GradeCompatible,
		Case: &Case{Goal: "upload multipart form with axios", Packages: []string{"pkg:npm/axios@1.12.0"}},
	}}, Diagnostic: NewDiagnosticTrace(req)}
	// The engine owns the bounded candidate list. Seed its selected entry to
	// verify that the output boundary enriches rather than invents identities.
	resp.Diagnostic.Candidates = []DiagnosticCandidate{{SampleID: "sha256:axios", Outcome: "selected"}}
	RecordOutputGateDiagnostic(&resp, req, 1, nil)
	if len(resp.Diagnostic.Candidates[0].RelevanceSignals) == 0 ||
		resp.Diagnostic.Candidates[0].RelevanceSignals[0] != RelevanceNamedSubject {
		t.Fatalf("selected candidate relevance missing: %+v", resp.Diagnostic.Candidates)
	}
}
