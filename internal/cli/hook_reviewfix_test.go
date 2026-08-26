package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestHookFiltersBeforeSelectingOneAnswer(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "go test ./...", "ENOENT"), func(e *hookEnv) {
		e.inspect = func(context.Context, string, [][]string) hookProject {
			return hookProject{
				Known: true,
				Stage: domain.StageProjectTest,
				Argv:  []string{"go", "test", "./..."},
			}
		}
		e.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
			return domain.SearchResponse{Results: []domain.SearchResult{
				{
					SampleID:   "sha256:hidden",
					Grade:      domain.GradeCompatible,
					Confidence: "LOW",
					Case: &domain.Case{
						Goal:     "format integers",
						Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"},
					},
				},
				{
					SampleID:   "sha256:visible",
					Grade:      domain.GradeReferenceOnly,
					Confidence: "LOW",
					Case: &domain.Case{
						Goal:     "handle missing files",
						Contract: []string{"os.Open reports ENOENT when the path is absent"},
					},
				},
			}}, nil
		}
	})

	if code := hookAgentMain(context.Background(), env); code != 0 {
		t.Fatalf("hook exit=%d", code)
	}
	if !strings.Contains(out.String(), "sha256:visible") {
		t.Fatalf("relevant lower-ranked candidate was lost: %s", out.String())
	}
	if strings.Contains(out.String(), "sha256:hidden") {
		t.Fatalf("unrelated top candidate leaked into hook output: %s", out.String())
	}
}
