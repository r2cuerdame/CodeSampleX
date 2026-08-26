package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The hook shares the MCP path's relevance gate, and shares it because two
// definitions of "this is the wrong language" drift.
//
// This hook arrives unasked, in the middle of somebody's work. Interrupting a
// failing `npm run typecheck` to show a Dart contract for package:crypto,
// under a "MATCH: COMPATIBLE" header, is worse than saying nothing: silence
// costs a reader nothing, and an answer in the wrong language costs them the
// time it takes to work out that it is.
func TestHookStaysQuietWhenTheOnlyAnswerIsAnotherEcosystem(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "npm run typecheck", "src/index.ts(12,5): error TS2352"),
		func(e *hookEnv) {
			e.inspect = func(context.Context, string, [][]string) hookProject {
				return hookProject{Known: true, Stage: domain.StageProjectTypecheck,
					Argv: []string{"npm", "run", "typecheck"}}
			}
			e.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
				return domain.SearchResponse{Results: []domain.SearchResult{{
					Grade: domain.GradeCompatible, Confidence: "LOW", SampleID: "sha256:dart",
					Case:     &domain.Case{Packages: []string{"pkg:pub/crypto@3.0.6"}},
					Evidence: domain.EvidenceSummary{ContractPasses: 1},
				}}}, nil
			}
		})

	if code := hookAgentMain(context.Background(), env); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if ctx := hookContext(t, out); ctx != "" {
		t.Errorf("the hook interrupted an npm build with a Dart sample:\n%s", ctx)
	}
}

// The fixture the issue opened with: a PowerShell parser error answered with
// a Dart sample. PowerShell builds for no package ecosystem this network
// knows, so a sample pinned to one has nothing to say about it — and the
// answer being labelled REFERENCE_CANDIDATE did not stop it being read.
func TestHookStaysQuietWhenAPowerShellFailureDrawsADartSample(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "pwsh -File Test-Dispatcher.ps1", "ParserError"),
		func(e *hookEnv) {
			e.inspect = func(context.Context, string, [][]string) hookProject {
				return hookProject{Known: true, Stage: domain.StageProjectTest,
					Argv: []string{"pwsh", "-File", "Test-Dispatcher.ps1"}}
			}
			e.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
				return domain.SearchResponse{Results: []domain.SearchResult{{
					Grade: domain.GradeReferenceOnly, Confidence: "LOW", SampleID: "sha256:dart",
					Case:     &domain.Case{Packages: []string{"pkg:pub/shelf@1.4.2"}},
					Evidence: domain.EvidenceSummary{ContractPasses: 1},
				}}}, nil
			}
		})

	hookAgentMain(context.Background(), env)
	if ctx := hookContext(t, out); ctx != "" {
		t.Errorf("a PowerShell parser error was answered with a Dart sample:\n%s", ctx)
	}
}

// The exemption is the same one the MCP path uses: a sample that matched this
// exact sanitized failure fingerprint has an evidence link to the failure in
// hand, and that outranks the ecosystem it happens to be written in.
func TestHookStillAnswersAnExactFailureMatchFromAnotherEcosystem(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "npm run typecheck", "error TS2352"),
		func(e *hookEnv) {
			e.inspect = func(context.Context, string, [][]string) hookProject {
				return hookProject{Known: true, Stage: domain.StageProjectTypecheck,
					Argv: []string{"npm", "run", "typecheck"}}
			}
			e.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
				return domain.SearchResponse{Results: []domain.SearchResult{{
					Grade: domain.GradeCompatible, Confidence: "LOW", SampleID: "sha256:linked",
					ExactFailureMatched: true,
					Case:                &domain.Case{Packages: []string{"pkg:pub/crypto@3.0.6"}},
					Evidence:            domain.EvidenceSummary{ContractPasses: 1},
				}}}, nil
			}
		})

	hookAgentMain(context.Background(), env)
	if ctx := hookContext(t, out); !strings.Contains(ctx, "sha256:linked") {
		t.Errorf("a sample that matched this failure's own fingerprint was suppressed:\n%s", ctx)
	}
}

// A sample in the command's own ecosystem, about the error the command
// printed, is the case the hook exists for.
//
// The fixture used to be an npm-labelled sample with no goal and no contract
// — same ecosystem and about nothing. That passed while the gate only asked
// "is this the wrong language". It does not pass a gate that asks whether a
// concrete link can be NAMED, and it should not: the relationship this test
// is really about is that a LOW-confidence, genuinely related candidate is
// still offered, and the fixture now actually holds it.
func TestHookAnswersASameEcosystemSample(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "npm run typecheck", "error TS2352"),
		func(e *hookEnv) {
			e.inspect = func(context.Context, string, [][]string) hookProject {
				return hookProject{Known: true, Stage: domain.StageProjectTypecheck,
					Argv: []string{"npm", "run", "typecheck"}}
			}
			e.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
				return domain.SearchResponse{Results: []domain.SearchResult{{
					Grade: domain.GradeCompatible, Confidence: "LOW", SampleID: "sha256:ts",
					Case: &domain.Case{
						Goal:     "Convert between unrelated types without tripping the compiler",
						Packages: []string{"pkg:npm/typescript@5.9.2"},
						Contract: []string{"A direct cast between unrelated types raises TS2352 unless it goes through unknown"},
					},
					Evidence: domain.EvidenceSummary{ContractPasses: 1},
				}}}, nil
			}
		})

	hookAgentMain(context.Background(), env)
	ctx := hookContext(t, out)
	if !strings.Contains(ctx, "sha256:ts") {
		t.Errorf("a same-ecosystem sample was suppressed:\n%s", ctx)
	}
	if !strings.Contains(ctx, domain.RecommendationReferenceCandidate) {
		t.Errorf("a LOW-confidence answer lost its reference-candidate label:\n%s", ctx)
	}
	// And it says WHY it interrupted, in one mechanically generated line.
	if !strings.Contains(ctx, "Relevance: ") {
		t.Errorf("the hook interrupted without saying what made this sample relevant:\n%s", ctx)
	}
}

// The case this issue is about, on the surface where it costs the most: same
// language, same runtime, same arch, and nothing else. A Go build breaking in
// a deploy script drew a Go number-formatting sample, and the hook is where
// an unasked answer is hardest to ignore.
func TestHookStaysQuietWhenTheOnlyAnswerSharesOnlyTheEnvironment(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "go build ./...", "undefined: workflowDispatch"),
		func(e *hookEnv) {
			e.inspect = func(context.Context, string, [][]string) hookProject {
				return hookProject{Known: true, Stage: domain.StageProjectCompile,
					Argv: []string{"go", "build", "./..."}}
			}
			e.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
				return domain.SearchResponse{Results: []domain.SearchResult{{
					Grade: domain.GradeCompatible, Confidence: "LOW", SampleID: "sha256:humanize",
					Case: &domain.Case{
						Goal:     "Format integers and floats with thousand separators",
						Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"},
						Symbols:  []string{"humanize.FormatInteger", "humanize.FormatFloat"},
					},
					Exact:    []string{"ecosystem golang", "go", "linux alpine"},
					Evidence: domain.EvidenceSummary{ContractPasses: 1},
				}}}, nil
			}
		})

	hookAgentMain(context.Background(), env)
	if ctx := hookContext(t, out); ctx != "" {
		t.Errorf("a Go build was interrupted with an unrelated Go sample:\n%s", ctx)
	}
}

// Silence is this hook's normal answer, which makes it indistinguishable
// from a broken hook — so the reason has to be readable where a reader can
// ask for it. The code is the stable one the diagnostic mode will report.
func TestTheSuppressionReasonIsVisibleInTheHookTrace(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "go build ./...", "undefined: workflowDispatch"),
		func(e *hookEnv) {
			e.inspect = func(context.Context, string, [][]string) hookProject {
				return hookProject{Known: true, Stage: domain.StageProjectCompile,
					Argv: []string{"go", "build", "./..."}}
			}
			e.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
				return domain.SearchResponse{Results: []domain.SearchResult{{
					Grade: domain.GradeCompatible, Confidence: "LOW", SampleID: "sha256:humanize",
					Case: &domain.Case{
						Goal:     "Format integers and floats with thousand separators",
						Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"},
					},
					Evidence: domain.EvidenceSummary{ContractPasses: 1},
				}}}, nil
			}
		})
	trace := &bytes.Buffer{}
	env.debug = trace

	hookAgentMain(context.Background(), env)
	if hookContext(t, out) != "" {
		t.Fatal("the candidate reached normal output")
	}
	if code := hookTraceCode(trace.String()); code != domain.SuppressedInsufficientGoalOverlap {
		t.Errorf("trace code = %q, want %q:\n%s", code,
			domain.SuppressedInsufficientGoalOverlap, trace.String())
	}
	if !strings.Contains(trace.String(), "sha256:humanize") {
		t.Errorf("the trace never says which candidate was held back:\n%s", trace.String())
	}
}
