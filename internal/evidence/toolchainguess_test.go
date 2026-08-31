package evidence

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// An unrecognised command must not be called JavaScript.
//
// testToolchain fell through to "javascript/test-runner" for every outer tool
// it did not know, so a Go test run through PowerShell was attributed to two
// toolchains at once: go/test from the lines carrying _test.go:, and
// javascript/test-runner from everything else. One command, two events, one of
// them naming an ecosystem that was never involved. Reported through
// report_csx_issue (12).
//
// This network's whole claim is that it reports what it observed. A guess in
// the shape of a measurement is worse than a gap, because a gap is visible.
func TestAnUnrecognisedRunnerIsNotAttributedToJavaScript(t *testing.T) {
	profile := scanner.CommandProfile{}
	argv := []string{"powershell", "-File", "run-tests.ps1"}

	if got := testToolchain("some failure text", profile, argv); got == "javascript/test-runner" {
		t.Errorf("a PowerShell command was attributed to %q; nothing here observed JavaScript", got)
	}
}

// The attribution that default was doing correctly stays: a JavaScript runner
// invoked the ordinary way is still JavaScript.
func TestJavaScriptRunnersAreStillNamed(t *testing.T) {
	profile := scanner.CommandProfile{}
	// Only the tools safeOuterCommand is willing to name. npx, bun and deno
	// are not on its allowlist, so they arrive here as an unknown command and
	// are unknown — which is the same answer this change gives PowerShell,
	// and the right one until that allowlist grows.
	for _, argv := range [][]string{
		{"npm", "test"},
		{"pnpm", "test"},
		{"yarn", "test"},
		{"node", "test"},
	} {
		if got := testToolchain("a failing assertion", profile, argv); got != "javascript/test-runner" {
			t.Errorf("%v attributed to %q, want javascript/test-runner", argv, got)
		}
	}
}

// And the markers in the output still win over the command, because a line
// that names its runner is evidence and the command name is an inference.
func TestOutputMarkersStillDecide(t *testing.T) {
	profile := scanner.CommandProfile{}
	argv := []string{"powershell", "-File", "run-tests.ps1"}
	for line, want := range map[string]string{
		"--- FAIL: TestThing":            "go/test",
		"pkg/thing_test.go:42: boom":     "go/test",
		"E   pytest.raises did not fire": "python/pytest",
		"FAIL  vitest  src/a.test.ts":    "javascript/vitest",
	} {
		if got := testToolchain(line, profile, argv); got != want {
			t.Errorf("%q attributed to %q, want %q", line, got, want)
		}
	}
}
