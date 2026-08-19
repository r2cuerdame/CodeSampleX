package httpapi

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A requirement states the precision it cares about. Demanding string
// equality made a job asking for a runtime LINE unsatisfiable by any real
// machine: the container reports its patch level and nothing else, so the
// receipt was refused for not matching its own job — and that refusal is
// the same statement that would have closed the job, so the job stayed
// claimed until its lease expired and failed again on the next attempt.
func TestVersionSatisfiesComparesWholeComponents(t *testing.T) {
	cases := []struct {
		want, got string
		ok        bool
	}{
		{"", "22.23.1", true},   // no requirement
		{"22", "22.23.1", true}, // the line, answered by a patch
		{"22.23", "22.23.1", true},
		{"3.14", "3.14.2", true},
		{"1.26", "1.26.5", true},
		{"21", "21", true},
		{"22.23.2", "22.23.2", true},
		// Precision the machine cannot answer stays refused.
		{"22.23.2", "22.23.1", false},
		{"22", "24.1.0", false},
		{"3.14", "3.13.9", false},
		// A whole component, never a string prefix: "2" is not "21".
		{"2", "21.0.5", false},
		{"1.2", "1.23.0", false},
	}
	for _, c := range cases {
		if got := versionSatisfies(c.want, c.got); got != c.ok {
			t.Errorf("versionSatisfies(%q, %q) = %v, want %v", c.want, c.got, got, c.ok)
		}
	}
}

// The receipt check as a whole must accept a line-pinned job answered by a
// real container, and still refuse the wrong line.
func TestReceiptMatchesLinePinnedRequirement(t *testing.T) {
	receipt := domain.VerificationReceipt{
		SandboxCapability: domain.CapContainerRun,
		VerifierAdapter:   "node-typescript@1",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Runtime: "node",
			RuntimeVersion: "22.23.1", ExecutionContext: "node",
		},
	}
	line := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "node-typescript@1",
		Ecosystem: "npm", Runtime: "node", RuntimeVersion: "22", ExecutionContext: "node",
	}
	if !receiptMatchesRequirements(receipt, line) {
		t.Error("a container answering the requested runtime line was refused")
	}
	wrongLine := line
	wrongLine.RuntimeVersion = "24"
	if receiptMatchesRequirements(receipt, wrongLine) {
		t.Error("evidence from another runtime line was accepted")
	}
}

// Cross jobs ask a different machine to reproduce a result, so they must
// pin the runtime line rather than the author's patch level — and which
// component names the line differs by runtime.
func TestRuntimeLineKeepsTheLineNotThePatch(t *testing.T) {
	cases := []struct{ runtime, version, want string }{
		{"node", "22.23.2", "22"},
		{"bun", "1.2.19", "1"},
		{"java", "21.0.5", "21"},
		{"python", "3.14.2", "3.14"},
		{"go", "1.26.5", "1.26"},
		{"rustc", "1.85.1", "1.85"},
		{"node", "", ""},
		{"python", "3.14", "3.14"},
	}
	for _, c := range cases {
		if got := domain.RuntimeLine(c.runtime, c.version); got != c.want {
			t.Errorf("RuntimeLine(%q, %q) = %q, want %q", c.runtime, c.version, got, c.want)
		}
	}
}
