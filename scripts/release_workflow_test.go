// The release pipeline used to be reviewed by a person before the signing
// environment would run. Nobody reviews it now, so the things that review
// caught have to be checked here instead: that the updater seed is reachable
// from exactly one job, that the job holding it can neither publish nor mint
// a token, that no trigger other than a `v*` tag can reach it, and that every
// action is still pinned to a reviewed commit.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	signingSecretRef = "secrets.CSX_UPDATE_SIGNING_KEY_B64"
	signingEnv       = "codesamplex-release-signing"
	trustRootFile    = "updater-public-key.b64"
)

var (
	jobHeader = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):[ \t]*$`)
	usesLine  = regexp.MustCompile(`^\s*-?\s*uses:\s*(\S+)\s*(?:#.*)?$`)
	pinnedRef = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@([0-9a-f]{40})$`)
)

// allowedActions is the reviewed set. A new entry is a deliberate decision to
// let more third-party code run in the release pipeline, so it belongs in a
// diff rather than in whatever the latest tag happens to point at today.
var allowedActions = map[string]bool{
	"actions/checkout":          true,
	"actions/setup-go":          true,
	"actions/upload-artifact":   true,
	"actions/download-artifact": true,
}

func releaseWorkflow(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// releaseJobs splits the workflow into its top-level jobs without a YAML
// dependency the module does not carry. Jobs are the only two-space keys
// under `jobs:`, and every line of a job is indented past that.
func releaseJobs(t *testing.T, workflow string) map[string]string {
	t.Helper()
	lines := strings.Split(workflow, "\n")
	start := -1
	for i, line := range lines {
		if line == "jobs:" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatal("release workflow has no top-level jobs: block")
	}
	jobs := map[string]string{}
	name, body := "", []string{}
	flush := func() {
		if name != "" {
			jobs[name] = strings.Join(body, "\n")
		}
		name, body = "", nil
	}
	for _, line := range lines[start:] {
		if m := jobHeader.FindStringSubmatch(line); m != nil {
			flush()
			name = m[1]
			continue
		}
		if line != "" && !strings.HasPrefix(line, " ") {
			break // back at column zero: the jobs block is over
		}
		if name != "" {
			body = append(body, line)
		}
	}
	flush()
	if len(jobs) == 0 {
		t.Fatal("release workflow parsed into zero jobs")
	}
	return jobs
}

// The seed must be reachable from one job, and that job must be the one the
// protected environment gates. Before the split, the same job also compiled
// the tree, ran every test in the repository, executed a bundler and a
// downloaded publisher binary, and carried a write-scoped token.
func TestUpdaterSeedIsReachableOnlyFromTheProtectedSigningJob(t *testing.T) {
	workflow := releaseWorkflow(t)
	jobs := releaseJobs(t, workflow)

	sign, ok := jobs["sign"]
	if !ok {
		t.Fatalf("release workflow has no sign job; jobs = %v", jobKeys(jobs))
	}
	total := strings.Count(workflow, signingSecretRef)
	inSign := strings.Count(sign, signingSecretRef)
	if inSign == 0 {
		t.Fatal("the sign job never references the updater signing seed")
	}
	if total != inSign {
		t.Fatalf("%s appears %d times in the workflow but only %d in the sign job", signingSecretRef, total, inSign)
	}
	for name, body := range jobs {
		hasEnv := strings.Contains(body, "environment: "+signingEnv)
		if name == "sign" && !hasEnv {
			t.Fatalf("the sign job does not run in the %s environment", signingEnv)
		}
		if name != "sign" && hasEnv {
			t.Fatalf("job %q also enters the %s environment", name, signingEnv)
		}
		if name != "sign" && strings.Contains(body, "environment:") {
			t.Fatalf("job %q declares an environment; only sign may", name)
		}
	}
}

// A job that can neither write to the repository nor mint an OIDC token
// cannot turn a leaked seed into a published release on its own.
func TestSigningJobCarriesNoPublishAuthority(t *testing.T) {
	jobs := releaseJobs(t, releaseWorkflow(t))
	sign := jobs["sign"]
	for _, forbidden := range []string{"contents: write", "id-token: write", "packages: write"} {
		if strings.Contains(sign, forbidden) {
			t.Fatalf("the sign job grants %q; it signs and nothing else", forbidden)
		}
	}
	if !strings.Contains(sign, "contents: read") {
		t.Fatal("the sign job does not pin itself to contents: read")
	}
	publish := jobs["publish"]
	for _, required := range []string{"contents: write", "id-token: write"} {
		if !strings.Contains(publish, required) {
			t.Fatalf("the publish job is missing %q", required)
		}
	}
	if strings.Contains(publish, signingSecretRef) {
		t.Fatal("the publish job reaches the updater seed")
	}
	if strings.Contains(jobs["build"], signingSecretRef) {
		t.Fatal("the build job reaches the updater seed")
	}
}

// The environment is restricted to `v*` tags. A manual replay is permitted
// only because the first job rejects every non-tag ref before any build or
// signing job can start; this recovers a dropped GitHub tag event without
// turning workflow_dispatch on main into release authority.
func TestOnlyAVersionTagRefCanStartTheRelease(t *testing.T) {
	workflow := releaseWorkflow(t)
	head := workflow
	if i := strings.Index(workflow, "\njobs:"); i > 0 {
		head = workflow[:i]
	}
	if !strings.Contains(head, "  push:\n    tags: [\"v*\"]") {
		t.Fatal("the release trigger is no longer a v* tag push")
	}
	if !strings.Contains(head, "  workflow_dispatch:") {
		t.Fatal("the release has no authenticated replay path when GitHub drops a tag event")
	}
	for _, forbidden := range []string{"pull_request", "pull_request_target", "workflow_call", "schedule", "repository_dispatch"} {
		if strings.Contains(head, forbidden) {
			t.Fatalf("release trigger %q can reach the signing environment from a non-tag ref", forbidden)
		}
	}
	jobs := releaseJobs(t, workflow)
	refGate := jobs["release-ref"]
	for _, required := range []string{
		`test "$GITHUB_REF_TYPE" = "tag"`,
		`refs/tags/v*) ;;`,
		"contents: read",
	} {
		if !strings.Contains(refGate, required) {
			t.Fatalf("manual release replay lacks tag-ref guard %q", required)
		}
	}
	if !strings.Contains(jobs["windows-test"], "needs: release-ref") {
		t.Fatal("release tests can start before the tag-ref gate")
	}
}

// Nothing signs until the seed in the protected environment, the pinned
// environment variable and the key the build job actually stamped into the
// six client binaries are the same key. An unnoticed trust-root rotation
// strands every installed client, and no human is watching for it.
func TestSigningRefusesUnlessThreeTrustRootsAgree(t *testing.T) {
	jobs := releaseJobs(t, releaseWorkflow(t))
	sign := jobs["sign"]
	for _, required := range []string{
		`EXPECTED_UPDATE_PUBLIC_KEY_B64: ${{ vars.CSX_UPDATE_PUBLIC_KEY_B64 }}`,
		`STAMPED_UPDATE_PUBLIC_KEY_B64: ${{ needs.build.outputs.public_key }}`,
		`PUB=$(go run ./scripts/update_manifest.go public)`,
		`test -n "$EXPECTED_UPDATE_PUBLIC_KEY_B64"`,
		`test "$PUB" = "$EXPECTED_UPDATE_PUBLIC_KEY_B64"`,
		`test -n "$STAMPED_UPDATE_PUBLIC_KEY_B64"`,
		`test "$PUB" = "$STAMPED_UPDATE_PUBLIC_KEY_B64"`,
	} {
		if !strings.Contains(sign, required) {
			t.Fatalf("the sign job no longer performs: %s", required)
		}
	}
	build := jobs["build"]
	if !strings.Contains(build, "PUB=$(tr -d '[:space:]' < .github/"+trustRootFile+")") {
		t.Fatal("the build job no longer stamps the committed trust root")
	}
	if !strings.Contains(build, "internal/update.PublicKeyBase64=${{ steps.trust_root.outputs.public_key }}") {
		t.Fatal("the cross-compile no longer stamps the trust root it validated")
	}
}

// The committed half of the trust root has to be the real one: the value the
// build job stamps into every client binary.
func TestCommittedTrustRootIsAnEd25519PublicKey(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", trustRootFile))
	if err != nil {
		t.Fatalf("read pinned trust root: %v", err)
	}
	encoded := strings.TrimSpace(string(raw))
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("pinned trust root is not base64: %v", err)
	}
	if len(key) != ed25519.PublicKeySize {
		t.Fatalf("pinned trust root is %d bytes, want %d", len(key), ed25519.PublicKeySize)
	}
	if strings.ContainsAny(encoded, " \t\r\n") {
		t.Fatalf("pinned trust root carries inner whitespace: %q", encoded)
	}
}

// Signing no longer happens in the job that compiled the binaries, so the
// artifact it downloads has to be tied back to the build that produced it by
// a value that did not travel inside the artifact.
func TestArtifactConsumersRebindToTheBuildThatProducedIt(t *testing.T) {
	jobs := releaseJobs(t, releaseWorkflow(t))
	if !strings.Contains(jobs["build"], `sha256=$(sha256sum dist/SHA256SUMS.txt | cut -d' ' -f1)`) {
		t.Fatal("the build job no longer publishes the checksum-file digest as a job output")
	}
	for _, name := range []string{"sign", "publish"} {
		body := jobs[name]
		for _, required := range []string{
			"EXPECTED_CHECKSUMS_SHA256: ${{ needs.build.outputs.checksums_sha256 }}",
			`echo "${EXPECTED_CHECKSUMS_SHA256}  dist/SHA256SUMS.txt" | sha256sum -c -`,
			"(cd dist && sha256sum -c SHA256SUMS.txt)",
		} {
			if !strings.Contains(body, required) {
				t.Fatalf("the %s job no longer performs: %s", name, required)
			}
		}
	}
}

// "Confirm all third-party Actions remain pinned to reviewed full commit
// SHAs" used to be an instruction to the person approving the environment.
// There is no such person, so it is a test.
func TestEveryReleaseActionStaysPinnedToAReviewedCommit(t *testing.T) {
	workflow := releaseWorkflow(t)
	seen := 0
	for _, line := range strings.Split(workflow, "\n") {
		m := usesLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		seen++
		ref := pinnedRef.FindStringSubmatch(m[1])
		if ref == nil {
			t.Fatalf("action %q is not pinned to a full 40-character commit SHA", m[1])
		}
		if !allowedActions[ref[1]] {
			t.Fatalf("action %q is outside the reviewed release action set", ref[1])
		}
	}
	if seen == 0 {
		t.Fatal("no uses: lines found; the parser stopped matching the workflow")
	}
}

// The order is the safety property: nothing is signed before the tree tests
// clean and the monotonic guard has run, and nothing is published before it
// is signed.
func TestReleaseStagesStayInOrder(t *testing.T) {
	jobs := releaseJobs(t, releaseWorkflow(t))
	for job, needs := range map[string]string{
		"build":   "needs: windows-test",
		"sign":    "needs: build",
		"publish": "needs: [build, sign]",
		"farm":    "needs: publish",
	} {
		body, ok := jobs[job]
		if !ok {
			t.Fatalf("release workflow has no %s job; jobs = %v", job, jobKeys(jobs))
		}
		if !strings.Contains(body, needs) {
			t.Fatalf("the %s job no longer declares %q", job, needs)
		}
	}
	build := jobs["build"]
	guard := strings.Index(build, "update_manifest.go guard")
	compile := strings.Index(build, "name: Cross-compile")
	if guard < 0 || compile < 0 || guard > compile {
		t.Fatalf("the monotonic release guard must run before the cross-compile: guard=%d compile=%d", guard, compile)
	}
}

// Publishing the client and observing the fleet run it are one release
// contract. Self-convergence may repair a failed rollout later, but it cannot
// make the Release run green: production uses that conclusion as its proof
// that the client half completed before the server half starts.
func TestPublishedReleaseCannotSucceedWithoutFarmEvidence(t *testing.T) {
	farm := releaseJobs(t, releaseWorkflow(t))["farm"]
	for _, required := range []string{
		`if [ -z "${GH_TOKEN:-}" ]; then`,
		"::error::CSX_FARM_DISPATCH_TOKEN is required",
		"Wait for the farm to report",
		`--json databaseId,displayTitle`,
		`--arg title "farm -> $TAG"`,
		`.databaseId > $before and .displayTitle == $title`,
		`if [ "$CONCLUSION" = "success" ]; then`,
		"::error::farm rollout",
	} {
		if !strings.Contains(farm, required) {
			t.Errorf("the farm release gate is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"dispatched=false",
		"steps.dispatch.outputs.dispatched",
		"will converge on $TAG within ten minutes on its own",
		"continue-on-error:",
	} {
		if strings.Contains(farm, forbidden) {
			t.Errorf("the farm release gate still permits an unverified success through %q", forbidden)
		}
	}
}

// A normal release is unattended, and the document that operators read has to
// say so — the contract this pipeline now depends on is that no one is
// waiting to press a button.
func TestOperationsDocumentsTheUnattendedReleaseContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "docs", "operations.md"))
	if err != nil {
		t.Fatalf("read operations doc: %v", err)
	}
	doc := string(raw)
	if !strings.Contains(doc, "unattended") {
		t.Fatal("docs/operations.md does not state the unattended release contract")
	}
	if strings.Contains(doc, "required reviewers and protected `v*` tag rules are mandatory") {
		t.Fatal("docs/operations.md still requires an environment reviewer")
	}
}

func TestOperationsRequiresVerifiedFleetBeforeServerRestart(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "docs", "operations.md"))
	if err != nil {
		t.Fatalf("read operations doc: %v", err)
	}
	doc := strings.Join(strings.Fields(string(raw)), " ")
	for _, required := range []string{
		"successful `Release` run for that exact target SHA",
		"complete the client rollout to every verifier",
		"Only after both checks pass on every verifier host",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("operations doc is missing the fleet-first contract %q", required)
		}
	}
	if strings.Contains(doc, "or at least in the same change") {
		t.Fatal("operations doc still permits a concurrent client/server rollout")
	}
}

func jobKeys(jobs map[string]string) []string {
	names := make([]string, 0, len(jobs))
	for name := range jobs {
		names = append(names, name)
	}
	return names
}

// The Windows launcher must be a GUI-subsystem binary.
//
// Measured on this workstation 2026-09-01, spawned from a console-less parent
// with pipes on stdio -- the shape an MCP host uses:
//
//	launcher built CONSOLE   2 console windows appeared
//	launcher built windowsgui  0
//
// Windows allocates a console for a console-subsystem process BEFORE any of
// its code runs, so no amount of CreateProcess policy inside the launcher can
// prevent its own window; the launcher can only decide what its payload gets.
// A host that has no console of its own -- and Claude Code has none, while
// codex.exe does, which is why only one of them showed the window -- therefore
// gets a fresh console per csx it starts.
//
// A GUI-subsystem process still inherits whatever stdio handles its parent
// gives it. Verified against a real cmd.exe console: `--launcher-version`
// prints on stdout and launcher diagnostics print on stderr exactly as before,
// and the same holds for a pipe and for a file redirect.
func TestTheWindowsLauncherIsBuiltWithoutAConsole(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	at := strings.Index(body, "csx-launcher-windows-$arch.exe")
	if at < 0 {
		t.Fatal("the release workflow no longer builds a Windows launcher")
	}
	// The ldflags for that build are on the line above the -o.
	start := strings.LastIndex(body[:at], "GOOS=windows")
	if start < 0 {
		t.Fatal("could not find the Windows launcher build")
	}
	build := body[start : at+len("csx-launcher-windows-$arch.exe")]
	if !strings.Contains(build, "-H=windowsgui") {
		t.Errorf("the Windows launcher is built as a console binary, so Windows "+
			"gives every csx an MCP host starts its own cmd window:\n%s", build)
	}
	// And only the launcher: the payload is run BY the launcher, which decides
	// its window, and a GUI-subsystem payload would lose that distinction.
	payloadAt := strings.Index(body, "csx-$os-$arch$ext")
	if payloadAt < 0 {
		t.Fatal("the release workflow no longer builds the payload")
	}
	payloadStart := strings.LastIndex(body[:payloadAt], "go build")
	if strings.Contains(body[payloadStart:payloadAt], "-H=windowsgui") {
		t.Error("the payload is built without a console; the launcher decides that, per invocation")
	}
}

// Nothing may touch a release binary after its checksum is taken.
//
// SHA256SUMS.txt is what the installer holds its download to, and the signed
// updater manifest carries the same digests. Authenticode signing MODIFIES
// the file it signs, so a signing step placed after `sha256sum` would produce
// a release whose every recorded digest describes bytes that were never
// published -- and the failure appears not at release time but at the moment
// a user tries to install, as a checksum mismatch on a file that is perfectly
// intact.
//
// This is the invariant to hold when Authenticode signing lands. It cannot be
// done in the build job at all: Authenticode requires a Windows runner and
// the cross-compile runs on ubuntu, so signing arrives as its own job between
// build and the manifest signer -- and that job must recompute SHA256SUMS.txt
// over what it produced.
func TestNothingModifiesABinaryAfterItsChecksumIsTaken(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	sums := strings.Index(body, "sha256sum csx-* > SHA256SUMS.txt")
	if sums < 0 {
		t.Fatal("the release no longer records asset checksums")
	}
	// Every Go build of a release binary happens before that line.
	for _, build := range []string{
		`-o "dist/csx-$os-$arch$ext" ./cmd/csx`,
		`-o "dist/csx-launcher-windows-$arch.exe" ./cmd/csx-launcher`,
		"-o dist/csx-server-linux-amd64 ./cmd/csx-server",
	} {
		at := strings.Index(body, build)
		if at < 0 {
			t.Errorf("the release no longer builds %q", build)
			continue
		}
		if at > sums {
			t.Errorf("%q is built after its checksum is taken", build)
		}
	}

	// And if a signing step ever appears, it must come before the checksums
	// too -- in this file that means before this line, or in an earlier job.
	for _, marker := range []string{"trusted-signing", "Trusted Signing", "signtool", "dotnet sign"} {
		at := strings.Index(body, marker)
		if at < 0 {
			continue
		}
		// A signing job that recomputes the checksums itself is the shape
		// that works; require the recomputation to be visible beside it.
		tail := body[at:]
		if !strings.Contains(tail, "SHA256SUMS.txt") {
			t.Errorf("a signing step (%q) appears with no checksum recomputation after it; "+
				"every recorded digest would describe unsigned bytes", marker)
		}
	}
}

// The Defender pre-release check must actually run.
//
// docs/operations.md listed "Measure the artifact before release" as **done**
// and scripts/defender-release-check.ps1 existed -- but nothing invoked it.
// It was a script a person had to remember, and on 2026-09-01 nobody did:
// v0.1.89 published a Windows payload that current definitions quarantine, and
// the first anyone knew was a user unable to install.
//
// A capability documented as done and wired to nothing is the same failure as
// a launcher fix that never leaves the release page. This test is what makes
// the difference visible.
func TestTheReleaseAsksDefenderAboutWhatItIsAboutToShip(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	// The INVOCATION, not the first mention: the job carries a comment that
	// names the script, and matching that would pass while nothing ran it.
	at := strings.Index(body, "./scripts/defender-release-check.ps1 -Path")
	if at < 0 {
		t.Fatal("the release never runs scripts/defender-release-check.ps1")
	}
	// On a Windows runner, because that is the only place Defender is.
	before := body[:at]
	job := strings.LastIndex(before, "runs-on:")
	if job < 0 || !strings.Contains(body[job:job+40], "windows") {
		t.Error("the Defender check does not run on a Windows runner")
	}
	// After the binaries exist.
	if !strings.Contains(body[:at], "download-artifact") {
		t.Error("the Defender check runs before the artifacts it scans are available")
	}
	// And it must not be able to stop a release. The verdict is dated and
	// unstable by nature -- the same bytes have been clean and flagged on
	// consecutive definition builds -- so a blocking gate would have held
	// every release on 2026-09-01, when the shipped payload was flagged.
	// Knowing before deploying is the whole value; refusing to ship is not.
	tail := body[at:]
	end := len(tail)
	if next := strings.Index(tail, "\n  publish:"); next > 0 {
		end = next
	}
	if !strings.Contains(body[:at], "continue-on-error: true") && !strings.Contains(tail[:end], "continue-on-error: true") {
		t.Error("the Defender check can fail a release; a dated model verdict must not be a release gate")
	}
}
