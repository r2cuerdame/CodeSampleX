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
		`$ActiveBuilderLatencyRounds = 5`,
		`$MaxActiveBuilderTTFBSeconds = 10.0`,
		`$MaxPressureWaitSeconds = 3.0`,
		`Start-Sleep -Seconds $BuilderPollSeconds`,
		`builder did not converge`,
		`conclusion = "failure"`,
		`Write-ObservationEvidence`,
		`server restart or exit detected during observation`,
		`server OOM detected during observation`,
		`builder error detected during observation`,
		`rollback detected: configured revision returned to the previous production SHA`,
		`Latency while builder work was active`,
		`no latency sample was captured while builder work was active`,
		`query timeouts were observed during builder convergence`,
		`pool-busy refusals were observed during builder convergence`,
		`maximum DB-pressure wait exceeded`,
		`/golang/github.com/jackc/pgx/v5/v5.10.0`,
		`/samples/sha256:13f4bcf31db6296c4d9325831f69e508e320520ab70dd6b2d237a11557c9fe9a`,
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

func TestPostDeployObserverMeasuresTTFBDuringActiveBuilderWork(t *testing.T) {
	observer := readDeployFixture(t, "observe-production.ps1")
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")

	for _, required := range []string{
		`$sample = Read-ObservationSample $false $false`,
		`$sample.builder_active -and $evidence.activeBuilder.rounds -lt $ActiveBuilderLatencyRounds`,
		`$latencySample = Read-ObservationSample $true $false`,
		`if ($latencySample.builder_active)`,
		`Add-RouteLatencyEvidence $evidence $latencySample 'active-builder'`,
		`$sample.builder_lifecycle_state -eq 'complete'`,
		`http503Count`,
		`maxTTFBSeconds`,
	} {
		if !strings.Contains(observer, required) {
			t.Errorf("observer does not retain active-builder HTTP evidence: missing %q", required)
		}
	}
	for _, required := range []string{
		`%{time_starttransfer}`,
		`probe_latency landing /`,
		`probe_latency package /golang/github.com/jackc/pgx/v5/v5.10.0`,
		`probe_latency sample /samples/sha256:13f4bcf31db6296c4d9325831f69e508e320520ab70dd6b2d237a11557c9fe9a`,
		`latency_%s_content_valid=%s`,
		`compatibility: builder (pass start|pass complete|run:)`,
		`[ "$builder_lifecycle_before" = "$builder_lifecycle_after" ]`,
		`builder_lifecycle_state=complete`,
		`builder_lifecycle_state=error`,
		`builder_error_events=`,
		`max_pressure_wait_seconds=`,
	} {
		if !strings.Contains(collector, required) {
			t.Errorf("collector does not produce required active-builder evidence: missing %q", required)
		}
	}
	if strings.Contains(collector, `%{time_total}`) {
		t.Fatal("production latency probe reports total duration instead of TTFB")
	}
}

func TestBuilderEmitsAnObservablePassStart(t *testing.T) {
	builder := readDeployFixture(t, "../../internal/compatibility/builder.go")
	if !strings.Contains(builder, `compatibility: builder pass start full=%t since=%s`) {
		t.Fatal("post-deploy acceptance cannot prove that latency was sampled during active builder work")
	}
}
