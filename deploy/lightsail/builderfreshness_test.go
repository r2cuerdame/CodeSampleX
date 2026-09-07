package lightsail

import (
	"strings"
	"testing"
)

// Builder convergence is operationally important, but it is not a smoke
// test. A healthy, correctly identified deployment must not sit inside the
// rollback transaction for the duration of a full corpus rebuild.
func TestDeploySuccessDoesNotWaitForBuilderFreshness(t *testing.T) {
	deploy := readDeployFixture(t, "deploy.ps1")
	wrapper := readDeployFixture(t, "deploy-production.ps1")

	for _, forbidden := range []string{
		"builderFreshPollAttempts",
		"builderFreshPollSeconds",
		"collectBuilderFreshScript",
		"did not complete a fresh full builder pass",
	} {
		if strings.Contains(deploy, forbidden) {
			t.Errorf("deploy critical path still contains builder wait %q", forbidden)
		}
	}
	if strings.Contains(wrapper, `$after.builder_fresh -ne "true"`) {
		t.Fatal("production wrapper still rejects a safe deployment while builderFresh is false")
	}
	for _, required := range []string{
		`$evidence.builderFresh = $after.builder_fresh -eq "true"`,
		`$evidence.conclusion = "success"`,
		`$evidence.smoke = "pass"`,
	} {
		if !strings.Contains(wrapper, required) {
			t.Errorf("deploy evidence no longer records the lightweight success boundary: missing %q", required)
		}
	}
}

// The wait moved rather than disappeared. The observer has its own bounded
// budget and a hard failure path, so a builder that never converges is visible
// on the tracking issue without rolling back an otherwise safe deployment.
func TestPostDeployObserverAlertsWhenBuilderNeverConverges(t *testing.T) {
	observer := readDeployFixture(t, "observe-production.ps1")
	for _, required := range []string{
		`$BuilderPollAttempts = 240`,
		`$BuilderPollSeconds = 20`,
		`Start-Sleep -Seconds $BuilderPollSeconds`,
		`builder did not converge`,
		`conclusion = "failure"`,
		`Write-ObservationEvidence`,
		`server restart or exit detected during observation`,
		`server OOM detected during observation`,
		`rollback detected: configured revision returned to the previous production SHA`,
		`Representative latency after convergence`,
		`Settled unbalanced cluster rows`,
	} {
		if !strings.Contains(observer, required) {
			t.Errorf("post-deploy observer does not fail closed on convergence: missing %q", required)
		}
	}
}

func TestPostDeployObserverBoundsReplacementExitEvents(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	observer := readDeployFixture(t, "observe-production.ps1")
	for _, required := range []string{
		`--filter event=die --format '{{.Time}}'`,
		`die_event_first_epoch`,
		`die_event_last_epoch`,
	} {
		if !strings.Contains(collector, required) {
			t.Errorf("post-deploy collector cannot attribute replacement exits: missing %q", required)
		}
		if !strings.Contains(observer, `'`+required+`'`) && required != `--filter event=die --format '{{.Time}}'` {
			t.Errorf("post-deploy evidence parser omits replacement exit bound %q", required)
		}
	}
}
