package sandbox

import (
	"regexp"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// pinnedRef is the only image form a verifier lane may execute: a
// human-readable alias plus the immutable content digest that decides which
// bytes actually run.
var pinnedRef = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)

// lane is one (daemon, manifest) pair a selector will answer.
type lane struct {
	name        string
	containerOS string
	manifest    domain.SampleManifest
}

func npmLane(runtime string) domain.SampleManifest {
	return domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", Runtime: runtime,
	}}
}

func ecoLane(ecosystem, runtime, runtimeVersion string) domain.SampleManifest {
	return domain.SampleManifest{Environment: domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: ecosystem, Runtime: runtime, RuntimeVersion: runtimeVersion,
	}}
}

func javaLane(adapter, runtimeVersion string) domain.SampleManifest {
	return domain.SampleManifest{
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "maven", Runtime: "java", RuntimeVersion: runtimeVersion,
		},
		VerifierAdapter: adapter,
	}
}

// verifierLanes is every lane this binary can select an image for.
//
// It is a readable checklist, not the safety net: the net is that every
// selectable image must come out of the one registry, and every registry
// entry must be digest-pinned. A lane forgotten here therefore still cannot
// reach a mutable tag.
func verifierLanes() []lane {
	lanes := []lane{
		{"npm/default", ContainerOSLinux, npmLane("")},
		{"npm/node", ContainerOSLinux, npmLane("node")},
		{"npm/bun", ContainerOSLinux, npmLane("bun")},
		{"npm/deno", ContainerOSLinux, npmLane("deno")},
		{"pypi/default", ContainerOSLinux, ecoLane("pypi", "python", "")},
		{"pypi/3.12", ContainerOSLinux, ecoLane("pypi", "python", "3.12")},
		{"pypi/3.14", ContainerOSLinux, ecoLane("pypi", "python", "3.14")},
		{"golang", ContainerOSLinux, ecoLane("golang", "", "")},
		{"cargo", ContainerOSLinux, ecoLane("cargo", "", "")},
		{"composer", ContainerOSLinux, ecoLane("composer", "", "")},
		{"gem", ContainerOSLinux, ecoLane("gem", "", "")},
		{"pub", ContainerOSLinux, ecoLane("pub", "", "")},
		{"hex", ContainerOSLinux, ecoLane("hex", "", "")},
		{"browser/chrome134", ContainerOSLinux, domain.SampleManifest{
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "npm", Runtime: "node",
				ExecutionContext: "browser", BrowserFamily: "chrome", BrowserMajor: "134",
			}}},
		{"windows/golang", ContainerOSWindows, ecoLane("golang", "", "")},
		{"windows/golang-go", ContainerOSWindows, ecoLane("golang", "go", "")},
		{"windows/pypi-default", ContainerOSWindows, ecoLane("pypi", "python", "")},
		{"windows/pypi-3.12", ContainerOSWindows, ecoLane("pypi", "python", "3.12")},
		{"windows/pypi-3.14", ContainerOSWindows, ecoLane("pypi", "python", "3.14")},
	}
	for _, v := range []string{"", "8", "11", "17", "21", "25"} {
		lanes = append(lanes, lane{"maven-java@1/" + v, ContainerOSLinux, javaLane("maven-java@1", v)})
	}
	for _, v := range []string{"8", "11", "17", "21", "25"} {
		lanes = append(lanes, lane{"gradle-java@1/" + v, ContainerOSLinux, javaLane("gradle-java@1", v)})
	}
	return lanes
}

// The public promise is that a published sample's contract ran in a PINNED
// container. A floating tag is not a pin: docker resolves it against
// whatever the registry points at today, or against whatever bytes already
// sit in that worker's local cache under the same name. Two workers can
// then produce receipts that name the same environment and describe runs of
// different software.
func TestEveryVerifierLaneRunsADigestPinnedImage(t *testing.T) {
	for _, l := range verifierLanes() {
		img, err := imageForManifestOn(l.containerOS, l.manifest)
		if err != nil {
			t.Errorf("%s: no image: %v", l.name, err)
			continue
		}
		if !pinnedRef.MatchString(img) {
			t.Errorf("%s runs %q, which is a mutable tag, not an immutable digest", l.name, img)
		}
	}
}

// The alias is for humans; the digest is the execution authority. Both are
// kept in the reference so an operator can read what the image is meant to
// be without being able to change what actually runs.
func TestPinnedReferenceKeepsAReadableAlias(t *testing.T) {
	img, err := imageForManifestOn(ContainerOSLinux, npmLane("node"))
	if err != nil {
		t.Fatal(err)
	}
	alias, _, ok := strings.Cut(img, "@")
	if !ok || alias != "node:22-alpine" {
		t.Errorf("npm/node reference = %q, want alias node:22-alpine plus a digest", img)
	}
}

// The registry is the single place an image may come from. Selecting a
// string that is not in it would reintroduce exactly what this change
// removes: an image whose bytes nobody measured and whose base the receipt
// then guesses from the name.
func TestEveryRegistryEntryIsDigestPinned(t *testing.T) {
	if len(verifierImages) == 0 {
		t.Fatal("the verifier image registry is empty")
	}
	seen := map[string]bool{}
	for alias, img := range verifierImages {
		if img.alias != alias {
			t.Errorf("registry key %q holds alias %q", alias, img.alias)
		}
		if strings.Contains(alias, "@") {
			t.Errorf("registry alias %q must be the readable tag, without a digest", alias)
		}
		if !pinnedRef.MatchString(img.ref()) {
			t.Errorf("registry entry %q is %q, not an immutable digest reference", alias, img.ref())
		}
		if img.libc == "" && img.bucket != "windowsservercore" {
			t.Errorf("registry entry %q records no libc; the receipt would have to guess it", alias)
		}
		if seen[img.digest] {
			t.Errorf("registry entry %q reuses a digest another alias already claims", alias)
		}
		seen[img.digest] = true
	}
}

func TestEveryLaneImageComesFromTheRegistry(t *testing.T) {
	for _, l := range verifierLanes() {
		img, err := imageForManifestOn(l.containerOS, l.manifest)
		if err != nil {
			continue
		}
		if _, ok := registryEntryFor(img); !ok {
			t.Errorf("%s runs %q, which is not in the verifier image registry", l.name, img)
		}
	}
}

// Requirement: the same lane on two different workers runs the same image
// bytes. Selection is a pure function of the lane, so two independently
// constructed runners must agree — and because what they agree on is a
// digest, a worker whose local cache still holds an older `node:22-alpine`
// cannot substitute it: docker resolves a digest reference by content.
func TestTheSameLaneRunsTheSameBytesOnEveryWorker(t *testing.T) {
	for _, l := range verifierLanes() {
		a, errA := imageForManifestOn(l.containerOS, l.manifest)
		b, errB := imageForManifestOn(l.containerOS, l.manifest)
		if (errA == nil) != (errB == nil) || a != b {
			t.Errorf("%s: two workers selected %q and %q", l.name, a, b)
			continue
		}
		if errA != nil {
			continue
		}
		imgA := DockerRunner{ContainerOS: l.containerOS}.VerifierImage(l.manifest)
		imgB := DockerRunner{ContainerOS: l.containerOS}.VerifierImage(l.manifest)
		if imgA == nil || imgB == nil || *imgA != *imgB {
			t.Errorf("%s: receipt image differs between workers: %+v vs %+v", l.name, imgA, imgB)
			continue
		}
		if imgA.Reference != a {
			t.Errorf("%s: receipt says %q but the stage runs %q", l.name, imgA.Reference, a)
		}
	}
}

// What reaches `docker run` has to be the digest, not the alias. A stale
// local tag is the failure this closes: docker will happily run whatever
// bytes already sit under `node:22-alpine` on that machine.
func TestDockerRunReceivesTheDigestNotTheTag(t *testing.T) {
	for _, containerOS := range []string{ContainerOSLinux, ContainerOSWindows} {
		for _, l := range verifierLanes() {
			if l.containerOS != containerOS {
				continue
			}
			img, err := imageForManifestOn(containerOS, l.manifest)
			if err != nil {
				continue
			}
			args := dockerArgsOn(containerOS, img, "/tmp/x", true, nil, []string{"true"}, "")
			found := false
			for _, a := range args {
				if a == img {
					found = true
				}
				if a == strings.SplitN(img, "@", 2)[0] {
					t.Errorf("%s: docker run received the mutable tag %q", l.name, a)
				}
			}
			if !found {
				t.Errorf("%s: docker run never received the pinned reference %q: %v", l.name, img, args)
			}
		}
	}
}
