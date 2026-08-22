package httpapi

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func containerWorker() authoringWorkRequest {
	return authoringWorkRequest{
		SandboxCapability: domain.CapContainerRun,
		VerifierOS:        []string{"linux"},
	}
}

// A Gradle plugin marker has no code in it.
//
// Gradle publishes one of these for every plugin id: a pom whose only job is
// to point at the artifact that does the work. There is no jar, so there are
// no classes, no symbols, and nothing a contract could call. Writing a sample
// for it is not hard — it is impossible.
//
// It was handed out anyway. One coordinate,
// org.jetbrains.kotlin.plugin.serialization.gradle.plugin, took an authoring
// slot on a 24-hour lease and held it: the agent tried 22 times, went as far
// as disassembling the csx binary looking for what to call, and every restart
// was handed the same coordinate again. Sample production for the whole
// network fell from 33 an hour to nothing while that ran.
//
// The name is the proof. Gradle's marker convention is structural, not a
// guess: the artifactId is always the plugin id with ".gradle.plugin"
// appended, and such an artifact is always pom-only.
func TestAGradlePluginMarkerIsNeverOfferedAsAuthoringWork(t *testing.T) {
	marker := serverstore.WantedRow{
		Ecosystem: "maven",
		Name:      "org.jetbrains.kotlin.plugin.serialization/org.jetbrains.kotlin.plugin.serialization.gradle.plugin",
		Version:   "2.4.10",
		Asks:      3,
		Kind:      "WANTED",
	}
	if authoringCandidateEligible(marker, containerWorker()) {
		t.Error("offered a pom-only Gradle plugin marker as authoring work")
	}
}

// The plugin's real implementation artifact is ordinary work and must stay
// offerable — excluding it would lose the very sample the marker points at.
func TestTheImplementationBehindAMarkerIsStillOffered(t *testing.T) {
	impl := serverstore.WantedRow{
		Ecosystem: "maven",
		Name:      "org.jetbrains.kotlin/kotlin-serialization-compiler-plugin-embeddable",
		Version:   "2.4.10",
		Asks:      3,
		Kind:      "WANTED",
	}
	if !authoringCandidateEligible(impl, containerWorker()) {
		t.Error("excluded a real maven artifact that happens to be a plugin implementation")
	}
}

// The rule is about maven coordinates. A package in another ecosystem that
// merely ends in the same letters is not a Gradle marker.
func TestOnlyMavenCoordinatesAreReadAsMarkers(t *testing.T) {
	npm := serverstore.WantedRow{
		Ecosystem: "npm",
		Name:      "some.gradle.plugin",
		Version:   "1.0.0",
		Asks:      1,
		Kind:      "WANTED",
	}
	if !authoringCandidateEligible(npm, containerWorker()) {
		t.Error("applied a maven naming convention to an npm package")
	}
}

// A platform-specific native package has no JS API to write a contract
// against, on any worker.
//
// npm publishes one per platform for a native addon — @tailwindcss/oxide
// ships @tailwindcss/oxide-linux-x64-gnu, @napi-rs/lzma ships
// @napi-rs/lzma-linux-x64-gnu — and their main is the .node binary itself.
// Measured on the registry:
//
//	@tailwindcss/oxide-linux-x64-gnu  main=tailwindcss-oxide.linux-x64-gnu.node
//	@napi-rs/lzma-linux-x64-gnu       main=lzma.linux-x64-gnu.node
//	@tailwindcss/oxide                main=index.js
//
// The eligibility rule already recognised these by name and used it only to
// refuse a worker on the wrong OS. A linux worker was still handed the linux
// one — installable, and impossible to write a sample for, because the thing
// a sample would import is a binary the parent package selects internally.
func TestANativePlatformPackageIsNeverOfferedAsAuthoringWork(t *testing.T) {
	for _, name := range []string{
		"@tailwindcss/oxide-linux-x64-gnu",
		"@napi-rs/lzma-linux-x64-gnu",
		"@tailwindcss/oxide-darwin-arm64",
	} {
		candidate := serverstore.WantedRow{
			Ecosystem: "npm", Name: name, Version: "4.3.3", Asks: 1, Kind: "WANTED",
		}
		if authoringCandidateEligible(candidate, containerWorker()) {
			t.Errorf("offered %s, which has no JS API on any platform", name)
		}
	}
}

// The parent package is the one worth a sample and must stay offerable.
func TestTheParentOfANativePackageIsStillOffered(t *testing.T) {
	for _, name := range []string{"@tailwindcss/oxide", "@napi-rs/lzma", "node-linux-utils"} {
		candidate := serverstore.WantedRow{
			Ecosystem: "npm", Name: name, Version: "4.3.3", Asks: 1, Kind: "WANTED",
		}
		if !authoringCandidateEligible(candidate, containerWorker()) {
			t.Errorf("excluded %s, which is an ordinary package", name)
		}
	}
}
