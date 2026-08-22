package sandbox

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A receipt states which libc the contract ran against, and musl versus
// glibc is the dimension the grader treats as decisive for whether a
// package with a native module loads at all.
//
// It was inferred from the image NAME. composer:2 is an Alpine image whose
// tag does not say so, so every PHP receipt this project ever produced
// claimed glibc for a run on musl — and a caller on Debian was told a
// musl-verified sample matched their libc exactly.
func TestReceiptLibcComesFromWhatTheImageIs(t *testing.T) {
	host := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	for _, eco := range []string{"npm", "pypi", "golang", "cargo", "composer", "gem", "pub", "hex"} {
		img, err := imageFor(eco, "")
		if err != nil {
			continue
		}
		want, known := registryEntryFor(img)
		if !known {
			t.Errorf("%s uses %s, which is not in the verifier image registry", eco, img)
			continue
		}
		env := DockerRunner{}.StageEnvironment(host, domain.SampleManifest{
			Environment: domain.EnvironmentFingerprint{Ecosystem: eco},
		})
		if env.Libc != want.libc || env.OSVersionBucket != want.bucket {
			t.Errorf("%s (%s): receipt says libc=%q bucket=%q, image is %q/%q",
				eco, img, env.Libc, env.OSVersionBucket, want.libc, want.bucket)
		}
	}
}

// The registry is only worth anything if it is checked against the images it
// describes. This is what stops it drifting as the image set grows — a name
// is not evidence, and neither is a table nobody verifies. It runs the
// PINNED reference, so what is measured is the same bytes a verification
// would run, not whatever the tag points at today.
func TestImageBaseMatchesTheRealImage(t *testing.T) {
	if testing.Short() {
		t.Skip("runs containers")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	for alias, want := range verifierImages {
		if want.bucket == "windowsservercore" {
			// A Linux daemon cannot start these, and a Windows daemon has no
			// libc to report. The entry is checked by TestWindowsImagesAreRealServerCoreImages.
			continue
		}
		image := want.ref()
		t.Run(alias, func(t *testing.T) {
			out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "sh", image,
				"-c", `if ls /lib/ld-musl-* >/dev/null 2>&1; then echo musl; else echo glibc; fi`).Output()
			if err != nil {
				t.Skipf("could not run %s: %v", image, err)
			}
			got := strings.TrimSpace(string(out))
			if got != want.libc {
				t.Errorf("%s is %s; the table says %s", image, got, want.libc)
			}
		})
	}
}

// A sample that declares no runtime must not borrow the host's. The
// container is chosen from the sample's ecosystem, so verifying an npm
// sample on a machine whose collected runtime was "go" picked the image by
// "go" and then stamped the receipt with it — a receipt naming a runtime
// the container never had.
func TestASampleWithoutARuntimeDoesNotBorrowTheHosts(t *testing.T) {
	host := domain.EnvironmentFingerprint{
		SchemaVersion: 1, OS: "linux", Arch: "x64", Runtime: "go", RuntimeVersion: "1.26",
	}
	env := DockerRunner{}.StageEnvironment(host, domain.SampleManifest{
		Environment: domain.EnvironmentFingerprint{Ecosystem: "npm"},
	})
	if env.Runtime == "go" {
		t.Errorf("an npm sample was stamped with the host's runtime: %+v", env)
	}
	if env.Runtime != "node" {
		t.Errorf("runtime = %q, want node — the ecosystem's own default", env.Runtime)
	}
}
