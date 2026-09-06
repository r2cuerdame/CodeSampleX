package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestSearchKnownSolutionSurfacesCLIExperienceWithoutMemoryLanguage(t *testing.T) {
	coord := domain.CLIExperienceCoordinate{
		Tool:        "docker",
		ToolVersion: "27.1.0",
		Subcommand:  "compose up",
		ArgsPattern: "-d",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1,
			OS:            "linux",
			Arch:          "amd64",
		},
	}

	exit0 := 0
	exit1 := 1

	expSummary := domain.BuildExperienceSummary(coord, []domain.CLIExperienceObservation{
		{
			Coordinate:  coord,
			Provenance:  domain.ProvenanceField,
			Result:      domain.ResultPass,
			Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit0},
			ObservedAt:  "2026-09-01T10:00:00Z",
			Count:       8,
		},
		{
			Coordinate:   coord,
			Provenance:   domain.ProvenanceField,
			Result:       domain.ResultFail,
			Termination:  domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit1},
			ErrorCode:    "EADDRINUSE",
			ErrorSummary: "port 8080 already bound",
			ObservedAt:   "2026-09-02T10:00:00Z",
			Count:        1,
		},
		{
			Coordinate:  coord,
			Provenance:  domain.ProvenanceFarm,
			Result:      domain.ResultPass,
			Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit0},
			ObservedAt:  "2026-09-03T10:00:00Z",
			Count:       2,
		},
	})

	deps := emptyDeps()
	deps.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
		return domain.SearchResponse{
			SchemaVersion: 2,
			Miss:          true,
			CLIExperience: &expSummary,
		}, ""
	}
	c := startServer(t, deps)

	res := callTool(t, c, "search_known_solution", map[string]any{
		"query": "docker compose up -d",
		"environment": map[string]any{
			"os": "linux", "arch": "amd64",
		},
	})

	text := toolText(t, res)

	if strings.Contains(strings.ToLower(text), "memory") {
		t.Errorf("MCP output must strictly use 'experience', NEVER 'memory'. Got text:\n%s", text)
	}

	for _, expected := range []string{
		"CLI EXECUTION EXPERIENCE: docker compose up -d",
		"Status: COEXISTING_BOUNDARY",
		"Field (8 PASS, 1 FAIL)",
		"Farm (2 PASS, 0 FAIL)",
		"EADDRINUSE",
		"port 8080 already bound",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("MCP output missing expected section %q:\n%s", expected, text)
		}
	}

	structured, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("Missing structuredContent in response: %v", res)
	}
	if structured["cliExperience"] == nil {
		t.Errorf("structuredContent missing cliExperience payload: %v", structured)
	}
}
