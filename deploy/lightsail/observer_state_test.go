package lightsail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Exercise the actual controller, including its terminal probe and finally
// block. Only the SSH transport and evidence writer are replaced; production
// acceptance thresholds and state transitions remain the source under test.
func TestObserverStateMachineRequiresMeasuredActiveAndSettledEvidence(t *testing.T) {
	source := readDeployFixture(t, "observe-production.ps1")
	section := func(start, end string) string {
		t.Helper()
		from := strings.Index(source, start)
		if from < 0 {
			t.Fatalf("observer section start missing: %s", start)
		}
		if end == "" {
			return source[from:]
		}
		to := strings.Index(source[from:], end)
		if to < 0 {
			t.Fatalf("observer section end missing: %s", end)
		}
		return source[from : from+to]
	}
	ps := os.Getenv("CSX_TEST_PWSH")
	if ps == "" {
		ps, _ = exec.LookPath("pwsh")
	}
	if ps == "" && runtime.GOOS == "windows" {
		ps, _ = exec.LookPath("powershell")
	}
	if ps == "" {
		t.Skip("PowerShell unavailable")
	}
	program := "$ErrorActionPreference='Stop'\n" +
		section("$BuilderPollAttempts =", "$repo =") +
		section("$latencyPaths =", "foreach ($sha") +
		section("function Add-ExtendedObservationEvidence(", "function Write-ObservationEvidence(") +
		"\n$observerBody = {\n" + section("$evidence = [ordered]@{", "") + "\n}\n" + `
$ExpectedRevision='a' * 40
$ExpectedPreviousRevision='d' * 40
$ExpectedImageDigest='sha256:' + ('b' * 64)
$ExpectedServerStartedAt='2026-09-09T00:00:00Z'
$expectedMigration='0052_fixture.sql'
$DeploymentRunId='123'
$ObservationControllerSha='c' * 40
$DeploymentOperationalSha='e' * 40
$TrackingIssue='#174'
$EvidencePath='fixture-evidence.json'
$SummaryPath='fixture-summary.md'
$BaselinePath=''
# Simulated transport is immediate; shorten only the polling count and sleep.
# Keep the production five-round, TTFB and pressure acceptance thresholds.
$BuilderPollAttempts=8
$BuilderPollSeconds=0

function New-ExtendedSample {
    $identity="${ExpectedRevision}|${ExpectedImageDigest}|${ExpectedServerStartedAt}|${ExpectedRevision}|"
    return @{
        identity_before=$identity;identity_after=$identity
        observation_started_at='2026-09-09T00:00:01.000000000Z'
        privacy_preflight='pass';privacy_live='pass';public_surface='pass';admin_state='pass';activity_state='pass';detail_status='pass'
        detail_invariants='{"pass":90,"fail":20,"publishedSamples":4,"failureClusterObservations":100,"unbalancedFailureClusterRows":0,"pgxParseConfigPass":2,"pgxParseConfigFail":1}'
        detail_failure_evidence_quality='{"available":true,"fail":20,"complete":10,"partial":4,"missing":3,"legacyEvidenceIncomplete":3}'
    }
}
function New-ObservationSample {
    $sample=@{
        revision=$ExpectedRevision;image_revision=$ExpectedRevision;served_revision=$ExpectedRevision
        image_digest=$ExpectedImageDigest;migration_version=$expectedMigration;health='ok';container_status='running'
        server_started_at=$ExpectedServerStartedAt;observed_at='2026-09-09T00:10:00Z';restart_count=0
        restart_events=0;die_events=0;oom_events=0;oom_killed=$false
        builder_fresh=$true;builder_active=$false;builder_lifecycle_state='complete';builder_generated_at='2026-09-09T00:05:00Z'
        builder_error_events=1;builder_error_events_before_observation=1;builder_error_events_during_observation=0;builder_error_window_status='complete'
        cpu_percent='10';memory_percent='25';load_average='0.1 0.2 0.3';detail_collected=$false
        # Historical lifetime pressure remains visible, but is not assigned to
        # the independently authenticated quiet observation window.
        pressure_lines=7;pool_busy_events=2;query_timeout_events=3;pool_busy_event_total=20;query_timeout_event_total=30
        admission_refused_event_total=4;deferred_refused_event_total=5;max_pressure_wait_seconds=19
        pressure_window_status='complete';window_pressure_lines=0;window_pool_busy_events=0
        window_query_timeout_events=0;window_max_pressure_wait_seconds=0
        settled_invariant_status='not-collected';settled_invariant_row_limit=250000;settled_source_row_limit=10000;settled_invariant_json_byte_limit=4096
        settled_invariant_exit_code=$null;settled_invariant_seconds=$null
        settled_failure_cluster_rows_examined=$null;settled_source_rows_examined=$null
        settled_fail_observations=$null;settled_failure_cluster_observations=$null;settled_unbalanced_failure_cluster_rows=$null
    }
    foreach ($name in $latencyPaths.Keys) {
        $sample["latency_${name}_status"]='200'
        $sample["latency_${name}_ttfb_seconds"]='0.25'
        $sample["latency_${name}_content_valid"]=$true
    }
    switch ($script:scenarioMode) {
        'new-builder-error' { $sample.builder_error_events=2; $sample.builder_error_events_during_observation=1 }
        'unknown-builder-window' { $sample.builder_error_window_status='unavailable' }
        'unknown-pressure-window' { $sample.pressure_window_status='unavailable' }
        'new-query-timeout' { $sample.window_query_timeout_events=1; $sample.window_pressure_lines=1 }
        'new-pool-refusal' { $sample.window_pool_busy_events=1; $sample.window_pressure_lines=1 }
        'new-pressure-wait' { $sample.window_max_pressure_wait_seconds=3.01 }
    }
    return $sample
}
function Read-ObservationSample([bool]$IncludeLatency, [bool]$IncludeDetail, [bool]$IncludeExtended=$false, [int]$TimeoutSeconds=180) {
    if ($IncludeExtended) { return New-ExtendedSample }
    if ($script:scenarioMode -eq 'unmeasured') { throw 'fixture transport failed before the first lifecycle sample' }
    $sample=New-ObservationSample
    if ($IncludeDetail) {
        $script:terminalProbes++
        if (-not $IncludeLatency) { throw 'terminal settled sample omitted independent latency' }
        if ($script:scenarioMode -eq 'detail-transport-failure') { throw 'fixture terminal transport failure' }
        $sample.detail_collected=$true
        $sample.settled_invariant_status='complete'
        $sample.settled_failure_cluster_rows_examined=2
        $sample.settled_source_rows_examined=0
        $sample.settled_invariant_exit_code=0; $sample.settled_invariant_seconds=1
        $sample.settled_failure_cluster_observations=100
        $sample.settled_unbalanced_failure_cluster_rows=0
        switch ($script:scenarioMode) {
            'settled-budget-exceeded' {
                $sample.settled_invariant_status='budget-exceeded'
                $sample.settled_failure_cluster_rows_examined=250001
                $sample.settled_failure_cluster_observations=$null
                $sample.settled_unbalanced_failure_cluster_rows=$null
            }
            'settled-unavailable' {
                $sample.settled_invariant_status='unavailable'
                $sample.settled_failure_cluster_rows_examined=$null
                $sample.settled_failure_cluster_observations=$null
                $sample.settled_unbalanced_failure_cluster_rows=$null
            }
            'settled-missing-source-proof' { $sample.settled_failure_cluster_observations=0 }
            'settled-empty-with-fail' { $sample.settled_failure_cluster_observations=0; $sample.settled_fail_observations=1 }
            'settled-unbalanced' { $sample.settled_unbalanced_failure_cluster_rows=1 }
            'terminal-started-another-pass' { $sample.builder_lifecycle_state='start'; $sample.builder_active=$true }
        }
        return $sample
    }
    if ($IncludeLatency) {
        $script:latencyProbes++
        if ($script:scenarioMode -ne 'raced-latency') {
            $sample.builder_active=$true
            $sample.builder_lifecycle_state='start'
        }
    } else {
        $script:lifecycleProbes++
        if ($script:lifecycleProbes -le $script:providedActiveRounds) {
            $sample.builder_active=$true
            $sample.builder_lifecycle_state='start'
        }
    }
    return $sample
}
function Write-ObservationEvidence([Collections.IDictionary]$Evidence) { $script:writes++ }
function Invoke-Scenario([int]$rounds, [string]$mode='healthy') {
    $script:providedActiveRounds=$rounds
    $script:scenarioMode=$mode
    $script:lifecycleProbes=0; $script:latencyProbes=0; $script:terminalProbes=0; $script:writes=0
    $collectorBytes=[byte[]]@(1,2); $extendedBytes=[byte[]]@(3,4)
    $caught=$false
    try { $null = . $observerBody } catch { $caught=$true }
    if ($script:writes -ne 1) { throw "$mode did not retain exactly one terminal evidence record" }
    if ($caught -ne ($evidence.conclusion -ne 'success')) { throw "$mode final throw disagrees with evidence conclusion" }
    if ($evidence.rollbackRequested) { throw "$mode fabricated rollback proof" }
    if (($collectorBytes | Measure-Object -Sum).Sum -ne 0 -or ($extendedBytes | Measure-Object -Sum).Sum -ne 0) {
        throw "$mode skipped terminal transport buffer cleanup"
    }
    return $evidence
}
function Assert-Failure($evidence, [string]$message) {
    if ($evidence.conclusion -ne 'failure' -or $evidence.classification -ne 'incident-only') {
        throw "$message unexpectedly accepted: $($evidence | ConvertTo-Json -Depth 8 -Compress)"
    }
}
function Assert-Anomaly($evidence, [string]$pattern) {
    if (@($evidence.anomalies | Where-Object { $_ -like $pattern }).Count -eq 0) {
        throw "missing anomaly $pattern : $($evidence.anomalies -join '; ')"
    }
}

foreach ($rounds in @(0,1,4)) {
    $e=Invoke-Scenario $rounds
    Assert-Failure $e "$rounds active rounds"
    Assert-Anomaly $e 'insufficient active-builder latency rounds:*'
    if ($e.activeBuilder.rounds -ne $rounds -or $e.converged -or $null -ne $e.builderCompletionSeconds) {
        throw "$rounds rounds fabricated active convergence or completion"
    }
    if ($e.activeBuilder.status -ne 'insufficient-evidence-awaiting-natural-pass') { throw 'missing fail-closed natural-pass status' }
    if ($e.activeBuilder.samples.Count -ne ($rounds * $latencyPaths.Count)) { throw 'active sample count was fabricated' }
    if ($e.settledInvariant.status -ne 'complete' -or $e.latencies.Count -ne $latencyPaths.Count -or $script:terminalProbes -ne 1) {
        throw 'missing independent settled evidence when active acceptance fails'
    }
    if ($script:lifecycleProbes -ne $BuilderPollAttempts) { throw 'settled samples bypassed the bounded natural-pass wait' }
    if ($rounds -eq 0 -and ($e.activeBuilder.observed -or $e.activeBuilder.requests -ne 0 -or $null -ne $e.activeBuilder.maxTTFBSeconds)) {
        throw 'already-complete builder invented active latency measurements'
    }
}
$e=Invoke-Scenario 5
if ($e.conclusion -ne 'success' -or -not $e.converged -or $e.activeBuilder.rounds -ne 5 -or
    $e.activeBuilder.status -ne 'measured' -or $e.activeBuilder.samples.Count -ne (5 * $latencyPaths.Count)) {
    throw "five real active rounds followed by completion did not earn acceptance: $($e.anomalies -join '; ')"
}
if ($e.events.builderError -ne 1 -or $e.events.builderErrorBeforeObservation -ne 1 -or $e.events.builderErrorDuringObservation -ne 0) {
    throw 'historical builder error was cleared or assigned to the new window'
}
if ($e.pressure.queryTimeoutEvents -ne 3 -or $e.pressure.poolBusyEvents -ne 2 -or $e.pressure.maxWaitSeconds -ne 19 -or
    $e.pressure.windowQueryTimeoutEvents -ne 0 -or $e.pressure.windowPoolBusyEvents -ne 0 -or $e.pressure.windowMaxWaitSeconds -ne 0 -or
    -not $e.pressure.measured -or -not $e.pressure.windowMeasured) {
    throw 'historical pressure was cleared, mislabeled as new, or not measured'
}
if ($null -ne $e.settledInvariant.failObservations -or $e.settledInvariant.sourceRowsExamined -ne 0) {
    throw 'bounded nonempty cluster proof invented an unmeasured source total'
}

foreach ($mode in @('settled-budget-exceeded','settled-unavailable','settled-missing-source-proof','settled-empty-with-fail','settled-unbalanced','terminal-started-another-pass')) {
    $e=Invoke-Scenario 5 $mode
    Assert-Failure $e $mode
    if (-not $e.converged -or $e.activeBuilder.rounds -ne 5) { throw "$mode lost actually measured active evidence" }
}
$e=Invoke-Scenario 5 'raced-latency'
Assert-Failure $e 'builder completed during latency probes'
if ($e.activeBuilder.rounds -ne 0 -or $e.activeBuilder.observed -or $e.converged) { throw 'raced latency was admitted as active work' }

foreach ($case in @(
    @('new-builder-error','builder error detected during observation'),
    @('unknown-builder-window','builder error observation window could not be authenticated'),
    @('unknown-pressure-window','DB-pressure observation window could not be authenticated'),
    @('new-query-timeout','query timeouts were observed during builder convergence*'),
    @('new-pool-refusal','pool-busy refusals were observed during builder convergence*'),
    @('new-pressure-wait','maximum DB-pressure wait exceeded*')
)) {
    $e=Invoke-Scenario 5 $case[0]
    Assert-Failure $e $case[0]
    Assert-Anomaly $e $case[1]
}
$e=Invoke-Scenario 5 'detail-transport-failure'
Assert-Failure $e 'terminal collection failed'
if (-not $e.converged -or $e.pressure.measured -or $e.events.measured -or $e.settledInvariant.status -ne 'not-collected') {
    throw 'failed detailed collection fabricated measured lifetime evidence or lost active proof'
}
$e=Invoke-Scenario 0 'unmeasured'
Assert-Failure $e 'transport never collected pressure'
if ($e.pressure.measured -or $e.pressure.windowMeasured -or $e.events.measured) { throw 'unmeasured zero defaults claimed event proof' }
'ok'
`
	// Use a file rather than a command-line argument: Windows PowerShell 5 has
	// a smaller command-line limit than the extracted production state machine.
	path := filepath.Join(t.TempDir(), "observer-state.ps1")
	if err := os.WriteFile(path, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path)
	output, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Fatalf("actual observer state machine: err=%v output=%s", err, output)
	}
}
