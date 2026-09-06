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
			Coordinate: coord,
			Provenance: ProvenanceField,
			Result:     ResultPass,
			ObservedAt: "2026-08-10T10:00:00Z",
			Count:      5,
		},
		{
			Coordinate: coord,
			Provenance: ProvenanceFarm,
			Result:     ResultPass,
			ObservedAt: "2026-08-12T10:00:00Z",
			Count:      1,
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
			Coordinate: coordV1,
			Provenance: ProvenanceField,
			Result:     ResultPass,
			ObservedAt: "2024-05-01T00:00:00Z", // old observation from 2024
			Count:      10,
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
			Coordinate: coord,
			Provenance: ProvenanceField,
			Result:     ResultPass,
			Count:      12,
			ObservedAt: "2026-09-01T10:00:00Z",
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

func TestSensitiveFlagNameRedaction(t *testing.T) {
	env := EnvironmentFingerprint{OS: "linux", Arch: "amd64"}

	tests := []struct {
		name     string
		argv     []string
		wantArgs string
	}{
		{
			name:     "password with equals",
			argv:     []string{"mycli", "--password=hunter2", "--user", "alice"},
			wantArgs: "--password=<redacted-secret> --user alice",
		},
		{
			name:     "api key with space",
			argv:     []string{"mycli", "--api-key", "abc12345secret", "--port", "8080"},
			wantArgs: "--api-key <redacted-secret> --port 8080",
		},
		{
			name:     "bare token assignment",
			argv:     []string{"mycli", "TOKEN=plainvalue", "--flag"},
			wantArgs: "TOKEN=<redacted-secret> --flag",
		},
		{
			name:     "env flag with token assignment",
			argv:     []string{"mycli", "-e", "TOKEN=plainvalue"},
			wantArgs: "-e TOKEN=<redacted-secret>",
		},
		{
			name:     "secret-key with equals",
			argv:     []string{"mycli", "--secret-key=supersecret"},
			wantArgs: "--secret-key=<redacted-secret>",
		},
		{
			name:     "auth-token with space",
			argv:     []string{"mycli", "--auth-token", "some-raw-token"},
			wantArgs: "--auth-token <redacted-secret>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coord := ParseCLICommand(tt.argv, env)
			if coord.ArgsPattern != tt.wantArgs {
				t.Errorf("ArgsPattern = %q, want %q", coord.ArgsPattern, tt.wantArgs)
			}
		})
	}
}

func TestExactCommandCoordinateRecall(t *testing.T) {
	target := CLIExperienceCoordinate{
		Tool:        "docker",
		ToolVersion: "27.1.0",
		Subcommand:  "compose up",
		Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
	}

	exitCode := 1
	obs := []CLIExperienceObservation{
		// Target command: PASS
		{
			Coordinate: target,
			Provenance: ProvenanceField,
			Result:     ResultPass,
			ObservedAt: "2026-09-01T10:00:00Z",
			Count:      5,
		},
		// Unrelated subcommand: compose down FAIL
		{
			Coordinate: CLIExperienceCoordinate{
				Tool:        "docker",
				ToolVersion: "27.1.0",
				Subcommand:  "compose down",
				Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
			},
			Provenance:   ProvenanceField,
			Result:       ResultFail,
			Termination:  FailureTermination{Kind: TerminationExit, ExitCode: &exitCode},
			ErrorCode:    "ECONNREFUSED",
			ErrorSummary: "connection refused",
			ObservedAt:   "2026-09-01T11:00:00Z",
			Count:        1,
		},
		// Unrelated subcommand: image build FAIL
		{
			Coordinate: CLIExperienceCoordinate{
				Tool:        "docker",
				ToolVersion: "27.1.0",
				Subcommand:  "image build",
				Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
			},
			Provenance:   ProvenanceField,
			Result:       ResultFail,
			Termination:  FailureTermination{Kind: TerminationExit, ExitCode: &exitCode},
			ErrorCode:    "EBUILD",
			ErrorSummary: "build failed",
			ObservedAt:   "2026-09-01T12:00:00Z",
			Count:        1,
		},
		// Unrelated argument pattern: compose up -d FAIL
		{
			Coordinate: CLIExperienceCoordinate{
				Tool:        "docker",
				ToolVersion: "27.1.0",
				Subcommand:  "compose up",
				ArgsPattern: "-d",
				Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
			},
			Provenance:   ProvenanceField,
			Result:       ResultFail,
			Termination:  FailureTermination{Kind: TerminationExit, ExitCode: &exitCode},
			ErrorCode:    "EADDRINUSE",
			ErrorSummary: "port 8080 already allocated",
			ObservedAt:   "2026-09-01T13:00:00Z",
			Count:        1,
		},
	}

	summary := BuildExperienceSummary(target, obs)

	// Summary for "docker compose up" must exclude unrelated subcommands and argument patterns
	if summary.FieldPassCount != 5 {
		t.Errorf("FieldPassCount = %d, want 5", summary.FieldPassCount)
	}
	if summary.FieldFailCount != 0 {
		t.Errorf("FieldFailCount = %d, want 0 (unrelated failures must be excluded)", summary.FieldFailCount)
	}
	if summary.Status != "OBSERVED_PASS" {
		t.Errorf("Status = %q, want OBSERVED_PASS (unrelated failures must not cause COEXISTING_BOUNDARY)", summary.Status)
	}
	if len(summary.RecentFailures) != 0 {
		t.Errorf("RecentFailures count = %d, want 0", len(summary.RecentFailures))
	}
}

func TestDeterministicSameVersionOutcomeAggregation(t *testing.T) {
	coordV1 := CLIExperienceCoordinate{
		Tool:        "tool",
		ToolVersion: "1.0.0",
		Subcommand:  "run",
		Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
	}
	coordV2 := CLIExperienceCoordinate{
		Tool:        "tool",
		ToolVersion: "2.0.0",
		Subcommand:  "run",
		Environment: EnvironmentFingerprint{OS: "linux", Arch: "amd64"},
	}

	failCode := 1
	passObs := CLIExperienceObservation{
		Coordinate: coordV1,
		Provenance: ProvenanceField,
		Result:     ResultPass,
		ObservedAt: "2026-09-01T10:00:00Z",
		Count:      1,
	}
	failObs := CLIExperienceObservation{
		Coordinate:   coordV1,
		Provenance:   ProvenanceField,
		Result:       ResultFail,
		Termination:  FailureTermination{Kind: TerminationExit, ExitCode: &failCode},
		ErrorCode:    "EFAIL",
		ErrorSummary: "v1 failed",
		ObservedAt:   "2026-09-01T11:00:00Z",
		Count:        1,
	}
	v2PassObs := CLIExperienceObservation{
		Coordinate: coordV2,
		Provenance: ProvenanceField,
		Result:     ResultPass,
		ObservedAt: "2026-09-02T10:00:00Z",
		Count:      1,
	}

	// Mixed outcomes at v1.0.0 (both PASS and FAIL) followed by v2.0.0 PASS.
	// Order 1: PASS then FAIL
	corpus1 := []CLIExperienceObservation{passObs, failObs, v2PassObs}
	b1 := DetectExperienceBoundaries(corpus1)
	if len(b1) != 0 {
		t.Errorf("Order 1: expected 0 boundaries for mixed v1, got %d: %+v", len(b1), b1)
	}

	// Order 2: FAIL then PASS
	corpus2 := []CLIExperienceObservation{failObs, passObs, v2PassObs}
	b2 := DetectExperienceBoundaries(corpus2)
	if len(b2) != 0 {
		t.Errorf("Order 2: expected 0 boundaries for mixed v1, got %d: %+v", len(b2), b2)
	}

	// Pure FAIL at v1.0.0 followed by pure PASS at v2.0.0
	pureCorpus := []CLIExperienceObservation{failObs, v2PassObs}
	pureBoundaries := DetectExperienceBoundaries(pureCorpus)
	if len(pureBoundaries) != 1 {
		t.Fatalf("Pure transition: expected 1 boundary, got %d", len(pureBoundaries))
	}
	if pureBoundaries[0].FromVerdict != ResultFail || pureBoundaries[0].ToVerdict != ResultPass {
		t.Errorf("Unexpected transition: %s -> %s", pureBoundaries[0].FromVerdict, pureBoundaries[0].ToVerdict)
	}
}
