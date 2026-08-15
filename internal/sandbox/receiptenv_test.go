package sandbox

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// "The verifier images are all alpine, so results carry musl" stopped being
// true when Dart arrived: dart:3.13.0 is Debian. Every Dart receipt claimed
// osVersionBucket alpine and libc musl for a run on glibc — and musl versus
// glibc is the dimension the grader treats as decisive for whether a native
// module loads at all.
func TestReceiptLibcFollowsTheImage(t *testing.T) {
	host := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	for _, eco := range []string{"npm", "pypi", "golang", "cargo", "composer", "gem", "pub", "hex"} {
		img, err := imageFor(eco, "")
		if err != nil {
			continue
		}
		env := DockerRunner{}.StageEnvironment(host, domain.SampleManifest{
			Environment: domain.EnvironmentFingerprint{Ecosystem: eco},
		})
		alpine := strings.Contains(img, "alpine")
		if alpine && (env.Libc != "musl" || env.OSVersionBucket != "alpine") {
			t.Errorf("%s (%s): libc=%q bucket=%q, want musl/alpine", eco, img, env.Libc, env.OSVersionBucket)
		}
		if !alpine && env.Libc == "musl" {
			t.Errorf("%s (%s) is not an alpine image but the receipt says musl", eco, img)
		}
	}
}

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
	args := strings.Join(dockerArgs("node:22-alpine", "/tmp/x", true, nil, []string{"true"}), " ")
	if strings.Contains(args, "--platform") {
		t.Skip("the runner now pins a platform; this test should assert that instead")
	}
}
