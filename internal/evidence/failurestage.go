package evidence

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// StageEvidence names the concrete evidence used to override command intent.
// It is intentionally a small public vocabulary rather than raw parser prose.
type StageEvidence = domain.FailureStageEvidence

const (
	StageEvidenceStructuredTermination  = domain.FailureStageStructuredTermination
	StageEvidenceResolveDiagnostic      = domain.FailureStageResolveDiagnostic
	StageEvidenceCompilerDiagnostic     = domain.FailureStageCompilerDiagnostic
	StageEvidenceTestRunnerDiagnostic   = domain.FailureStageTestRunnerDiagnostic
	StageEvidenceBuildAggregate         = domain.FailureStageBuildAggregate
	StageEvidenceUnclassifiedDiagnostic = domain.FailureStageUnclassifiedDiagnostic
)

// EvidenceGap explains which part of a failure event could not be established.
type EvidenceGap = domain.FailureEvidenceGap

const (
	EvidenceGapDiagnosticMissing = domain.FailureDiagnosticMissing
	EvidenceGapStageUnknown      = domain.FailureStageUnknown
)

// FailureEvent is one independently evidenced failure inside an outer command.
// Diagnostic remains local until the sanitizer converts it to public evidence.
type FailureEvent struct {
	Stage          domain.Stage
	Toolchain      string
	DiagnosticCode string
	Diagnostic     string
	StageEvidence  StageEvidence
	EvidenceGap    EvidenceGap
}

// FailureAnalysis separates the user's outer workflow intent from each actual
// failing stage. OuterCommand is privacy-bounded to a known public tool plus a
// known subcommand; project paths and arbitrary script names are never copied.
type FailureAnalysis struct {
	OuterCommand string
	OuterStage   domain.Stage
	Events       []FailureEvent
}

var (
	typeScriptDiagnosticRE = regexp.MustCompile(`(?i)(?:^|[\\/])[^\s:()]+\.tsx?(?::\d+:\d+|\(\d+,\d+\)).*\berror\s+(TS\d+)\b`)
	rustDiagnosticRE       = regexp.MustCompile(`^error\[(E\d+)\]:`)
	goLocationRE           = regexp.MustCompile(`(?i)\.go:\d+(?::\d+)?:`)
	goCompileMessageRE     = regexp.MustCompile(`(?i)(undefined:|syntax error:|cannot use |declared and not used|imported and not used|assignment mismatch|not enough arguments|too many arguments|invalid operation|redeclared in this block|build constraints exclude all Go files)`)
	goTestStartRE          = regexp.MustCompile(`^--- FAIL: `)
	jsTestDiagnosticRE     = regexp.MustCompile(`(?i)(AssertionError|expect\(|expected:|received:|tests? failed|^FAIL\s+[^\t].*)`)
)

type detectedFailure struct {
	index int
	FailureEvent
}

// AnalyzeFailure classifies actual failing stages from structured termination
// and bounded stdout/stderr. CommandProfile is retained only as outer intent
// and as a conservative tool hint after a real test-runner marker is found.
func AnalyzeFailure(profile scanner.CommandProfile, argv []string, output CommandOutput) FailureAnalysis {
	analysis := FailureAnalysis{OuterCommand: safeOuterCommand(argv), OuterStage: profile.Stage}
	if output.Termination.Kind == domain.TerminationProcessStartFailed {
		analysis.Events = []FailureEvent{{
			Stage: domain.StageProcessStart, Toolchain: outerTool(profile, argv),
			StageEvidence: StageEvidenceStructuredTermination,
			EvidenceGap:   EvidenceGapDiagnosticMissing,
		}}
		return analysis
	}

	text := strings.TrimSpace(strings.TrimSpace(output.Stderr) + "\n" + strings.TrimSpace(output.Stdout))
	lines := strings.Split(text, "\n")
	var detected []detectedFailure
	var buildAggregate bool
	testEventIndex := -1
	// The command classifier can establish a test runner before any output is
	// produced. Otherwise only a runner-specific marker may do so; generic
	// language runtime text such as "panic:" or "AssertionError" is not proof
	// that a normal process was executing tests.
	testRunnerEstablished := profile.Known && profile.Stage == domain.StageProjectTest

	for i, raw := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if isResolveDiagnostic(lower) {
			detected = appendOrMerge(detected, detectedFailure{index: i, FailureEvent: FailureEvent{
				Stage: domain.StageProjectResolve, Toolchain: resolveToolchain(line, profile, argv),
				Diagnostic: line, StageEvidence: StageEvidenceResolveDiagnostic,
			}})
			continue
		}
		if match := typeScriptDiagnosticRE.FindStringSubmatch(line); len(match) > 1 {
			detected = appendOrMerge(detected, detectedFailure{index: i, FailureEvent: FailureEvent{
				Stage: domain.StageProjectCompile, Toolchain: "typescript/tsc", DiagnosticCode: strings.ToUpper(match[1]),
				Diagnostic: line, StageEvidence: StageEvidenceCompilerDiagnostic,
			}})
			continue
		}
		if match := rustDiagnosticRE.FindStringSubmatch(line); match != nil || strings.Contains(lower, "could not compile") {
			code := ""
			if len(match) > 1 {
				code = strings.ToUpper(match[1])
			}
			detected = appendOrMerge(detected, detectedFailure{index: i, FailureEvent: FailureEvent{
				Stage: domain.StageProjectCompile, Toolchain: "rust/rustc", DiagnosticCode: code,
				Diagnostic: line, StageEvidence: StageEvidenceCompilerDiagnostic,
			}})
			continue
		}
		if isJavaCompileDiagnostic(lower) {
			toolchain := "java/javac"
			if strings.Contains(lower, ".kt:") || strings.HasPrefix(lower, "e: ") {
				toolchain = "kotlin/kotlinc"
			}
			detected = appendOrMerge(detected, detectedFailure{index: i, FailureEvent: FailureEvent{
				Stage: domain.StageProjectCompile, Toolchain: toolchain, Diagnostic: line,
				StageEvidence: StageEvidenceCompilerDiagnostic,
			}})
			continue
		}
		if goLocationRE.MatchString(line) && goCompileMessageRE.MatchString(line) {
			detected = appendOrMerge(detected, detectedFailure{index: i, FailureEvent: FailureEvent{
				Stage: domain.StageProjectCompile, Toolchain: "go/compiler", Diagnostic: line,
				StageEvidence: StageEvidenceCompilerDiagnostic,
			}})
			continue
		}
		if strings.Contains(lower, "[build failed]") {
			buildAggregate = true
			continue
		}
		goTestMarker := goTestStartRE.MatchString(line)
		if goTestMarker {
			testRunnerEstablished = true
		}
		if goTestMarker || (testRunnerEstablished && (jsTestDiagnosticRE.MatchString(line) || strings.HasPrefix(line, "panic: "))) {
			event := detectedFailure{index: i, FailureEvent: FailureEvent{
				Stage: domain.StageProjectTest, Toolchain: testToolchain(line, profile, argv), Diagnostic: line,
				StageEvidence: StageEvidenceTestRunnerDiagnostic,
			}}
			detected = appendOrMerge(detected, event)
			testEventIndex = matchingEventIndex(detected, event)
			continue
		}
		// Assertion detail normally follows the test runner marker. Keep it
		// with that event, but never attach aggregate FAIL/build boilerplate.
		if testEventIndex >= 0 && !strings.HasPrefix(line, "FAIL") {
			detected[testEventIndex].Diagnostic += "\n" + line
		}
	}

	if buildAggregate && !hasStage(detected, domain.StageProjectCompile) {
		detected = append(detected, detectedFailure{index: len(lines), FailureEvent: FailureEvent{
			Stage: domain.StageProjectCompile, Toolchain: compileToolchain(profile, argv),
			StageEvidence: StageEvidenceBuildAggregate, EvidenceGap: EvidenceGapDiagnosticMissing,
		}})
	}
	if len(detected) == 0 {
		event := FailureEvent{Stage: domain.StageUnknown, Toolchain: outerTool(profile, argv), StageEvidence: StageEvidenceUnclassifiedDiagnostic, EvidenceGap: EvidenceGapStageUnknown}
		if text == "" {
			event.EvidenceGap = EvidenceGapDiagnosticMissing
		} else {
			event.Diagnostic = text
			event.DiagnosticCode = sanitizer.ErrorCode(text)
		}
		detected = append(detected, detectedFailure{FailureEvent: event})
	}

	// Canonical pipeline order represents the earliest stage independent of
	// stdout/stderr scheduling; distinct toolchains at one stage stay separate.
	sort.SliceStable(detected, func(i, j int) bool {
		pi, pj := stagePriority(detected[i].Stage), stagePriority(detected[j].Stage)
		if pi != pj {
			return pi < pj
		}
		return detected[i].index < detected[j].index
	})
	analysis.Events = make([]FailureEvent, len(detected))
	for i := range detected {
		if detected[i].DiagnosticCode == "" {
			detected[i].DiagnosticCode = sanitizer.ErrorCode(detected[i].Diagnostic)
		}
		analysis.Events[i] = detected[i].FailureEvent
	}
	return analysis
}

func matchingEventIndex(events []detectedFailure, event detectedFailure) int {
	for i := range events {
		if events[i].Stage == event.Stage && events[i].Toolchain == event.Toolchain &&
			events[i].StageEvidence == event.StageEvidence && events[i].DiagnosticCode == event.DiagnosticCode {
			return i
		}
	}
	return -1
}

func appendOrMerge(events []detectedFailure, event detectedFailure) []detectedFailure {
	for i := range events {
		if events[i].Stage == event.Stage && events[i].Toolchain == event.Toolchain &&
			events[i].StageEvidence == event.StageEvidence && events[i].DiagnosticCode == event.DiagnosticCode {
			if event.Diagnostic != "" && !strings.Contains(events[i].Diagnostic, event.Diagnostic) {
				if events[i].Diagnostic != "" {
					events[i].Diagnostic += "\n"
				}
				events[i].Diagnostic += event.Diagnostic
			}
			if events[i].DiagnosticCode == "" {
				events[i].DiagnosticCode = event.DiagnosticCode
			}
			return events
		}
	}
	return append(events, event)
}

func hasStage(events []detectedFailure, stage domain.Stage) bool {
	for _, event := range events {
		if event.Stage == stage {
			return true
		}
	}
	return false
}

func isResolveDiagnostic(lower string) bool {
	markers := []string{
		"no required module provides package", "missing go.sum entry", "cannot find module providing package",
		"module lookup disabled", "npm err! code eresolve", "npm error code eresolve",
		"failed to select a version for", "failed to get `", "unable to load the service index for source",
		"restore failed", "could not resolve all files for configuration",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isJavaCompileDiagnostic(lower string) bool {
	return strings.Contains(lower, "compilation error") || strings.Contains(lower, ":compilejava failed") ||
		strings.Contains(lower, ":compilekotlin failed") || (strings.Contains(lower, ".java:") && strings.Contains(lower, " error:")) ||
		(strings.HasPrefix(lower, "e: ") && strings.Contains(lower, ".kt:"))
}

func safeOuterCommand(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	tool := strings.TrimSuffix(strings.ToLower(filepath.Base(argv[0])), ".exe")
	known := map[string]bool{"go": true, "npm": true, "pnpm": true, "yarn": true, "cargo": true, "dotnet": true, "gradle": true, "gradlew": true, "pytest": true, "python": true, "python3": true, "node": true, "tsc": true}
	if !known[tool] {
		return ""
	}
	if len(argv) < 2 {
		return tool
	}
	sub := strings.ToLower(argv[1])
	allowed := map[string]bool{"test": true, "build": true, "check": true, "run": true, "restore": true, "list": true, "mod": true, "get": true, "install": true}
	if allowed[sub] {
		return tool + " " + sub
	}
	return tool
}

func outerTool(profile scanner.CommandProfile, argv []string) string {
	if profile.Tool != "" {
		return strings.ToLower(profile.Tool)
	}
	command := safeOuterCommand(argv)
	if i := strings.IndexByte(command, ' '); i >= 0 {
		command = command[:i]
	}
	return command
}

func resolveToolchain(line string, profile scanner.CommandProfile, argv []string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "module"), strings.Contains(lower, "go.sum"):
		return "go/module"
	case strings.Contains(lower, "npm") || strings.Contains(lower, "eresolve"):
		return "node/npm"
	case strings.Contains(lower, "failed to select a version"), strings.Contains(lower, "failed to get `"):
		return "rust/cargo"
	case strings.Contains(lower, "restore") || strings.Contains(lower, "service index"):
		return "dotnet/restore"
	case strings.Contains(lower, "configuration"):
		return "gradle/resolve"
	default:
		return outerTool(profile, argv)
	}
}

func compileToolchain(profile scanner.CommandProfile, argv []string) string {
	switch outerTool(profile, argv) {
	case "go":
		return "go/compiler"
	case "cargo":
		return "rust/rustc"
	case "tsc", "npm", "pnpm", "yarn":
		return "typescript/tsc"
	case "dotnet":
		return "dotnet/compiler"
	case "gradle", "gradlew":
		return "jvm/compiler"
	default:
		return ""
	}
}

func testToolchain(line string, profile scanner.CommandProfile, argv []string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.HasPrefix(line, "--- FAIL:"), strings.Contains(lower, "_test.go:"):
		return "go/test"
	case strings.Contains(lower, "pytest"):
		return "python/pytest"
	case strings.Contains(lower, "vitest"):
		return "javascript/vitest"
	case strings.Contains(lower, "jest"):
		return "javascript/jest"
	}
	switch outerTool(profile, argv) {
	case "go":
		return "go/test"
	case "pytest", "python", "python3":
		return "python/pytest"
	case "cargo":
		return "rust/test"
	case "dotnet":
		return "dotnet/test"
	case "gradle", "gradlew":
		return "jvm/test"
	case "npm", "pnpm", "yarn", "node":
		return "javascript/test-runner"
	default:
		// Unknown, not JavaScript.
		//
		// This was the fallthrough, so every command whose outer tool was not
		// recognised became a JavaScript test run -- and a Go test invoked
		// through PowerShell was attributed to two toolchains at once:
		// go/test from the lines carrying _test.go:, and this from everything
		// else. One command, two events, one of them naming an ecosystem
		// nothing observed. Reported through report_csx_issue (12).
		//
		// The JavaScript runners it was quietly serving are listed above, so
		// npm test is still npm test. What is left here is a command this
		// build cannot place, and an empty toolchain says that. A guess in
		// the shape of a measurement is worse than a gap, because a gap is
		// visible.
		return ""
	}
}

func stagePriority(stage domain.Stage) int {
	switch stage {
	case domain.StageProcessStart:
		return 0
	case domain.StageProjectResolve:
		return 1
	case domain.StageProjectCompile, domain.StageProjectTypecheck:
		return 2
	case domain.StageProjectTest:
		return 3
	case domain.StageProjectProcess:
		return 4
	default:
		return 5
	}
}
