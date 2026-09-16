package lightsail

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// realistic fixtures for GET /v1/ops/pool-metrics's locked JSON contract
// (internal/httpapi/opsmetrics.go), copied field-for-field so a rename there
// fails this test rather than production.
const (
	poolMetricsHealthy = `{"pool":{"enabled":true,"maxConns":12,"open":9,"inUse":3,"idle":6,` +
		`"classes":[{"class":"interactive","limit":6,"inUse":1,"waited":0,"busy":0,"timeouts":0,"retries":0,"suppressed":0},` +
		`{"class":"background","limit":4,"inUse":0,"waited":0,"busy":0,"timeouts":0,"retries":0,"suppressed":0}]},` +
		`"host":{"stealPercent":0.4,"loadAvg1":1.2,"sampledAt":"2026-09-16T12:00:00Z"},` +
		`"farmIngest":{"lastCommitAt":"2026-09-16T11:59:40Z","lastCommitFound":true}}`
	poolMetricsBreaching = `{"pool":{"enabled":true,"maxConns":12,"open":9,"inUse":6,"idle":0,` +
		`"classes":[{"class":"interactive","limit":6,"inUse":6,"waited":3,"busy":57,"timeouts":2,"retries":0,"suppressed":0}]},` +
		`"host":{"stealPercent":23.5,"loadAvg1":4.2,"sampledAt":"2026-09-16T12:00:05Z"},` +
		`"farmIngest":{"lastCommitFound":false}}`
	poolMetricsHostErrored = `{"pool":{"enabled":true,"maxConns":12,"open":9,"inUse":3,"idle":6,` +
		`"classes":[{"class":"interactive","limit":6,"inUse":1,"waited":0,"busy":0,"timeouts":0,"retries":0,"suppressed":0}]},` +
		`"host":{"stealPercent":0,"loadAvg1":0,"error":"host pressure sampler not configured"},` +
		`"farmIngest":{"lastCommitFound":false}}`
)

// TestSummarizePoolMetricsParsesTheLockedContract exercises the collector's
// own summarize_pool_metrics() -- the function the sample lines
// pool_metrics_status/pool_metrics_host_steal_percent/pool_metrics_host_error/
// pool_metrics_interactive_busy come from -- against fixtures shaped exactly
// like internal/httpapi/opsmetrics.go's real JSON output (#454 Task 7).
func TestSummarizePoolMetricsParsesTheLockedContract(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	program := shellFunction(t, collector, "summarize_pool_metrics") + "\nsummarize_pool_metrics\n"

	for _, tc := range []struct {
		name string
		body string
		want map[string]string
	}{
		{
			name: "healthy reading below both thresholds",
			body: poolMetricsHealthy,
			want: map[string]string{
				"pool_metrics_status":             "complete",
				"pool_metrics_host_steal_percent": "0.4",
				"pool_metrics_host_error":         "false",
				"pool_metrics_interactive_busy":   "0",
			},
		},
		{
			name: "steal and busy above threshold",
			body: poolMetricsBreaching,
			want: map[string]string{
				"pool_metrics_status":             "complete",
				"pool_metrics_host_steal_percent": "23.5",
				"pool_metrics_host_error":         "false",
				"pool_metrics_interactive_busy":   "57",
			},
		},
		{
			// host.error present must never read as "0% steal measured" --
			// the same rule internal/httpapi/opsmetrics.go and the governor's
			// own sampleHost() already state explicitly.
			name: "host error must not read as healthy",
			body: poolMetricsHostErrored,
			want: map[string]string{
				"pool_metrics_status":             "complete",
				"pool_metrics_host_steal_percent": "0",
				"pool_metrics_host_error":         "true",
				"pool_metrics_interactive_busy":   "0",
			},
		},
		{
			name: "empty body is unavailable, not zero",
			body: "",
			want: map[string]string{
				"pool_metrics_status":             "unavailable",
				"pool_metrics_host_steal_percent": "0",
				"pool_metrics_host_error":         "true",
				"pool_metrics_interactive_busy":   "0",
			},
		},
		{
			name: "malformed JSON is unavailable, not zero",
			body: `{"pool":{"classes":[{"class":"interactive"`,
			want: map[string]string{
				"pool_metrics_status":             "unavailable",
				"pool_metrics_host_steal_percent": "0",
				"pool_metrics_host_error":         "true",
				"pool_metrics_interactive_busy":   "0",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runPressureShell(t, program, tc.body)
			for field, want := range tc.want {
				if got[field] != want {
					t.Errorf("summarize_pool_metrics %s=%q, want %q (from %v)", field, got[field], want, got)
				}
			}
		})
	}
}

// TestObserverGovernorThresholdsCiteTheirSource proves
// deploy/lightsail/observe-production.ps1 copies
// cmd/csx-server/governor.go's defaultGovernorThresholds() values verbatim,
// with a comment tying the two together, and keeps the "sustained, not one
// reading" bound the brief for #454 Task 7 asked for -- so a change to
// either file that silently moves a number fails here.
func TestObserverGovernorThresholdsCiteTheirSource(t *testing.T) {
	governor := readDeployFixture(t, "../../cmd/csx-server/governor.go")
	if !strings.Contains(governor, "InteractiveRefusalRate: 0.10, HostStealPercent: 20") {
		t.Fatal("cmd/csx-server/governor.go's defaultGovernorThresholds() literal values changed; update observe-production.ps1 to match")
	}

	observer := readDeployFixture(t, "observe-production.ps1")
	for _, required := range []string{
		"$GovernorInteractiveRefusalRateThreshold = 0.10",
		"$GovernorHostStealPercentThreshold = 20",
		"$GovernorSustainedBreachSamples = 3",
		"cmd/csx-server/governor.go",
		"defaultGovernorThresholds",
		"reason=host-cpu-steal",
		"reason=interactive-pool-pressure",
		"function Update-GovernorPressureEvidence(",
		"Update-GovernorPressureEvidence $evidence $sample",
		"Update-GovernorPressureEvidence $evidence $final",
	} {
		if !strings.Contains(observer, required) {
			t.Errorf("observer is missing governor-threshold wiring: %q", required)
		}
	}
	// The two anomaly thresholds must trip on an exact "reached the Nth
	// consecutive breach" edge, not on every tick above it -- otherwise one
	// incident would append the same anomaly text on every remaining poll.
	for _, exact := range []string{
		"consecutiveHostStealBreaches -eq $GovernorSustainedBreachSamples",
		"consecutiveInteractiveBusyIncreases -eq $GovernorSustainedBreachSamples",
	} {
		if !strings.Contains(observer, exact) {
			t.Errorf("governor anomaly check is not edge-triggered: missing %q", exact)
		}
	}
}

// TestGovernorPressureEvidenceRequiresSustainedBreaches runs the real
// Update-GovernorPressureEvidence function (lifted unmodified from
// observe-production.ps1, the same technique
// TestObservationClassificationUsesActualProofPolicy in
// observation_classification_test.go already uses) against synthetic
// sequences of samples, and proves:
//   - a single breaching poll never fails the window (siblings pool_busy/
//     query_timeout are not more trigger-happy than this new check);
//   - GovernorSustainedBreachSamples consecutive breaching polls does;
//   - a gap (not-configured/unavailable) between two breaches resets the
//     streak rather than bridging it;
//   - an unconfigured token (pool_metrics_status=not-configured) never
//     fails anything, however many polls run.
func TestGovernorPressureEvidenceRequiresSustainedBreaches(t *testing.T) {
	source := readDeployFixture(t, "observe-production.ps1")
	start := strings.Index(source, "function Update-GovernorPressureEvidence(")
	end := strings.Index(source, "function Add-RouteLatencyEvidence(")
	if start < 0 || end <= start {
		t.Fatal("cannot locate the real Update-GovernorPressureEvidence function")
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
		"$GovernorHostStealPercentThreshold = 20\n" +
		"$GovernorInteractiveRefusalRateThreshold = 0.10\n" +
		"$GovernorSustainedBreachSamples = 3\n" +
		source[start:end] + `
function New-Evidence {
    return @{anomalies=[Collections.Generic.List[string]]::new();governor=@{
        configured=$false;observed=$false;maxHostStealPercent=$null;consecutiveHostStealBreaches=0
        maxConsecutiveHostStealBreaches=0;lastInteractiveBusy=$null;consecutiveInteractiveBusyIncreases=0
        maxConsecutiveInteractiveBusyIncreases=0}}
}
function New-Sample([string]$status,[double]$steal,[bool]$hostError,[int]$busy) {
    return @{pool_metrics_status=$status;pool_metrics_host_steal_percent=$steal;pool_metrics_host_error=$hostError;pool_metrics_interactive_busy=$busy}
}
function Count-GovernorAnomalies($e) { ($e.anomalies | Where-Object { $_ -match 'reason=' }).Count }

# One breaching poll never fails the window.
$e = New-Evidence
Update-GovernorPressureEvidence $e (New-Sample 'complete' 25.0 $false 0)
if ((Count-GovernorAnomalies $e) -ne 0) { throw 'a single steal breach failed the window' }

# Three consecutive breaching polls does.
$e = New-Evidence
1..3 | ForEach-Object { Update-GovernorPressureEvidence $e (New-Sample 'complete' 25.0 $false 0) }
if ((Count-GovernorAnomalies $e) -ne 1) { throw "three consecutive steal breaches did not fail exactly once: $(Count-GovernorAnomalies $e)" }
if (($e.anomalies | Where-Object { $_ -match 'reason=host-cpu-steal' }).Count -ne 1) { throw 'steal anomaly missing reason=host-cpu-steal' }

# A fourth breaching poll must not append a second anomaly for the same incident.
Update-GovernorPressureEvidence $e (New-Sample 'complete' 25.0 $false 0)
if ((Count-GovernorAnomalies $e) -ne 1) { throw 'governor anomaly is not edge-triggered' }

# A gap resets the streak instead of bridging it.
$e = New-Evidence
Update-GovernorPressureEvidence $e (New-Sample 'complete' 25.0 $false 0)
Update-GovernorPressureEvidence $e (New-Sample 'unavailable' 0 $true 0)
Update-GovernorPressureEvidence $e (New-Sample 'complete' 25.0 $false 0)
Update-GovernorPressureEvidence $e (New-Sample 'complete' 25.0 $false 0)
if ((Count-GovernorAnomalies $e) -ne 0) { throw 'a gap did not reset the consecutive-breach streak' }

# host.error must never read as a healthy 0% -- it resets the streak too.
$e = New-Evidence
1..5 | ForEach-Object { Update-GovernorPressureEvidence $e (New-Sample 'complete' 0 $true 0) }
if ((Count-GovernorAnomalies $e) -ne 0) { throw 'a host.error reading was treated as healthy steal' }

# Three consecutive interactive-busy increases fails, with the other reason string.
$e = New-Evidence
Update-GovernorPressureEvidence $e (New-Sample 'complete' 0.0 $false 10)
Update-GovernorPressureEvidence $e (New-Sample 'complete' 0.0 $false 20)
Update-GovernorPressureEvidence $e (New-Sample 'complete' 0.0 $false 30)
Update-GovernorPressureEvidence $e (New-Sample 'complete' 0.0 $false 40)
if ((Count-GovernorAnomalies $e) -ne 1) { throw "three consecutive busy increases did not fail exactly once: $(Count-GovernorAnomalies $e)" }
if (($e.anomalies | Where-Object { $_ -match 'reason=interactive-pool-pressure' }).Count -ne 1) { throw 'busy anomaly missing reason=interactive-pool-pressure' }

# A busy counter that stops rising resets the streak (mirrors the pool
# reopening/monotoneDelta guard governor.go itself applies).
$e = New-Evidence
Update-GovernorPressureEvidence $e (New-Sample 'complete' 0.0 $false 10)
Update-GovernorPressureEvidence $e (New-Sample 'complete' 0.0 $false 20)
Update-GovernorPressureEvidence $e (New-Sample 'complete' 0.0 $false 20)
Update-GovernorPressureEvidence $e (New-Sample 'complete' 0.0 $false 30)
if ((Count-GovernorAnomalies $e) -ne 0) { throw 'a flat reading did not reset the busy-increase streak' }

# Not-configured (no admin token yet) never fails, however many polls run.
$e = New-Evidence
1..10 | ForEach-Object { Update-GovernorPressureEvidence $e (New-Sample 'not-configured' 99.0 $false 999) }
if ((Count-GovernorAnomalies $e) -ne 0) { throw 'an unconfigured token produced a governor anomaly' }
if ($e.governor.observed) { throw 'an unconfigured token was recorded as observed' }
'ok'
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command", program)
	output, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Fatalf("governor pressure evidence: err=%v output=%s", err, output)
	}
}
