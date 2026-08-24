package cli

import (
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

// A sample in the command's own ecosystem is the case the hook exists for.
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
					Case:     &domain.Case{Packages: []string{"pkg:npm/typescript@5.9.2"}},
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
}
