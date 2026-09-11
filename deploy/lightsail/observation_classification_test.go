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

func TestObservationClassificationUsesActualProofPolicy(t *testing.T) {
	source := readDeployFixture(t, "observe-production.ps1")
	start := strings.Index(source, "function Add-ExtendedObservationEvidence(")
	end := strings.Index(source, "function Get-StateAnomalies(")
	if start < 0 || end <= start {
		t.Fatal("cannot locate actual observation classification functions")
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
	program := "$ErrorActionPreference='Stop'\n" + source[start:end] + `
$ExpectedRevision='a' * 40
$ExpectedPreviousRevision='d' * 40
$ExpectedImageDigest='sha256:' + ('b' * 64)
$ExpectedServerStartedAt='2026-09-09T00:00:00Z'
$identity="${ExpectedRevision}|${ExpectedImageDigest}|${ExpectedServerStartedAt}|${ExpectedRevision}|"
function New-Evidence {
    return @{anomalies=[Collections.Generic.List[string]]::new();findings=[Collections.Generic.List[object]]::new();sourceContinuity=@{status='not-assessed';reason='baseline unavailable'}}
}
function New-Sample {
    return @{
        identity_before=$identity;identity_after=$identity
        observation_started_at='2026-09-09T00:00:01.000000000Z'
        privacy_preflight='pass';privacy_live='pass';public_surface='pass';admin_state='pass';activity_state='pass';detail_status='pass'
        detail_invariants='{"pass":90,"fail":20,"publishedSamples":4,"failureClusterObservations":20098,"unbalancedFailureClusterRows":0,"pgxParseConfigPass":2,"pgxParseConfigFail":1}'
        detail_failure_evidence_quality='{"available":true,"fail":20,"complete":10,"partial":4,"missing":3,"legacyEvidenceIncomplete":3}'
    }
}
function Assert-Decision($evidence,$classification,$rollback) {
    Set-ObservationClassification $evidence
    if ($evidence.classification -ne $classification -or $evidence.rollbackRequested -ne $rollback) {
        throw "wrong classification: $($evidence | ConvertTo-Json -Depth 8 -Compress)"
    }
}
foreach ($anomaly in @('latency exceeded','HTTP 503','convergence delay','observation probe failed before completion','configured revision drifted from the deployed SHA','server OOM detected during observation')) {
    $e=New-Evidence; $e.anomalies.Add($anomaly); Assert-Decision $e 'incident-only' $false
}
foreach ($check in @('privacy_preflight','privacy_live','public_surface','admin_state','activity_state','detail_status')) {
    $e=New-Evidence; $s=New-Sample; $s[$check]='unavailable'
    Add-ExtendedObservationEvidence $e $s; Assert-Decision $e 'incident-only' $false
}
$e=New-Evidence; $s=New-Sample
Add-ExtendedObservationEvidence $e $s
if ($e.observationWindowStartedAt -cne $s.observation_started_at) { throw 'remote observation boundary was not retained' }
$s.detail_invariants=$s.detail_invariants.Replace('20098','20096')
Add-ExtendedObservationEvidence $e $s
Assert-Decision $e 'none' $false # Derived reconciliation is not source data loss.
foreach ($boundary in @('', 'malformed', '2026-09-09T00:00:01Z')) {
    $e=New-Evidence; $s=New-Sample; $s.observation_started_at=$boundary
    Add-ExtendedObservationEvidence $e $s; Assert-Decision $e 'incident-only' $false
    if ($e.observationWindowStartedAt) { throw 'unproven boundary was retained' }
}
foreach ($bad in @('malformed','{"fail":20}')) {
    $e=New-Evidence; $s=New-Sample; $s.detail_invariants=$bad
    Add-ExtendedObservationEvidence $e $s; Assert-Decision $e 'incident-only' $false
}
$e=New-Evidence; $s=New-Sample
$s.detail_failure_evidence_quality=$s.detail_failure_evidence_quality.Replace('"complete":10','"complete":9')
Add-ExtendedObservationEvidence $e $s; Assert-Decision $e 'incident-only' $false
$e=New-Evidence; $s=New-Sample
$s.detail_invariants=$s.detail_invariants.Replace('"unbalancedFailureClusterRows":0','"unbalancedFailureClusterRows":1')
Add-ExtendedObservationEvidence $e $s; Assert-Decision $e 'incident-only' $false
foreach ($exact in @($true,$false)) {
    $e=New-Evidence; $s=New-Sample; $s.privacy_live='violation';$s.privacy_live_probe_id='c' * 32
    if (-not $exact) { $s.identity_after='different activation' }
    Add-ExtendedObservationEvidence $e $s
    if ($exact) { Assert-Decision $e 'security-critical' $true }
    else { Assert-Decision $e 'incident-only' $false }
}
$e=New-Evidence; $s=New-Sample; $s.privacy_live='violation' # No unique probe proof.
Add-ExtendedObservationEvidence $e $s; Assert-Decision $e 'incident-only' $false
foreach ($badProof in @('false',$false)) {
    $e=New-Evidence; $e.anomalies.Add('unproven severity label')
    $e.findings.Add(@{code='privacy-synthetic-marker-recorded';classification='security-critical';proven=$badProof;identityVerified=$true;evidence='claimed'})
    Assert-Decision $e 'incident-only' $false
}
$baselinePath=Join-Path $env:CSX_OBSERVATION_TEST_DIR 'baseline.json'
$e=New-Evidence
Add-SourceContinuityEvidence $e $baselinePath
if ($e.sourceContinuity.status -ne 'not-assessed') { throw 'missing baseline fabricated continuity' }
foreach ($priorPass in @(90,91)) {
    $e=New-Evidence; $s=New-Sample; Add-ExtendedObservationEvidence $e $s
    $counts=$s.detail_invariants | ConvertFrom-Json; $counts.pass=$priorPass
    @{targetSha=$ExpectedPreviousRevision;baselineDeploymentRunId='123';invariants=$counts} |
        ConvertTo-Json -Depth 8 | Set-Content $baselinePath
    Add-SourceContinuityEvidence $e $baselinePath
    if ($priorPass -eq 91) {
        if ($e.sourceContinuity.status -ne 'decrease-observed') { throw 'source decrease went undetected' }
        Assert-Decision $e 'incident-only' $false
    } else {
        if ($e.sourceContinuity.status -ne 'no-decrease-observed') { throw 'valid baseline not compared' }
        Assert-Decision $e 'none' $false
    }
}
$e=New-Evidence; $s=New-Sample; Add-ExtendedObservationEvidence $e $s
'{"targetSha":"wrong deployment","baselineDeploymentRunId":"123"}' | Set-Content $baselinePath
Add-SourceContinuityEvidence $e $baselinePath
if ($e.sourceContinuity.status -ne 'not-assessed') { throw 'unrelated baseline trusted' }
Assert-Decision $e 'none' $false
'ok'
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command", program)
	cmd.Env = append(os.Environ(), "CSX_OBSERVATION_TEST_DIR="+t.TempDir())
	output, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Fatalf("actual observation classification: err=%v output=%s", err, output)
	}
}

func TestObservationPhaseHasIndependentBudgetsAndNoProductionMutation(t *testing.T) {
	observer := readDeployFixture(t, "observe-production.ps1")
	for _, required := range []string{
		"$ExtendedObservationSeconds = 600", "$SampleTimeoutSeconds = 180", "$BuilderWindowSeconds = 4800",
		"$process.WaitForExit($remainingMs)", "$writeTask.Wait($transportLimitMs)", "$process.Kill($true)",
		"phase=observation-", "elapsed=", "ceiling=", "$builderAnomalyStart = $evidence.anomalies.Count",
		"$evidence.anomalies.Count -gt $builderAnomalyStart", "Set-ObservationClassification $evidence",
	} {
		if !strings.Contains(observer, required) {
			t.Errorf("missing observation boundary %q", required)
		}
	}
	if strings.Index(observer, "$extendedSample = Read-ObservationSample") > strings.Index(observer, "for ($attempt = 1;") {
		t.Fatal("extended checks wait for convergence")
	}
	helper := readDeployFixture(t, "collect-extended-observation.sh")
	for _, forbidden := range []string{"docker compose up", "docker compose down", "caddy reload", "TRUNCATE ", "DELETE FROM "} {
		if strings.Contains(helper, forbidden) {
			t.Errorf("observer includes production mutation %q", forbidden)
		}
	}
}

func TestLivePrivacyProofRequiresPositiveUniqueMarkerMatch(t *testing.T) {
	helper := readDeployFixture(t, "collect-extended-observation.sh")
	marker := "docker compose exec -T caddy sh -c '\n    log=/var/log/caddy-safe/access-safe.log"
	start := strings.Index(helper, marker)
	if start < 0 {
		t.Fatal("live privacy proof shell is missing")
	}
	program := helper[start+len("docker compose exec -T caddy sh -c '\n"):]
	end := strings.Index(program, "\n  ' sh \"$marker\"")
	if end < 0 {
		t.Fatal("live privacy proof shell boundary is missing")
	}
	program = strings.ReplaceAll(program[:end], "log=/var/log/caddy-safe/access-safe.log", `log="$CSX_TEST_LOG"`)
	sh, err := exec.LookPath("sh")
	if err != nil && runtime.GOOS == "windows" {
		sh, err = exec.LookPath(`C:\Program Files\Git\bin\sh.exe`)
	}
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	for _, tc := range []struct {
		name, body, requestFailed, want string
		missing, grepError              bool
	}{
		{name: "proven leak despite response timeout", body: "synthetic-unique-marker", requestFailed: "1", want: "violation"},
		{name: "transport failure without leak", body: "safe aggregate", requestFailed: "1", want: "unavailable"},
		{name: "missing log", requestFailed: "0", missing: true},
		{name: "grep read error", body: "safe aggregate", requestFailed: "0", grepError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "log")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			actual := program
			if tc.grepError {
				actual = "grep() { return 2; }\n" + actual
			}
			if runtime.GOOS == "windows" {
				actual = "PATH='/usr/bin':$PATH\n" + actual
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, sh, "-c", actual, "sh", "synthetic-unique-marker", tc.requestFailed)
			cmd.Env = append(os.Environ(), "CSX_TEST_LOG="+filepath.ToSlash(path))
			output, err := cmd.CombinedOutput()
			if strings.TrimSpace(string(output)) != tc.want || (err != nil) != (tc.missing || tc.grepError) {
				t.Fatalf("privacy proof: output=%q err=%v want=%q", output, err, tc.want)
			}
		})
	}
}
