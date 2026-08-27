package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestToolLevelDebugUsesOneCanonicalTraceForTextAndStructuredOutput(t *testing.T) {
	deps := emptyDeps()
	deps.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
		return domain.SearchResponse{SchemaVersion: 2, Miss: true}, ""
	}
	c := startServer(t, deps)
	res := callTool(t, c, "search_known_solution", map[string]any{
		"query": "no known sample", "debug": true,
	})
	structured, _ := res["structuredContent"].(map[string]any)
	response, _ := structured["response"].(map[string]any)
	diagnostic, _ := response["diagnostic"].(map[string]any)
	if diagnostic == nil {
		diagnostic, _ = structured["diagnostic"].(map[string]any)
	}
	requestID, _ := diagnostic["requestId"].(string)
	if requestID == "" {
		t.Fatalf("structured diagnostic missing: %s", mustJSON(t, structured))
	}
	if !strings.Contains(toolText(t, res), "request_id: "+requestID) {
		t.Fatalf("human output did not render the canonical trace: %s", toolText(t, res))
	}

	normal := callTool(t, c, "search_known_solution", map[string]any{"query": "no known sample"})
	raw := mustJSON(t, normal["structuredContent"])
	if strings.Contains(raw, "diagnostic") || strings.Contains(toolText(t, normal), "DEBUG DIAGNOSTIC") {
		t.Fatalf("debug OFF exposed internal trace: %s / %s", raw, toolText(t, normal))
	}
}

func TestFailureDebugExposesNestedTypeScriptCompileLineageWithoutRawSecrets(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_COMPILE", "FAIL", []string{
				"failureEvent: stage=PROJECT_COMPILE toolchain=typescript/tsc outer=go test evidence=compiler-diagnostic gap=",
				"errorCode: TS2352",
				"fingerprint: ff_public",
				"termination: exit:1",
				"evidenceQuality: complete",
				"<path>: token=super-secret",
			}, commandOutput{Stderr: `C:\Users\alice\private\index.ts(1,1): error TS2352`}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			return domain.SearchResponse{Miss: true}, ""
		}
	})
	out := s.toolRunObserved(context.Background(), runArgsJSONWithDebug(t, true, "go", "test", "./..."))
	m := structured(t, out)
	diagnostic, _ := m["diagnostic"].(map[string]any)
	raw, _ := json.Marshal(diagnostic)
	trace := string(raw)
	for _, want := range []string{"typescript/tsc", "PROJECT_COMPILE", "TS2352", "ff_public", "go test"} {
		if !strings.Contains(trace, want) {
			t.Fatalf("failure lineage missing %q: %s", want, trace)
		}
	}
	for _, forbidden := range []string{"alice", "super-secret", "private\\index"} {
		if strings.Contains(trace, forbidden) {
			t.Fatalf("diagnostic leaked %q: %s", forbidden, trace)
		}
	}
	requestID, _ := diagnostic["requestId"].(string)
	if !strings.Contains(resultText(out), "request_id: "+requestID) {
		t.Fatalf("human failure diagnostic drifted from structured trace: %s", resultText(out))
	}
}

func TestFailureDebugLeavesGoAssertionAtTestExecution(t *testing.T) {
	f := parseFailureDiagnostic([]string{"go", "test", "./..."}, 1, "PROJECT_TEST", []string{
		"failureEvent: stage=PROJECT_TEST toolchain=go/test outer=go test evidence=test-runner-diagnostic gap=",
		"termination: exit:1", "evidenceQuality: complete",
	})
	if len(f.Events) != 1 || f.Events[0].ActualStage != "PROJECT_TEST" || f.OuterStage != "PROJECT_TEST" {
		t.Fatalf("assertion lineage = %+v", f)
	}
}

func TestFailureDebugCreatesDIAGGapWithoutGuessingACause(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "UNKNOWN", "FAIL", nil, commandOutput{}, nil
		}
	})
	out := s.toolRunObserved(context.Background(), runArgsJSONWithDebug(t, true, "go", "test", "./..."))
	m := structured(t, out)
	raw := mustJSON(t, m["diagnostic"])
	if !strings.Contains(raw, `"code":"DIAG"`) || !strings.Contains(raw, "STAGE_LINEAGE_UNAVAILABLE") || strings.Contains(strings.ToLower(raw), "root cause") {
		t.Fatalf("missing diagnostic did not remain an explicit DIAG gap: %s", raw)
	}
}

func runArgsJSONWithDebug(t *testing.T, debug bool, command ...string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(runArgs{Command: command, Debug: debug})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
