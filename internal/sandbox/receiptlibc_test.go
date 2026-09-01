package sandbox

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The libc a receipt claims must be the libc of the image the stages really
// ran in.
//
// The selector and the receipt derive it separately -- one picks an image
// from the manifest, the other describes the environment for signing -- and
// nothing connected the two. That is how the Java path came to bypass the
// libc guard at all: it returned its image before the guard, and no later
// check would have noticed the receipt saying glibc about an Alpine run.
// A receipt naming a libc the run did not use is worse than one saying
// nothing, because everything downstream treats it as measured.
func TestAReceiptNeverClaimsALibcTheImageDoesNotHave(t *testing.T) {
	host := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	for _, tc := range []struct {
		name     string
		manifest domain.SampleManifest
	}{
		// The one production manifest the Java libc guard actually moved:
		// maven, glibc declared, no runtime version to pin a lane with.
		{"maven glibc with no runtime version", domain.SampleManifest{
			VerifierAdapter: "maven-java@1",
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "maven", OS: "linux", Arch: "x64",
				Runtime: "java", Libc: "glibc",
			}}},
		{"maven glibc pinned to 21", domain.SampleManifest{
			VerifierAdapter: "maven-java@1",
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "maven", OS: "linux", Arch: "x64",
				Runtime: "java", RuntimeVersion: "21", Libc: "glibc",
			}}},
		{"gradle glibc", domain.SampleManifest{
			VerifierAdapter: "gradle-java@1",
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "maven", OS: "linux", Arch: "x64",
				Runtime: "java", RuntimeVersion: "21", Libc: "glibc",
			}}},
		// And the lanes that are not Java, so the invariant is the selector's
		// and not one path's habit.
		{"golang glibc", domain.SampleManifest{
			VerifierAdapter: "golang@1",
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "golang", OS: "linux", Arch: "x64",
				Runtime: "go", RuntimeVersion: "1.26", Libc: "glibc",
			}}},
		{"npm musl", domain.SampleManifest{
			VerifierAdapter: "node-typescript@1",
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
				Runtime: "node", RuntimeVersion: "22", Libc: "musl",
			}}},
		{"pypi musl", domain.SampleManifest{
			VerifierAdapter: "python@1",
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "pypi", OS: "linux", Arch: "x64",
				Runtime: "python", RuntimeVersion: "3.14", Libc: "musl",
			}}},
	} {
		signed := DockerRunner{}.VerifierImage(tc.manifest)
		if signed == nil {
			t.Errorf("%s: no image was established, so nothing can be signed about it", tc.name)
			continue
		}
		// The receipt signs a reference; what that reference IS was
		// established by running it, and lives in the registry.
		image, ok := registryEntryFor(signed.Reference)
		if !ok {
			t.Errorf("%s: %s is not an entry this registry describes", tc.name, signed.Reference)
			continue
		}
		env := DockerRunner{}.StageEnvironment(host, tc.manifest)
		if image.libc == "" {
			t.Errorf("%s: image %s declares no libc", tc.name, image.alias)
			continue
		}
		if env.Libc != image.libc {
			t.Errorf("%s: the receipt would say libc=%q while running in %s (libc=%q)",
				tc.name, env.Libc, image.alias, image.libc)
		}
		// And the declaration is honoured, not merely echoed back.
		if declared := tc.manifest.Environment.Libc; declared != "" && env.Libc != declared {
			t.Errorf("%s: manifest declared libc=%q, receipt says %q", tc.name, declared, env.Libc)
		}
		if env.Libc == "glibc" && strings.Contains(image.alias, "alpine") {
			t.Errorf("%s: %s is an Alpine image and the receipt claims glibc", tc.name, image.alias)
		}
	}
}
