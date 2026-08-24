package evidence

import "testing"

// A failed typecheck said nothing on stderr, so the network was told nothing.
//
// `tsc` writes its diagnostics to stdout and exits non-zero with an empty
// stderr, and stderr was the only stream anything ever sanitized. Sanitizing
// nothing still produces a fingerprint — the hash of a blank template — so a
// broken TypeScript build was recorded, and searched for, under a hash of
// nothing at all. The error the agent needed was sitting in stdout the whole
// time.
func TestAFailureThatOnlyWroteToStdoutIsStillDiagnosable(t *testing.T) {
	out := CommandOutput{
		Stdout: "src/index.ts(12,5): error TS2352: Conversion of type 'string' to type 'number' may be a mistake.\n",
	}
	if got := out.FailureDiagnostics(); got != out.Stdout {
		t.Errorf("FailureDiagnostics() = %q, want the stdout diagnostics", got)
	}
}

// A command that reports on stderr keeps the exact text it always had, and
// with it the exact fingerprint. Everything already recorded against that
// hash stays reachable.
func TestAFailureOnStderrIsUnchanged(t *testing.T) {
	out := CommandOutput{
		Stdout: "ok  \tgithub.com/x/y\n",
		Stderr: "internal/web/pivot.go:944:12: undefined: partz\n",
	}
	if got := out.FailureDiagnostics(); got != out.Stderr {
		t.Errorf("FailureDiagnostics() = %q, want the stderr diagnostics", got)
	}
}

// A runner that prints its own boilerplate on stderr while the compiler it
// wrapped prints the real diagnosis on stdout. "npm error path <dir>" carries
// no error code; TS2352 does, and the code is what a stored failure is keyed
// by.
func TestARunnersBoilerplateDoesNotOutrankTheCompilersCode(t *testing.T) {
	out := CommandOutput{
		Stdout: "src/api.ts(4,9): error TS2352: Conversion of type 'string' to type 'number'.\n",
		Stderr: "npm error Lifecycle script `typecheck` failed with error:\nnpm error workspace repro\n",
	}
	if got := out.FailureDiagnostics(); got != out.Stdout {
		t.Errorf("FailureDiagnostics() = %q, want the stdout diagnostics carrying TS2352", got)
	}
}

// Neither stream names a code — the ordinary Go build failure. stderr is
// where that one lives and must stay where it was.
func TestWithNoCodeAnywhereStderrStillWins(t *testing.T) {
	out := CommandOutput{
		Stdout: "# github.com/x/y\n",
		Stderr: "./main.go:5:2: undefined: foo\n",
	}
	if got := out.FailureDiagnostics(); got != out.Stderr {
		t.Errorf("FailureDiagnostics() = %q, want the stderr diagnostics", got)
	}
}

// A command that failed silently has nothing to diagnose. An empty answer is
// the honest one: what must not happen is a fingerprint of nothing being
// passed off as this failure's identity.
func TestASilentFailureDiagnosesNothing(t *testing.T) {
	if got := (CommandOutput{}).FailureDiagnostics(); got != "" {
		t.Errorf("FailureDiagnostics() = %q, want empty", got)
	}
}
