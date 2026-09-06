package domain

import (
	"strings"
	"testing"
)

func TestParseCLICommandNormalizesMultiWordSubcommandsAndSanitizesSecrets(t *testing.T) {
	env := EnvironmentFingerprint{
		SchemaVersion: 1,
		OS:            "linux",
		Arch:          "amd64",
	}

	tests := []struct {
		name            string
		argv            []string
		wantTool        string
		wantSubcommand  string
		wantArgsPattern string
	}{
		{
			name:            "docker compose up with secret and file",
			argv:            []string{"docker.exe", "compose", "up", "-d", "--file", "./docker-compose.yml", "-e", "TOKEN=ghp_secretToken12345"},
			wantTool:        "docker",
			wantSubcommand:  "compose up",
			wantArgsPattern: "-d --file <path> -e TOKEN=<redacted-secret>",
		},
		{
			name:            "gh workflow run with branch",
			argv:            []string{"gh", "workflow", "run", "deploy.yml", "--ref", "main"},
			wantTool:        "gh",
			wantSubcommand:  "workflow run",
			wantArgsPattern: "<path> --ref main",
		},
		{
			name:            "git worktree add with path and branch",
			argv:            []string{"git.cmd", "worktree", "add", "C:\\temp\\worktree-1", "feature/my-branch"},
			wantTool:        "git",
			wantSubcommand:  "worktree add",
			wantArgsPattern: "<path> feature/my-branch",
		},
		{
			name:            "npm run build",
			argv:            []string{"npm", "run", "build", "--verbose"},
			wantTool:        "npm",
			wantSubcommand:  "run build",
			wantArgsPattern: "--verbose",
		},
		{
			name:            "go test with path and flags",
			argv:            []string{"go", "test", "./internal/domain/...", "-v", "-race"},
			wantTool:        "go",
			wantSubcommand:  "test",
			wantArgsPattern: "<path> -v -race",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coord := ParseCLICommand(tt.argv, env)
			if coord.Tool != tt.wantTool {
				t.Errorf("Tool = %q, want %q", coord.Tool, tt.wantTool)
			}
			if coord.Subcommand != tt.wantSubcommand {
				t.Errorf("Subcommand = %q, want %q", coord.Subcommand, tt.wantSubcommand)
			}
			if coord.ArgsPattern != tt.wantArgsPattern {
				t.Errorf("ArgsPattern = %q, want %q", coord.ArgsPattern, tt.wantArgsPattern)
			}
		})
	}
}

func TestCLIExperienceCoordinateDeterministicIDAndPURL(t *testing.T) {
	coord1 := CLIExperienceCoordinate{
		Tool:        "docker.EXE",
		ToolVersion: "27.1.0",
		Subcommand:  "compose  up",
		ArgsPattern: "-d   --build",
		Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
	}

	coord2 := CLIExperienceCoordinate{
		Tool:        "docker",
		ToolVersion: "27.1.0",
		Subcommand:  "compose up",
		ArgsPattern: "-d --build",
		Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
	}

	if coord1.CoordinateID() != coord2.CoordinateID() {
		t.Errorf("CoordinateID not deterministic:\ncoord1: %s\ncoord2: %s",
			coord1.CoordinateID(), coord2.CoordinateID())
	}

	purl, ok := coord1.PURL()
	if !ok {
		t.Fatalf("PURL expected valid, got false")
	}
	if purl.String() != "pkg:generic/cli/docker@27.1.0" {
		t.Errorf("PURL = %q, want pkg:generic/cli/docker@27.1.0", purl.String())
	}
}

func TestForensicCoexistencePASSAndFAILNeverEraseEachOther(t *testing.T) {
	coord := CLIExperienceCoordinate{
		Tool:        "docker",
		ToolVersion: "27.1.0",
		Subcommand:  "compose up",
		ArgsPattern: "-d",
		Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
	}

	failCode := 1
	var obs []CLIExperienceObservation

	// 100 passing field observations
	for i := 0; i < 100; i++ {
		obs = append(obs, CLIExperienceObservation{
			Coordinate:  coord,
			Provenance:  ProvenanceField,
			Result:      ResultPass,
			Termination: FailureTermination{Kind: TerminationExit, ExitCode: intPtr(0)},
			ObservedAt:  "2026-08-01T12:00:00Z",
			Count:       1,
		})
	}

	// 1 verified failure observation
	obs = append(obs, CLIExperienceObservation{
		Coordinate:        coord,
		Provenance:        ProvenanceField,
		Result:            ResultFail,
		Termination:       FailureTermination{Kind: TerminationExit, ExitCode: &failCode},
		ErrorCode:         "EADDRINUSE",
		ErrorSummary:      "bind: address already in use 0.0.0.0:8080",
		ErrorFingerprint:  "fp-addr-in-use",
		ObservedAt:        "2026-08-15T10:00:00Z",
		Count:             1,
		IsHighInformation: true,
	})

	// Compressed observations must retain the single failure
	compressed := CompressExperienceObservations(obs)
	if len(compressed) != 2 {
		t.Fatalf("Compressed observations len = %d, want 2 (1 compressed PASS + 1 FAIL)", len(compressed))
	}

	summary := BuildExperienceSummary(coord, compressed)
	if summary.Status != "COEXISTING_BOUNDARY" {
		t.Errorf("Status = %q, want COEXISTING_BOUNDARY (failure must coexist with passes)", summary.Status)
	}
	if summary.FieldPassCount != 100 {
		t.Errorf("FieldPassCount = %d, want 100", summary.FieldPassCount)
	}
	if summary.FieldFailCount != 1 {
		t.Errorf("FieldFailCount = %d, want 1", summary.FieldFailCount)
	}
	if len(summary.RecentFailures) != 1 {
		t.Fatalf("RecentFailures len = %d, want 1", len(summary.RecentFailures))
	}
	if summary.RecentFailures[0].ErrorCode != "EADDRINUSE" {
		t.Errorf("RecentFailure ErrorCode = %q, want EADDRINUSE", summary.RecentFailures[0].ErrorCode)
	}
}

func TestFieldEvidencePrimaryAndFarmSupportingDistinction(t *testing.T) {
	coord := CLIExperienceCoordinate{
		Tool:        "gh",
		ToolVersion: "2.50.0",
		Subcommand:  "workflow run",
		Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
	}

	obs := []CLIExperienceObservation{
		{
			Coordinate:  coord,
			Provenance:  ProvenanceField,
			Result:      ResultPass,
			ObservedAt:  "2026-08-10T10:00:00Z",
			Count:       5,
		},
		{
			Coordinate:  coord,
			Provenance:  ProvenanceFarm,
			Result:      ResultPass,
			ObservedAt:  "2026-08-12T10:00:00Z",
			Count:       1,
		},
	}

	compressed := CompressExperienceObservations(obs)
	if len(compressed) != 2 {
		t.Fatalf("Field and Farm observations must remain separate; got %d groups", len(compressed))
	}

	summary := BuildExperienceSummary(coord, compressed)
	if summary.FieldPassCount != 5 || summary.FarmPassCount != 1 {
		t.Errorf("Counts mismatch: FieldPass=%d (want 5), FarmPass=%d (want 1)",
			summary.FieldPassCount, summary.FarmPassCount)
	}
}

func TestTemporalEvidencePolicyPreservesOldObservationsAndDetectsBoundaries(t *testing.T) {
	coordV1 := CLIExperienceCoordinate{
		Tool:        "git",
		ToolVersion: "2.40.0",
		Subcommand:  "worktree add",
		Environment: EnvironmentFingerprint{OS: "windows", Arch: "amd64"},
	}
	coordV2 := CLIExperienceCoordinate{
		Tool:        "git",
		ToolVersion: "2.46.0",
		Subcommand:  "worktree add",
		Environment: EnvironmentFingerprint{OS: "windows", Arch: "amd64"},
	}

	failCode := 128
	obs := []CLIExperienceObservation{
		{
			Coordinate:  coordV1,
			Provenance:  ProvenanceField,
			Result:      ResultPass,
			ObservedAt:  "2024-05-01T00:00:00Z", // old observation from 2024
			Count:       10,
		},
		{
			Coordinate:   coordV2,
			Provenance:   ProvenanceFarm,
			Result:       ResultFail,
			Termination:  FailureTermination{Kind: TerminationExit, ExitCode: &failCode},
			ErrorCode:    "FATAL_CONFIG",
			ErrorSummary: "fatal: not a valid repository",
			ObservedAt:   "2026-09-01T00:00:00Z",
			Count:        1,
		},
	}

	boundaries := DetectExperienceBoundaries(obs)
	if len(boundaries) != 1 {
		t.Fatalf("Expected 1 boundary detected across versions, got %d", len(boundaries))
	}

	b := boundaries[0]
	if b.Axis != "version" || b.TransitionFrom != "2.40.0" || b.TransitionTo != "2.46.0" {
		t.Errorf("Unexpected boundary: %+v", b)
	}
	if b.FromVerdict != ResultPass || b.ToVerdict != ResultFail {
		t.Errorf("Verdict transition mismatch: %s -> %s", b.FromVerdict, b.ToVerdict)
	}

	// Querying coordV2 returns summary with boundary and both history records preserved
	summary := BuildExperienceSummary(coordV2, obs)
	if len(summary.Boundaries) != 1 {
		t.Errorf("Summary should include boundary; got %d", len(summary.Boundaries))
	}
	if summary.FarmFailCount != 1 || summary.FieldPassCount != 10 {
		t.Errorf("History counts lost: FarmFail=%d, FieldPass=%d", summary.FarmFailCount, summary.FieldPassCount)
	}
}

func TestTextSummaryUsesExperienceAndNeverMemory(t *testing.T) {
	coord := CLIExperienceCoordinate{
		Tool:        "docker",
		ToolVersion: "27.1.0",
		Subcommand:  "compose up",
		ArgsPattern: "-d",
		Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
	}

	exitCode := 1
	obs := []CLIExperienceObservation{
		{
			Coordinate:  coord,
			Provenance:  ProvenanceField,
			Result:      ResultPass,
			Count:       12,
			ObservedAt:  "2026-09-01T10:00:00Z",
		},
		{
			Coordinate:   coord,
			Provenance:   ProvenanceField,
			Result:       ResultFail,
			Termination:  FailureTermination{Kind: TerminationExit, ExitCode: &exitCode},
			ErrorCode:    "EADDRINUSE",
			ErrorSummary: "port 8080 already bound",
			ObservedAt:   "2026-09-02T10:00:00Z",
			Count:        1,
		},
	}

	summary := BuildExperienceSummary(coord, obs)
	text := summary.TextSummary()

	if strings.Contains(strings.ToLower(text), "memory") {
		t.Errorf("User-facing text must use 'experience', NEVER 'memory'. Got text:\n%s", text)
	}
	if !strings.Contains(text, "CLI EXECUTION EXPERIENCE") {
		t.Errorf("Expected 'CLI EXECUTION EXPERIENCE' header, got:\n%s", text)
	}
	if !strings.Contains(text, "Verified Failure Observations") {
		t.Errorf("Expected Verified Failure Observations section, got:\n%s", text)
	}
}

func intPtr(i int) *int {
	return &i
}
