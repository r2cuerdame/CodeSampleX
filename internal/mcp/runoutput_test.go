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
	b, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
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
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_COMPILE", "FAIL", []string{"ERR_MODULE_NOT_FOUND: cannot find <path>"}, commandOutput{}, nil
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
	recommendation, _ := m["recommendation"].(map[string]any)
	if got := recommendation["classification"]; got != "VERIFIED_MATCH" {
		t.Errorf("classification = %v, want VERIFIED_MATCH: %s", got, mustJSON(t, m))
	}
}

// The wrapped command is the primary evidence. A recommendation is a separate
// passenger on that result, and neither MCP text rendering nor JSON field order
// may put it in front of the failure that caused the lookup.
func TestTheLocalFailurePrecedesTheNetworkAnswer(t *testing.T) {
	var asked domain.SearchRequest
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TEST", "FAIL", []string{"ParserError"},
				commandOutput{Stdout: "dispatcher setup started", Stderr: "Test-Dispatcher.ps1:12: Unexpected token '${escaped}'"}, nil
		}
		d.Search = func(_ context.Context, req domain.SearchRequest) (domain.SearchResponse, string) {
			asked = req
			return domain.SearchResponse{Results: []domain.SearchResult{{
				Grade: domain.GradeReferenceOnly, Confidence: "LOW", SampleID: "sha256:dart",
				Case:     &domain.Case{Packages: []string{"pkg:pub/shelf@1.4.2"}},
				Evidence: domain.EvidenceSummary{ContractPasses: 1},
			}}}, "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "pwsh", "-File", "Test-Dispatcher.ps1"))
	if asked.Environment.Ecosystem != "generic" || asked.Environment.ExecutionContext != "powershell" {
		t.Errorf("PowerShell failure lookup lost tool context: %#v", asked.Environment)
	}
	rawStructured := mustJSON(t, out.StructuredContent)
	exitAt := strings.Index(rawStructured, `"exitCode"`)
	stdoutAt := strings.Index(rawStructured, `"stdout"`)
	stderrAt := strings.Index(rawStructured, `"stderr"`)
	recommendationFieldAt := strings.Index(rawStructured, `"recommendation"`)
	if !(exitAt >= 0 && exitAt < stdoutAt && stdoutAt < stderrAt && stderrAt < recommendationFieldAt) {
		t.Errorf("structured fields do not preserve primary-before-secondary order: %s", rawStructured)
	}
	text := resultText(out)
	failureAt := strings.Index(text, "Unexpected token")
	recommendationAt := strings.Index(text, "sha256:dart")
	if failureAt < 0 || recommendationAt < 0 || failureAt > recommendationAt {
		t.Fatalf("local failure must precede recommendation:\n%s", text)
	}

	m := structured(t, out)
	if got, _ := m["stderr"].(string); !strings.Contains(got, "Unexpected token") {
		t.Errorf("structured stderr lost the parser error: %s", mustJSON(t, m))
	}
	if _, ok := m["stdout"]; !ok {
		t.Errorf("structured stdout is absent: %s", mustJSON(t, m))
	}
	if got, _ := m["stdout"].(string); !strings.Contains(got, "setup started") {
		t.Errorf("structured stdout lost the command output: %s", mustJSON(t, m))
	}
	recommendation, _ := m["recommendation"].(map[string]any)
	if got := recommendation["classification"]; got != "REFERENCE_CANDIDATE" {
		t.Errorf("classification = %v, want REFERENCE_CANDIDATE: %s", got, mustJSON(t, m))
	}
	if got := recommendation["advisoryOnly"]; got != true {
		t.Errorf("advisoryOnly = %v, want true: %s", got, mustJSON(t, m))
	}
}

func TestLookupTimeoutStillReturnsTheLocalFailure(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TEST", "FAIL", []string{"ERR_TIMEOUT_CASE"},
				commandOutput{Stderr: "the test itself failed before lookup"}, nil
		}
		d.Search = func(ctx context.Context, _ domain.SearchRequest) (domain.SearchResponse, string) {
			<-ctx.Done()
			return domain.SearchResponse{Miss: true}, ""
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := s.toolRunObserved(ctx, runArgsJSON(t, "go", "test", "./..."))
	m := structured(t, out)
	if got, _ := m["stderr"].(string); !strings.Contains(got, "test itself failed") {
		t.Errorf("timeout hid the local failure: %s", mustJSON(t, m))
	}
	recommendation, _ := m["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != "UNAVAILABLE" {
		t.Errorf("recommendation status = %v, want UNAVAILABLE: %s", got, mustJSON(t, m))
	}
}

func TestNoSafeMatchStillReturnsTheLocalFailure(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 2, "PROJECT_COMPILE", "FAIL", []string{"TS2304"}, commandOutput{Stderr: "src/index.ts:9: cannot find name Widget"}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			return domain.SearchResponse{Miss: true}, ""
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "build"))
	m := structured(t, out)
	if got, _ := m["stderr"].(string); !strings.Contains(got, "cannot find name Widget") {
		t.Errorf("NO_SAFE_MATCH hid the local failure: %s", mustJSON(t, m))
	}
	recommendation, _ := m["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != "NO_SAFE_MATCH" {
		t.Errorf("recommendation status = %v, want NO_SAFE_MATCH: %s", got, mustJSON(t, m))
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
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_COMPILE", "FAIL",
				[]string{"errorCode: ERR", "stat <path> directory not found"},
				commandOutput{Stderr: "internal/web/pivot.go:944:12: undefined: partz"}, nil
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
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 0, "PROJECT_TEST", "PASS", nil, commandOutput{Stdout: strings.Repeat("ok  github.com/x/y\n", 400)}, nil
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
