package lightsail

import (
	"path/filepath"
	"strings"
	"testing"
)

// The footer, /version and the deploy transaction all read stamps that are
// put on the artifact at build time. The build context excludes .git, so the
// Go toolchain stamps nothing: if these arguments stop being passed, the
// production site quietly renders a development build and nothing fails.
func TestServerImageCarriesTheBuildIdentity(t *testing.T) {
	dockerfile := readDeployFixture(t, filepath.Join("..", "Dockerfile.server"))

	for _, required := range []string{
		"ARG CSX_VERSION=dev",
		"ARG CSX_BUILD_VERSION=",
		"ARG CSX_BUILT_AT=",
		"ARG CSX_ENV=development",
		"ENV CSX_VERSION=$CSX_VERSION",
		"ENV CSX_BUILD_VERSION=$CSX_BUILD_VERSION",
		"ENV CSX_BUILT_AT=$CSX_BUILT_AT",
		"ENV CSX_ENV=$CSX_ENV",
		"LABEL org.opencontainers.image.revision=$CSX_VERSION",
		"LABEL org.opencontainers.image.version=$CSX_BUILD_VERSION",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("server image no longer carries %q", required)
		}
	}
}

// An image built by a laptop or by CI must never be able to describe itself
// as production. Only the production deploy passes that value, so the
// default in the image is the one thing standing between a local build and a
// footer that claims to be the live site.
func TestServerImageDefaultsToDevelopmentNotProduction(t *testing.T) {
	dockerfile := readDeployFixture(t, filepath.Join("..", "Dockerfile.server"))
	if strings.Contains(dockerfile, "ARG CSX_ENV=production") {
		t.Error("the image defaults to production; every build would claim to be the live site")
	}
	if !strings.Contains(dockerfile, "ARG CSX_ENV=development") {
		t.Error("the image has no environment default; an unset stamp is not a deployment name")
	}
}

func TestDeployStampsTheBuildIdentityOntoTheImage(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")

	for _, required := range []string{
		`Invoke-DeployProcess git @("-C", $repo, "describe", "--tags", "--always") 10`,
		`$builtAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")`,
		`"--build-arg", "CSX_VERSION=$revision"`,
		`"--build-arg", "CSX_BUILD_VERSION=$buildVersion"`,
		`"--build-arg", "CSX_BUILT_AT=$builtAt"`,
		`"--build-arg", "CSX_ENV=production"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("deploy no longer stamps the image: missing %q", required)
		}
	}
	if !strings.Contains(script, `throw "could not determine the server build version"`) {
		t.Error("deploy ships an unstamped build version instead of refusing")
	}
}

// docker inspect proves what was configured and the image label proves what
// was built. Neither proves that the process now answering requests is the
// one that was shipped -- a container can start from the right image and
// serve an old binary that is still bound to the port.
func TestDeployVerifiesTheServedBuildNotOnlyTheConfiguredOne(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")

	probe := `served=$(docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/version |`
	if !strings.Contains(script, probe) {
		t.Fatal("the live identity check never asks the server what it is serving")
	}
	if !strings.Contains(script, `printf '%s|%s|%s|%s|%s|%s\n' "$revision" "$image" "$label" "$migration" "$served" "$started"`) {
		t.Error("the served revision is probed but not reported back")
	}
	if !strings.Contains(script, `$liveIdentityParts.Count -ne 6`) ||
		!strings.Contains(script, `$liveIdentityParts[4] -ne $revision`) {
		t.Error("the served revision is reported but not compared against the deployed revision")
	}
	// The mismatch has to enter the existing rollback path, not become a
	// warning: this check runs before the deployment is committed.
	guard := strings.Index(script, `$liveIdentityParts[4] -ne $revision`)
	fail := strings.Index(script, `throw "served SHA does not match the immutable deployment revision"`)
	if guard < 0 || fail < guard {
		t.Error("a served-revision mismatch does not throw before the deployment is committed")
	}
}

// The collector also runs against the server that is about to be replaced.
// If a missing /version failed the probe, the very deploy that introduces
// /version could never read its own pre-deploy state.
func TestProductionEvidenceToleratesAServerWithoutVersion(t *testing.T) {
	collector := readDeployFixture(t, "collect-production-evidence.sh")

	if !strings.Contains(collector, "http://127.0.0.1:8080/version") {
		t.Fatal("production evidence does not record what the server says it is serving")
	}
	if !strings.Contains(collector, `printf 'served_revision=%s\n' "$served_revision"`) {
		t.Error("the served revision never reaches the evidence file")
	}
	if !strings.Contains(collector, `if [ -z "$served_revision" ]; then served_revision=unavailable; fi`) {
		t.Error("a server without /version leaves the field empty instead of naming it unavailable")
	}
	if !strings.Contains(collector, "| head -n 1 || true)") {
		t.Error("a failed /version probe aborts the collector under set -e")
	}
}

func TestAutomaticRolloutUsesTheValidatedIdentityInsideRollbackScope(t *testing.T) {
	wrapper := readDeployFixture(t, "deploy-production.ps1")
	deploy := readDeployFixture(t, "deploy.ps1")
	if !strings.Contains(wrapper, "-DeploymentEvidence $evidence") {
		t.Error("canonical deployment does not consume the transaction's validated identity")
	}
	for _, required := range []string{
		`$DeploymentEvidence.servedRevision = $liveIdentityParts[4]`,
		`$DeploymentEvidence.deployedSha = $liveIdentityParts[0]`,
		`$DeploymentEvidence.imageDigest = $liveIdentityParts[1]`,
		`$liveIdentityParts[4] -ne $revision`,
	} {
		if !strings.Contains(deploy, required) {
			t.Errorf("deploy omits exact-SHA evidence contract %q", required)
		}
	}
	validated := strings.Index(deploy, `throw "served SHA does not match the immutable deployment revision"`)
	evidence := strings.Index(deploy, `$DeploymentEvidence.servedRevision =`)
	if evidence < 0 {
		t.Fatal("validated identity evidence missing")
	}
	committed := evidence + strings.Index(deploy[evidence:], `$serverActivationStarted = $false`)
	if validated < 0 || evidence <= validated || committed <= evidence {
		t.Fatalf("exact identity evidence escapes rollback scope: validated=%d evidence=%d committed=%d", validated, evidence, committed)
	}
	if strings.Contains(wrapper, "$after = Read-ProductionState") {
		t.Error("fallible postcommit collector can relabel a healthy deployment as failed")
	}
}
