package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

// helperFailureOnStdout is a real child process that fails the way tsc does:
// diagnostics on stdout, nothing on stderr, exit 1. It is skipped unless
// runObserved spawned it.
func TestHelperFailureOnStdout(t *testing.T) {
	if os.Getenv("CSX_TEST_STDOUT_FAILURE") != "1" {
		t.Skip("helper process; run by TestAStdoutOnlyFailureIsSanitizedIntoADiagnosableError")
	}
	fmt.Fprintln(os.Stdout,
		"src/index.ts(12,5): error TS2352: Conversion of type 'string' to type 'number' may be a mistake.")
	os.Exit(1)
}

// The whole loop, over a command that reports the way half of them do.
//
// A failed `npm run typecheck` came back with `sanitizedErrors` holding one
// line — "fingerprint: sha256:…" — and nothing else. The TypeScript error was
// in stdout and stderr was empty, and stderr was the only stream the
// sanitizer had ever been shown. So the agent was handed the hash of a blank
// string as the account of what broke, ran the same build again in a shell to
// find out, and the network recorded the failure under a fingerprint that
// matches nothing that has ever happened anywhere.
func TestAStdoutOnlyFailureIsSanitizedIntoADiagnosableError(t *testing.T) {
	t.Setenv("CSX_TEST_STDOUT_FAILURE", "1")
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "package.json"),
		[]byte(`{"name":"typecheck-fixture","dependencies":{"typescript":"5.9.2"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly // no registry probes, no upload queue
	argv := []string{os.Args[0], "-test.run=^TestHelperFailureOnStdout$"}

	exitCode, stage, result, sanitized, output, err := runObserved(
		context.Background(), nil, nil, cfg, nil, nil, argv, project)
	if err != nil {
		t.Fatalf("runObserved: %v", err)
	}
	if exitCode != 1 || result != "FAIL" {
		t.Fatalf("exitCode = %d, result = %q; want 1/FAIL", exitCode, result)
	}
	if stage != "PROJECT_COMPILE" {
		t.Fatalf("actual stage = %q, want PROJECT_COMPILE", stage)
	}
	if !strings.Contains(output.Stdout, "TS2352") {
		t.Fatalf("the helper did not produce the fixture output: %q / %q", output.Stdout, output.Stderr)
	}

	joined := strings.Join(sanitized, "\n")
	if !strings.Contains(joined, "errorCode: TS2352") {
		t.Errorf("the error code never left the stdout it was printed on:\n%s", joined)
	}
	if !strings.Contains(joined, "Conversion of") {
		t.Errorf("the diagnosis is missing; the agent has only a hash:\n%s", joined)
	}
	// The fingerprint stays — it is a key, and it is no longer the only thing
	// returned.
	if !strings.Contains(joined, "fingerprint: sha256:") {
		t.Errorf("the fingerprint key was dropped:\n%s", joined)
	}
}

func TestGoTestCompileFailureReportsActualCompileStage(t *testing.T) {
	project := t.TempDir()
	files := map[string]string{
		"go.mod":         "module example.com/stagefixture\n\ngo 1.26.5\n",
		"broken_test.go": "package stagefixture\n\nimport \"testing\"\n\nfunc TestBroken(t *testing.T) { _ = doesNotExist }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(project, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	exitCode, stage, result, sanitized, output, err := runObserved(
		context.Background(), nil, nil, cfg, nil, nil, []string{"go", "test", "./..."}, project)
	if err != nil {
		t.Fatalf("runObserved: %v", err)
	}
	if exitCode != 1 || result != "FAIL" || stage != "PROJECT_COMPILE" {
		t.Fatalf("exit/stage/result = %d/%s/%s; output=%q / %q", exitCode, stage, result, output.Stdout, output.Stderr)
	}
	joined := strings.Join(sanitized, "\n")
	for _, want := range []string{"stage=PROJECT_COMPILE", "toolchain=go/compiler", "outer=go test", "undefined: doesNotExist"} {
		if !strings.Contains(joined, want) {
			t.Errorf("classified evidence missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "stage=PROJECT_TEST") {
		t.Errorf("outer command intent leaked into actual stage:\n%s", joined)
	}
}

func TestGoTestAssertionFailureReportsTestExecutionStage(t *testing.T) {
	project := t.TempDir()
	files := map[string]string{
		"go.mod":        "module example.com/testfixture\n\ngo 1.26.5\n",
		"thing_test.go": "package testfixture\n\nimport \"testing\"\n\nfunc TestAssertion(t *testing.T) { t.Errorf(\"got 1, want 2\") }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(project, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	_, stage, result, sanitized, _, err := runObserved(
		context.Background(), nil, nil, cfg, nil, nil, []string{"go", "test", "./..."}, project)
	if err != nil {
		t.Fatalf("runObserved: %v", err)
	}
	joined := strings.Join(sanitized, "\n")
	if result != "FAIL" || stage != "PROJECT_TEST" || !strings.Contains(joined, "toolchain=go/test") {
		t.Fatalf("stage/result/evidence = %s/%s/%s", stage, result, joined)
	}
}

func TestProcessStartFailureDoesNotInheritTestStage(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	_, stage, result, _, output, err := runObserved(
		context.Background(), nil, nil, cfg, nil, nil, []string{"csx-command-that-does-not-exist", "test"}, t.TempDir())
	if err == nil {
		t.Fatal("missing process unexpectedly started")
	}
	if result != "FAIL" || stage != "PROCESS_START" || output.Termination.Kind != "process-start-failed" {
		t.Fatalf("stage/result/termination = %s/%s/%s", stage, result, output.Termination.Kind)
	}
}

// helperSilentFailure exits non-zero having printed nothing at all.
func TestHelperSilentFailure(t *testing.T) {
	if os.Getenv("CSX_TEST_SILENT_FAILURE") != "1" {
		t.Skip("helper process; run by TestASilentFailureReportsNoSanitizedError")
	}
	os.Exit(3)
}

// A command that printed nothing still has structured process evidence. Its
// fingerprint must include that exit coordinate and must not fabricate text.
func TestASilentFailureReportsNoSanitizedError(t *testing.T) {
	t.Setenv("CSX_TEST_SILENT_FAILURE", "1")
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	argv := []string{os.Args[0], "-test.run=^TestHelperSilentFailure$"}

	exitCode, _, result, sanitized, _, err := runObserved(
		context.Background(), nil, nil, cfg, nil, nil, argv, t.TempDir())
	if err != nil {
		t.Fatalf("runObserved: %v", err)
	}
	if exitCode != 3 || result != "FAIL" {
		t.Fatalf("exitCode = %d, result = %q; want 3/FAIL", exitCode, result)
	}
	joined := strings.Join(sanitized, "\n")
	if !strings.Contains(joined, "termination: exit:3") || !strings.Contains(joined, "evidenceQuality: partial") {
		t.Errorf("silent termination evidence = %q", sanitized)
	}
	if strings.Contains(joined, "errorCode:") {
		t.Errorf("silent failure invented an error code: %q", sanitized)
	}
}
