package evidence

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func TestAnalyzeFailureUsesGoCompilerDiagnosticInsteadOfOuterTestIntent(t *testing.T) {
	analysis := AnalyzeFailure(
		scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"},
		[]string{"go", "test", "./internal/sanitizer"},
		CommandOutput{Stdout: "# example.test\ninternal/sanitizer/x.go:12:5: undefined: missing\nFAIL\texample.test [build failed]\n"},
	)

	if analysis.OuterCommand != "go test" || analysis.OuterStage != domain.StageProjectTest {
		t.Fatalf("outer lineage = %q / %q", analysis.OuterCommand, analysis.OuterStage)
	}
	if len(analysis.Events) != 1 {
		t.Fatalf("events = %#v", analysis.Events)
	}
	event := analysis.Events[0]
	if event.Stage != domain.StageProjectCompile || event.Toolchain != "go/compiler" {
		t.Fatalf("actual failure = %q / %q, want PROJECT_COMPILE / go/compiler", event.Stage, event.Toolchain)
	}
	if event.Diagnostic == "" || event.StageEvidence != StageEvidenceCompilerDiagnostic {
		t.Fatalf("compiler evidence not preserved: %#v", event)
	}
}

func TestAnalyzeFailureSeparatesGoTestExecutionAndResolve(t *testing.T) {
	profile := scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"}

	t.Run("test assertion", func(t *testing.T) {
		analysis := AnalyzeFailure(profile, []string{"go", "test", "./..."}, CommandOutput{Stdout: "--- FAIL: TestThing (0.00s)\n    thing_test.go:22: got 1, want 2\nFAIL\n"})
		if len(analysis.Events) != 1 || analysis.Events[0].Stage != domain.StageProjectTest || analysis.Events[0].Toolchain != "go/test" {
			t.Fatalf("events = %#v", analysis.Events)
		}
	})

	t.Run("module resolve", func(t *testing.T) {
		analysis := AnalyzeFailure(profile, []string{"go", "test", "./..."}, CommandOutput{Stderr: "no required module provides package example.invalid/missing; to add it:\n\tgo get example.invalid/missing\n"})
		if len(analysis.Events) != 1 || analysis.Events[0].Stage != domain.StageProjectResolve || analysis.Events[0].Toolchain != "go/module" {
			t.Fatalf("events = %#v", analysis.Events)
		}
	})
}

func TestAnalyzeFailureBuildAggregateCreatesDiagnosticGap(t *testing.T) {
	analysis := AnalyzeFailure(
		scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"},
		[]string{"go", "test", "./..."},
		CommandOutput{Stdout: "FAIL\texample.test [build failed]\n"},
	)

	if len(analysis.Events) != 1 {
		t.Fatalf("events = %#v", analysis.Events)
	}
	event := analysis.Events[0]
	if event.Stage != domain.StageProjectCompile || event.Diagnostic != "" || event.EvidenceGap != EvidenceGapDiagnosticMissing {
		t.Fatalf("aggregate build evidence = %#v", event)
	}
}

func TestAnalyzeFailureFindsNestedTypeScriptCompilerAcrossOuterWrappers(t *testing.T) {
	fixtures := []struct {
		name    string
		profile scanner.CommandProfile
		argv    []string
	}{
		{"go test", scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"}, []string{"go", "test", "./internal/mcp"}},
		{"npm test", scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "npm"}, []string{"npm", "test"}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			analysis := AnalyzeFailure(fixture.profile, fixture.argv, CommandOutput{Stdout: "src/index.ts(12,5): error TS2352: Conversion of type 'string' to type 'number' may be a mistake.\n"})
			if len(analysis.Events) != 1 {
				t.Fatalf("events = %#v", analysis.Events)
			}
			event := analysis.Events[0]
			if event.Stage != domain.StageProjectCompile || event.Toolchain != "typescript/tsc" || event.DiagnosticCode != "TS2352" {
				t.Fatalf("nested compile event = %#v", event)
			}
		})
	}
}

func TestAnalyzeFailureSplitsNestedCompileAndTestFailure(t *testing.T) {
	analysis := AnalyzeFailure(
		scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"},
		[]string{"go", "test", "./internal/mcp"},
		CommandOutput{Stdout: "src/index.ts(12,5): error TS2352: bad conversion\n--- FAIL: TestMCP (0.01s)\n    mcp_test.go:20: got false, want true\nFAIL\n"},
	)

	if len(analysis.Events) != 2 {
		t.Fatalf("events = %#v", analysis.Events)
	}
	if analysis.Events[0].Stage != domain.StageProjectCompile || analysis.Events[1].Stage != domain.StageProjectTest {
		t.Fatalf("event stages = %q, %q", analysis.Events[0].Stage, analysis.Events[1].Stage)
	}
	if analysis.Events[0].Diagnostic == analysis.Events[1].Diagnostic {
		t.Fatalf("independent diagnostics were merged: %#v", analysis.Events)
	}
}

func TestAnalyzeFailureKeepsAssertionDetailAfterAnotherEventReallocatesTheSlice(t *testing.T) {
	analysis := AnalyzeFailure(
		scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"},
		[]string{"go", "test", "./internal/mcp"},
		CommandOutput{Stdout: "--- FAIL: TestFirst (0.01s)\nsrc/index.ts(12,5): error TS2352: bad conversion\n--- FAIL: TestSecond (0.01s)\n    mcp_test.go:20: got false, want true\nFAIL\n"},
	)

	if len(analysis.Events) != 2 {
		t.Fatalf("events = %#v", analysis.Events)
	}
	if got := analysis.Events[1].Diagnostic; !strings.Contains(got, "mcp_test.go") {
		t.Fatalf("test assertion detail was lost or attached to another event: %q", got)
	}
}

func TestAnalyzeFailureProcessStartIsNotOuterTest(t *testing.T) {
	analysis := AnalyzeFailure(
		scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"},
		[]string{"missing-go", "test"},
		CommandOutput{Termination: domain.FailureTermination{Kind: domain.TerminationProcessStartFailed}},
	)
	if len(analysis.Events) != 1 || analysis.Events[0].Stage != domain.StageProcessStart || analysis.Events[0].StageEvidence != StageEvidenceStructuredTermination {
		t.Fatalf("events = %#v", analysis.Events)
	}
}

func TestAnalyzeFailureDoesNotTurnGenericRuntimeDiagnosticsIntoTests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile scanner.CommandProfile
		argv    []string
		output  CommandOutput
	}{
		{
			name:    "go run panic",
			profile: scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: "go"},
			argv:    []string{"go", "run", "."},
			output:  CommandOutput{Stderr: "panic: production startup failed\n"},
		},
		{
			name:    "node process assertion",
			profile: scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: "node"},
			argv:    []string{"node", "app.js"},
			output:  CommandOutput{Stderr: "AssertionError: request invariant failed\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analysis := AnalyzeFailure(tc.profile, tc.argv, tc.output)
			if len(analysis.Events) != 1 {
				t.Fatalf("events = %#v", analysis.Events)
			}
			if analysis.Events[0].Stage == domain.StageProjectTest || analysis.Events[0].StageEvidence == StageEvidenceTestRunnerDiagnostic {
				t.Fatalf("generic runtime diagnostic became test evidence: %#v", analysis.Events[0])
			}
		})
	}
}

func TestAnalyzeFailureAllowsGenericDiagnosticsAfterTestRunnerIsEstablished(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tool   string
		argv   []string
		output string
	}{
		{"go test panic", "go", []string{"go", "test", "./..."}, "panic: test setup failed\n"},
		{"npm test assertion", "npm", []string{"npm", "test"}, "AssertionError: expected true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analysis := AnalyzeFailure(
				scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: tc.tool},
				tc.argv, CommandOutput{Stderr: tc.output})
			if len(analysis.Events) != 1 || analysis.Events[0].Stage != domain.StageProjectTest ||
				analysis.Events[0].StageEvidence != StageEvidenceTestRunnerDiagnostic {
				t.Fatalf("established test diagnostic = %#v", analysis.Events)
			}
		})
	}
}
