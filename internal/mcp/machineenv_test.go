package mcp

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// musl versus glibc decides whether a package with a native module loads at
// all, and the grader treats it that way — but it compares the dimension
// only when BOTH sides declare it, and the tool schema had no libc property
// for an agent to declare it with. Sample manifests do carry it, collected
// from the machine that verified them.
//
// So an agent on Alpine asking about a sample verified on glibc got
// MATCH: EXACT with an empty list of differences. That is the exact failure
// the grader was fixed for, arriving through the only surface an agent can
// touch.
func TestTheMachinesLibcReachesTheGrader(t *testing.T) {
	machine := domain.EnvironmentFingerprint{
		SchemaVersion: 1, OS: "linux", Arch: "amd64", Libc: "musl",
		OSVersionBucket: "alpine",
	}
	// Everything the schema offered an agent, and nothing more.
	asked := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
	}

	got := fillFromMachine(asked, machine)
	if got.Libc != "musl" {
		t.Errorf("libc = %q, want musl — the grader cannot compare what nobody states", got.Libc)
	}
	// What the agent did say is untouched.
	if got.Runtime != "node" || got.ModuleSystem != "esm" {
		t.Errorf("the caller's own answers were altered: %+v", got)
	}
}

// An agent asking about a target it is not running on is asking a different
// question, and the machine must not answer over it.
func TestAStatedDimensionBeatsTheMachine(t *testing.T) {
	machine := domain.EnvironmentFingerprint{OS: "linux", Arch: "amd64", Libc: "glibc"}
	asked := domain.EnvironmentFingerprint{OS: "linux", Arch: "arm64", Libc: "musl"}

	got := fillFromMachine(asked, machine)
	if got.Libc != "musl" || got.Arch != "arm64" {
		t.Errorf("the machine overwrote what the caller asked about: %+v", got)
	}
}

// A Windows host must not contribute its version bucket to a question about
// Linux.
func TestHostShapedDimensionsNeedTheOSToAgree(t *testing.T) {
	machine := domain.EnvironmentFingerprint{OS: "windows", Arch: "amd64", OSVersionBucket: "11"}
	asked := domain.EnvironmentFingerprint{OS: "linux"}

	got := fillFromMachine(asked, machine)
	if got.OSVersionBucket != "" {
		t.Errorf("osVersionBucket = %q, want empty for a different OS", got.OSVersionBucket)
	}
	// Arch is still the machine's: same hardware either way.
	if got.Arch != "amd64" {
		t.Errorf("arch = %q, want the machine's", got.Arch)
	}
}
