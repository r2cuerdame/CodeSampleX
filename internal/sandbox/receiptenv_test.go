package sandbox

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The libc rule now lives in imagebase_test.go, checked against the real
// images rather than against their names.

// Nothing passes --platform, so the container runs the HOST architecture.
// Stamping x64 meant every receipt from an arm64 machine — an Apple
// Silicon laptop, a Graviton runner — described a run that never happened.
func TestReceiptArchFollowsTheHost(t *testing.T) {
	m := domain.SampleManifest{Environment: domain.EnvironmentFingerprint{Ecosystem: "npm"}}
	for _, arch := range []string{"arm64", "x64"} {
		host := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: arch}
		got := DockerRunner{}.StageEnvironment(host, m).Arch
		if got != arch {
			t.Errorf("host arch %s produced a receipt claiming %s", arch, got)
		}
	}
	// Nothing is pinned, so nothing may claim it is.
	args := strings.Join(dockerArgs(pinned("node:22-alpine"), "/tmp/x", true, nil, []string{"true"}, ""), " ")
	if strings.Contains(args, "--platform") {
		t.Skip("the runner now pins a platform; this test should assert that instead")
	}
}
