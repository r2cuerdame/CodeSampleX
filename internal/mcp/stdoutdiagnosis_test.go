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

	exitCode, _, result, sanitized, output, err := runObserved(
		context.Background(), nil, nil, cfg, nil, nil, argv, project)
	if err != nil {
		t.Fatalf("runObserved: %v", err)
	}
	if exitCode != 1 || result != "FAIL" {
		t.Fatalf("exitCode = %d, result = %q; want 1/FAIL", exitCode, result)
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

// helperSilentFailure exits non-zero having printed nothing at all.
func TestHelperSilentFailure(t *testing.T) {
	if os.Getenv("CSX_TEST_SILENT_FAILURE") != "1" {
		t.Skip("helper process; run by TestASilentFailureReportsNoSanitizedError")
	}
	os.Exit(3)
}

// A command that printed nothing has no error to sanitize, and a fingerprint
// of nothing is not a stand-in for one. It is the same hash for every silent
// failure on every machine, so it matches nothing, and returning it alone
// hands the caller a line that reads like an error and says nothing.
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
	if len(sanitized) != 0 {
		t.Errorf("a silent failure still produced a sanitized error: %q", sanitized)
	}
}
