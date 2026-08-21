package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func structured(t *testing.T, out *toolResult) map[string]any {
	t.Helper()
	m, ok := out.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content: %#v", out.StructuredContent)
	}
	return m
}

// The client renders structuredContent and drops the text block.
//
// Everything this tool says to an agent was written into a strings.Builder —
// the exit code, the sanitized error, and the whole answer the network found
// for the failure. Wrapping a failing build returned exactly five JSON keys
// and none of that prose, so the auto-lookup shipped last release has been
// answering into a channel nobody reads.
//
// Whatever the agent must act on belongs in the structured payload.
func TestTheNetworkAnswerReachesTheAgent(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, string, error) {
			return 1, "PROJECT_COMPILE", "FAIL", []string{"ERR_MODULE_NOT_FOUND: cannot find <path>"}, "", nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			return domain.SearchResponse{Results: []domain.SearchResult{{
				Grade: domain.GradeExact, SampleID: "sha256:aaa",
				Evidence: domain.EvidenceSummary{ContractPasses: 2},
			}}}, "offer-1"
		}
	})

	m := structured(t, s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "build")))
	answer, _ := m["networkAnswer"].(string)
	if !strings.Contains(answer, "sha256:aaa") {
		t.Errorf("the sample never reached the agent; structured = %s", mustJSON(t, m))
	}
}

// A sanitized template is what may LEAVE the machine. It is not what the agent
// needs to fix the build.
//
// Wrapping a broken compile returned "stat <path> directory not found" — a
// fingerprint and a redacted template, with the compiler's own words dropped.
// An agent that cannot see why the build broke runs the build again outside
// this tool, which is the one behaviour the product exists to prevent, and the
// redaction bought nothing: the response goes to an agent on this machine that
// already holds the source and the paths.
func TestAFailedBuildShowsTheAgentWhatBroke(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, string, error) {
			return 1, "PROJECT_COMPILE", "FAIL",
				[]string{"errorCode: ERR", "stat <path> directory not found"},
				"internal/web/pivot.go:944:12: undefined: partz", nil
		}
	})

	m := structured(t, s.toolRunObserved(context.Background(), runArgsJSON(t, "go", "build", "./...")))
	out, _ := m["output"].(string)
	if !strings.Contains(out, "undefined: partz") {
		t.Errorf("the compiler's own words never reached the agent; structured = %s", mustJSON(t, m))
	}
}

// A command that passed has nothing to explain, and its log is the largest
// thing this tool could send. Silence is the cheaper and truer answer.
func TestAPassingBuildSendsNoLog(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, string, error) {
			return 0, "PROJECT_TEST", "PASS", nil, strings.Repeat("ok  github.com/x/y\n", 400), nil
		}
	})

	m := structured(t, s.toolRunObserved(context.Background(), runArgsJSON(t, "go", "test", "./...")))
	if _, ok := m["output"]; ok {
		t.Errorf("a passing build sent its log: %s", mustJSON(t, m))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
