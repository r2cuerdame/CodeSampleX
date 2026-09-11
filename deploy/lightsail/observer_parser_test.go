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

// Execute the shipped wire parser without starting SSH. Cheap samples leave
// detail blank; unknowns must stay null and attribution must fail closed.
func TestObservationWireParserKeepsUnknownDetailAndRejectsInvalidAttribution(t *testing.T) {
	source := strings.ReplaceAll(readDeployFixture(t, "observe-production.ps1"), "\r\n", "\n")
	section := func(start, end string) string {
		t.Helper()
		from := strings.Index(source, start)
		if from < 0 {
			t.Fatalf("parser section missing: %s", start)
		}
		to := strings.Index(source[from:], end)
		if to < 0 {
			t.Fatalf("parser section end missing: %s", end)
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
		section("function Convert-Percent(", "function Update-PressureEvidence(") +
		"\nfunction Parse-Fixture([string]$stdout) {\n$IncludeExtended=$false\n$IncludeLatency=$false\n" +
		section("        $state = [ordered]@{}", "    } finally {") + "\n}\n" + `
$base = [ordered]@{
    observed_at='2026-09-12T14:31:00Z';revision=('a' * 40);image_digest=('sha256:' + ('b' * 64))
    image_revision=('a' * 40);migration_version='0052_fixture.sql';health='ok';served_revision=('a' * 40)
    server_started_at='2026-09-12T13:00:00Z';restart_count='0';oom_killed='false';container_status='running'
    builder_generated_at='2026-09-12T14:29:00Z';builder_fresh='true';builder_active='false';builder_lifecycle_state='complete'
    builder_error_events='1';builder_error_events_before_observation='1';builder_error_events_during_observation='0'
    builder_error_window_status='complete';pressure_window_status='complete'
    window_pressure_lines='0';window_pool_busy_events='0';window_query_timeout_events='0';window_max_pressure_wait_seconds='0.000000000'
    cpu_percent='1.2';memory_usage='42MiB / 1GiB';memory_percent='4.2';load_average='0.1 0.2 0.3';detail_collected='false'
    pressure_lines='0';pool_busy_events='0';query_timeout_events='0';pool_busy_event_total='0';query_timeout_event_total='0'
    admission_refused_event_total='0';deferred_refused_event_total='0';max_pressure_wait_seconds='0.000000'
    oom_events='0';restart_events='0';die_events='0';die_event_first_epoch='0';die_event_last_epoch='0'
    settled_fail_observations='';settled_failure_cluster_observations='';settled_unbalanced_failure_cluster_rows=''
    settled_failure_cluster_rows_examined='';settled_source_rows_examined='';settled_invariant_exit_code='';settled_invariant_seconds=''
    settled_invariant_status='not-collected';settled_invariant_row_limit='10000';settled_source_row_limit='10000';settled_invariant_json_byte_limit='1048576'
}
function New-Fixture {
    $copy=[ordered]@{}
    foreach($key in $base.Keys) { $copy[$key]=$base[$key] }
    return $copy
}
function To-Wire($fields) {
    return (($fields.Keys | ForEach-Object { "$_=$($fields[$_])" }) -join [char]10)
}
function Assert-Rejected([string]$wire, [string]$reason) {
    $rejected=$false
    try { $null=Parse-Fixture $wire } catch { $rejected=$true }
    if(-not $rejected) { throw "parser accepted $reason" }
}
$parsed=Parse-Fixture (To-Wire (New-Fixture))
foreach($key in @('settled_fail_observations','settled_failure_cluster_observations','settled_unbalanced_failure_cluster_rows',
                 'settled_failure_cluster_rows_examined','settled_source_rows_examined','settled_invariant_exit_code','settled_invariant_seconds')) {
    if($null -ne $parsed[$key]) { throw "cheap unknown $key was fabricated" }
}
if($parsed.detail_collected -isnot [bool] -or $parsed.detail_collected -or $parsed.builder_fresh -ne $true -or
   $parsed.builder_error_events -isnot [long] -or $parsed.window_max_pressure_wait_seconds -ne 0) {
    throw 'cheap sample types are incorrect'
}
$detail=New-Fixture
$detail.detail_collected='true';$detail.settled_invariant_status='complete'
$detail.settled_failure_cluster_observations='400';$detail.settled_unbalanced_failure_cluster_rows='0'
$detail.settled_failure_cluster_rows_examined='20';$detail.settled_source_rows_examined='0'
$detail.settled_invariant_exit_code='0';$detail.settled_invariant_seconds='4'
$parsed=Parse-Fixture (To-Wire $detail)
if($null -ne $parsed.settled_fail_observations -or $parsed.settled_failure_cluster_observations -ne 400 -or
   $parsed.settled_invariant_exit_code -isnot [long] -or $parsed.settled_invariant_seconds -ne 4 -or -not $parsed.detail_collected) {
    throw 'covered current-ledger detail fabricated a source total or lost diagnostic integers'
}
foreach($key in @('builder_error_events_before_observation','builder_error_events_during_observation','builder_error_window_status',
    'pressure_window_status','window_pressure_lines','window_pool_busy_events','window_query_timeout_events','window_max_pressure_wait_seconds',
    'settled_invariant_status','settled_invariant_row_limit','settled_source_row_limit','settled_invariant_json_byte_limit',
    'settled_failure_cluster_rows_examined','settled_source_rows_examined','settled_invariant_exit_code','settled_invariant_seconds')) {
    $sample=New-Fixture;$sample.Remove($key)
    Assert-Rejected (To-Wire $sample) "missing $key"
}
foreach($key in @('builder_error_window_status','pressure_window_status','settled_invariant_status')) {
    foreach($invalid in @('pass','', 'unknown')) {
        $sample=New-Fixture;$sample[$key]=$invalid
        Assert-Rejected (To-Wire $sample) "invalid $key=$invalid"
    }
}
foreach($key in @('window_pressure_lines','window_pool_busy_events','window_query_timeout_events','window_max_pressure_wait_seconds',
    'builder_error_events_before_observation','builder_error_events_during_observation','settled_invariant_row_limit',
    'settled_source_row_limit','settled_invariant_json_byte_limit','settled_failure_cluster_rows_examined',
    'settled_source_rows_examined','settled_invariant_exit_code','settled_invariant_seconds')) {
    foreach($invalid in @('-1','NaN')) {
        $sample=New-Fixture;$sample[$key]=$invalid
        Assert-Rejected (To-Wire $sample) "malformed $key=$invalid"
    }
}
$sample=New-Fixture;$sample.builder_error_events_during_observation='1'
Assert-Rejected (To-Wire $sample) 'historical + during count mismatch'
$wire=To-Wire (New-Fixture)
Assert-Rejected ($wire + [char]10 + 'pressure_window_status=complete') 'duplicate key'
Assert-Rejected ($wire + [char]10 + 'not-a-key-value') 'malformed wire line'
'ok'
`
	path := filepath.Join(t.TempDir(), "observer-parser.ps1")
	if err := os.WriteFile(path, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-File", path).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Fatalf("actual observer parser: err=%v output=%s", err, output)
	}
}
