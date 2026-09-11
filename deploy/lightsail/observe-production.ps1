param(
    [Parameter(Mandatory)][string]$Ip,
    [Parameter(Mandatory)][string]$KeyPath,
    [Parameter(Mandatory)][string]$KnownHostsPath,
    [Parameter(Mandatory)][string]$ExpectedRevision,
    [Parameter(Mandatory)][string]$ExpectedPreviousRevision,
    [Parameter(Mandatory)][string]$ExpectedImageDigest,
    [Parameter(Mandatory)][string]$ExpectedServerStartedAt,
    [Parameter(Mandatory)][string]$TrackingIssue,
    [Parameter(Mandatory)][string]$DeploymentRunId,
    [Parameter(Mandatory)][string]$ObservationControllerSha,
    [Parameter(Mandatory)][string]$DeploymentOperationalSha,
    [Parameter(Mandatory)][string]$EvidencePath,
    [Parameter(Mandatory)][string]$SummaryPath,
    [string]$BaselinePath,
    [string]$ExpectedMigrationVersion = "",
    [string]$User = "ubuntu"
)

$ErrorActionPreference = "Stop"
foreach ($sha in @($ObservationControllerSha, $DeploymentOperationalSha)) {
    if ($sha -cnotmatch '^[0-9a-f]{40}$') { throw "observer and deployment controller SHAs must be immutable commits" }
}
$BuilderPollAttempts = 240
$BuilderPollSeconds = 20
$ActiveBuilderLatencyRounds = 5
$MaxActiveBuilderTTFBSeconds = 10.0
$MaxPressureWaitSeconds = 3.0
$ExtendedObservationSeconds = 600
$SampleTimeoutSeconds = 180
$BuilderWindowSeconds = 4800
$observationWindowMinutes = [int](($BuilderPollAttempts * $BuilderPollSeconds) / 60)
$repo = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$collector = Join-Path $PSScriptRoot "collect-post-deploy-observation.sh"
$extendedCollector = Join-Path $PSScriptRoot "collect-extended-observation.sh"
$detailedCollector = Join-Path $PSScriptRoot "collect-production-evidence.sh"
$ssh = (Get-Command ssh -ErrorAction Stop).Source
$latencyPaths = [ordered]@{
    healthz = '/healthz'
    landing = '/'
    wanted = '/v1/wanted'
    otel = '/golang/go.opentelemetry.io/otel/v1.45.0'
    package = '/golang/github.com/jackc/pgx/v5/v5.10.0'
    sample = '/samples/sha256:13f4bcf31db6296c4d9325831f69e508e320520ab70dd6b2d237a11557c9fe9a'
}

foreach ($sha in @($ExpectedRevision, $ExpectedPreviousRevision)) {
    if ($sha -notmatch '^[0-9a-f]{40}$') { throw "production revisions must be lowercase immutable SHAs" }
}
if ($ExpectedRevision -eq $ExpectedPreviousRevision) { throw "target and previous production revisions must differ" }
if ($ExpectedImageDigest -notmatch '^sha256:[0-9a-f]{64}$') { throw "expected image digest is malformed" }
if ($ExpectedServerStartedAt -notmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$') {
    throw "expected server start must be an immutable UTC RFC3339 timestamp"
}
if ($TrackingIssue -notmatch '^(?:#?[1-9][0-9]*|https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/issues/[1-9][0-9]*)$') {
    throw "invalid GitHub tracking issue identifier"
}
if ($DeploymentRunId -notmatch '^[1-9][0-9]*$') { throw "deployment run id must be numeric" }
if ($User -notmatch '^[a-z_][a-z0-9_-]{0,31}$') { throw "user must be a simple Linux account name" }
foreach ($path in @($KeyPath, $KnownHostsPath, $collector, $extendedCollector, $detailedCollector)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "required observation file is missing" }
}
if ([IO.Path]::GetFullPath($EvidencePath) -eq [IO.Path]::GetFullPath($SummaryPath)) {
    throw "JSON evidence and Markdown summary paths must differ"
}

$expectedMigration = $ExpectedMigrationVersion
if ($expectedMigration -eq "") {
    $expectedMigration = (Get-ChildItem (Join-Path $repo "internal/serverstore/migrations") -Filter "*.sql" -File |
        Sort-Object Name | Select-Object -Last 1).Name
}
if ($expectedMigration -notmatch '^[0-9]{4}_[a-z0-9_]+\.sql$') {
    throw "could not determine the expected migration version"
}

$remote = "${User}@${Ip}"
$sshArgs = @(
    "-i", $KeyPath,
    "-o", "StrictHostKeyChecking=yes",
    "-o", "UserKnownHostsFile=$KnownHostsPath",
    "-o", "ConnectTimeout=20",
    "-o", "BatchMode=yes",
    "-o", "ServerAliveInterval=10",
    "-o", "ServerAliveCountMax=2",
    $remote
)
$collectorBytes = [IO.File]::ReadAllBytes($collector)
$extendedSource = [IO.File]::ReadAllText($extendedCollector).Replace('__CSX_DETAILED_COLLECTOR__', [IO.File]::ReadAllText($detailedCollector))
$extendedBytes = [Text.UTF8Encoding]::new($false).GetBytes($extendedSource)

function Read-ObservationSample([bool]$IncludeLatency, [bool]$IncludeDetail, [bool]$IncludeExtended = $false, [int]$TimeoutSeconds = $SampleTimeoutSeconds) {
    $mode = if ($IncludeLatency) { "1" } else { "0" }
    $detailMode = if ($IncludeDetail) { "1" } else { "0" }
    # The leading marker is intentionally consumed by the remote '#'. It also
    # neutralizes the UTF-8 preamble Windows PowerShell may put on stdin.
    # Frame the full collector as one compound command before execution:
    # docker exec must not consume unparsed shell source from this stdin.
    # The remote transport supplies the fixed closing brace. The validated
    # timestamp contains no shell metacharacters.
    $observationStartedAt = $evidence.observationWindowStartedAt
    $prefix = "CSX-OBSERVE-V1`n{`nCSX_OBSERVE_LATENCY=$mode`nCSX_OBSERVE_DETAIL=$detailMode`nCSX_OBSERVE_SINCE=$ExpectedServerStartedAt`nCSX_OBSERVATION_STARTED_AT=$observationStartedAt`n"
    $prefixBytes = [Text.UTF8Encoding]::new($false).GetBytes($prefix)
    $sourceBytes = if ($IncludeExtended) { $extendedBytes } else { $collectorBytes }
    $payload = [byte[]]::new($prefixBytes.Length + $sourceBytes.Length)
    [Array]::Copy($prefixBytes, 0, $payload, 0, $prefixBytes.Length)
    [Array]::Copy($sourceBytes, 0, $payload, $prefixBytes.Length, $sourceBytes.Length)

    $psi = [Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = $ssh
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    foreach ($arg in $sshArgs) { [void]$psi.ArgumentList.Add($arg) }
    $remoteCommand = "{ printf '#'; cat; printf '\n}\n'; } | timeout --kill-after=5s ${TimeoutSeconds}s sh"
    [void]$psi.ArgumentList.Add($remoteCommand)
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $psi
    $probeClock = [Diagnostics.Stopwatch]::StartNew()
    $transportLimitMs = ($TimeoutSeconds + 15) * 1000
    try {
        if (-not $process.Start()) { throw "could not start production observation probe" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        try {
            $writeTask = $process.StandardInput.BaseStream.WriteAsync($payload, 0, $payload.Length)
            if (-not $writeTask.Wait($transportLimitMs)) { throw "observation SSH input exceeded its bounded deadline" }
        }
        finally { $process.StandardInput.Close() }
        $remainingMs = [int][Math]::Max(1, $transportLimitMs - $probeClock.ElapsedMilliseconds)
        if (-not $process.WaitForExit($remainingMs)) { throw "observation SSH probe exceeded its bounded deadline" }
        $remainingMs = [int][Math]::Max(1, $transportLimitMs - $probeClock.ElapsedMilliseconds)
        if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdoutTask, $stderrTask), $remainingMs)) {
            throw "observation SSH output exceeded its bounded deadline"
        }
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) { throw "production observation probe failed ($($process.ExitCode))" }

        $state = [ordered]@{}
        foreach ($line in ($stdout -split "`r?`n")) {
            if ([string]::IsNullOrWhiteSpace($line)) { continue }
            $pair = $line -split '=', 2
            if ($pair.Count -ne 2 -or $state.Contains($pair[0])) { throw "malformed production observation evidence" }
            $state[$pair[0]] = $pair[1]
        }
        if ($IncludeExtended) {
            foreach ($name in @('identity_before','identity_after','observation_started_at','observed_at','privacy_preflight','privacy_live','public_surface','admin_state','activity_state','detail_status')) {
                if (-not $state.Contains($name)) { throw "extended observation evidence is missing $name" }
            }
            foreach ($name in @('privacy_preflight','privacy_live','public_surface','admin_state','activity_state','detail_status')) {
                if ($state[$name] -notin @('pass','fail','violation','unavailable')) { throw "extended observation evidence has malformed $name" }
            }
            foreach ($name in $state.Keys) {
                if ($name -match '^[a-z_]+_seconds$' -and $state[$name] -match '^\d+$') {
                    Write-Output "phase=extended-$name elapsed=$($state[$name])s" | Out-Host
                }
            }
            return $state
        }
        $required = @(
            'observed_at','revision','image_digest','image_revision','migration_version','health','served_revision',
            'server_started_at','restart_count','oom_killed','container_status','builder_generated_at','builder_fresh','builder_active',
            'builder_lifecycle_state','builder_error_events','builder_error_events_before_observation',
            'builder_error_events_during_observation','builder_error_window_status',
            'pressure_window_status','window_pressure_lines','window_pool_busy_events','window_query_timeout_events','window_max_pressure_wait_seconds',
            'cpu_percent','memory_usage','memory_percent','load_average','detail_collected','pressure_lines','pool_busy_events',
            'query_timeout_events','pool_busy_event_total','query_timeout_event_total',
            'admission_refused_event_total','deferred_refused_event_total',
            'max_pressure_wait_seconds','oom_events','restart_events','die_events','settled_fail_observations',
            'die_event_first_epoch','die_event_last_epoch',
            'settled_failure_cluster_observations','settled_unbalanced_failure_cluster_rows',
            'settled_invariant_status','settled_invariant_row_limit','settled_failure_cluster_rows_examined','settled_source_rows_examined',
            'settled_source_row_limit','settled_invariant_json_byte_limit','settled_invariant_exit_code','settled_invariant_seconds'
        )
        if ($IncludeLatency) {
            foreach ($name in $latencyPaths.Keys) {
                $required += "latency_${name}_status", "latency_${name}_ttfb_seconds", "latency_${name}_content_valid"
            }
        }
        foreach ($name in $required) {
            if (-not $state.Contains($name)) { throw "production observation evidence is missing $name" }
        }
        foreach ($name in @('restart_count','builder_error_events','pressure_lines','pool_busy_events','query_timeout_events','oom_events','restart_events','die_events',
                'pool_busy_event_total','query_timeout_event_total','admission_refused_event_total','deferred_refused_event_total',
                'die_event_first_epoch','die_event_last_epoch',
                'builder_error_events_before_observation','builder_error_events_during_observation',
                'settled_invariant_row_limit','settled_source_row_limit','settled_invariant_json_byte_limit',
                'window_pressure_lines','window_pool_busy_events','window_query_timeout_events')) {
            if ($state[$name] -notmatch '^\d+$') { throw "production observation evidence has malformed $name" }
            $state[$name] = [int64]$state[$name]
        }
        foreach ($name in @('settled_fail_observations','settled_failure_cluster_observations','settled_unbalanced_failure_cluster_rows',
                'settled_failure_cluster_rows_examined','settled_source_rows_examined','settled_invariant_exit_code','settled_invariant_seconds')) {
            if ($state[$name] -eq '') { $state[$name] = $null; continue }
            if ($state[$name] -notmatch '^\d+$') { throw "production observation evidence has malformed $name" }
            $state[$name] = [int64]$state[$name]
        }
        if ($state.settled_invariant_status -notin @('not-collected','complete','budget-exceeded','unavailable')) {
            throw "production observation evidence has malformed settled_invariant_status"
        }
        if ($state.builder_error_window_status -notin @('complete','unavailable')) {
            throw "production observation evidence has malformed builder_error_window_status"
        }
        if ($state.pressure_window_status -notin @('complete','unavailable')) {
            throw "production observation evidence has malformed pressure_window_status"
        }
        if ($state.builder_error_window_status -eq 'complete' -and
            $state.builder_error_events -ne ($state.builder_error_events_before_observation + $state.builder_error_events_during_observation)) {
            throw "production observation builder error window does not reconcile with retained history"
        }
        foreach ($name in @('builder_fresh','builder_active','oom_killed','detail_collected')) {
            if ($state[$name] -notin @('true','false')) { throw "production observation evidence has malformed $name" }
            $state[$name] = $state[$name] -eq 'true'
        }
        if ($state.builder_lifecycle_state -notin @('none','start','complete','error','race')) {
            throw "production observation evidence has malformed builder_lifecycle_state"
        }
        if ($state.max_pressure_wait_seconds -notmatch '^\d+(?:\.\d+)?$') {
            throw "production observation evidence has malformed max_pressure_wait_seconds"
        }
        $state.max_pressure_wait_seconds = Convert-Percent $state.max_pressure_wait_seconds
        if ($state.window_max_pressure_wait_seconds -notmatch '^\d+(?:\.\d+)?$') {
            throw "production observation evidence has malformed window_max_pressure_wait_seconds"
        }
        $state.window_max_pressure_wait_seconds = Convert-Percent $state.window_max_pressure_wait_seconds
        if ($IncludeLatency) {
            foreach ($name in $latencyPaths.Keys) {
                if ($state["latency_${name}_status"] -notmatch '^\d{3}$') {
                    throw "production observation evidence has malformed $name latency status"
                }
                if ($state["latency_${name}_ttfb_seconds"] -ne 'unavailable' -and
                    $state["latency_${name}_ttfb_seconds"] -notmatch '^\d+(?:\.\d+)?$') {
                    throw "production observation evidence has malformed $name TTFB"
                }
                if ($state["latency_${name}_content_valid"] -notin @('true','false')) {
                    throw "production observation evidence has malformed $name content validity"
                }
                $state["latency_${name}_content_valid"] = $state["latency_${name}_content_valid"] -eq 'true'
            }
        }
        return $state
    } finally {
        try { if (-not $process.HasExited) { $process.Kill($true) } } catch { }
        Write-Output "phase=observation-$(if ($IncludeExtended) { 'extended' } elseif ($IncludeDetail) { 'detail' } elseif ($IncludeLatency) { 'latency' } else { 'sample' }) elapsed=$([Math]::Round($probeClock.Elapsed.TotalSeconds, 3))s ceiling=$($TimeoutSeconds + 15)s" | Out-Host
        [Array]::Clear($payload, 0, $payload.Length)
        [Array]::Clear($prefixBytes, 0, $prefixBytes.Length)
        $stdout = $null
        $stderr = $null
        $process.Dispose()
    }
}

function Add-ExtendedObservationEvidence([Collections.IDictionary]$Evidence, [Collections.IDictionary]$Sample) {
    $Evidence.extended = $Sample
    $identity = "${ExpectedRevision}|${ExpectedImageDigest}|${ExpectedServerStartedAt}|${ExpectedRevision}|"
    $identityVerified = $Sample.identity_before -ceq $identity -and $Sample.identity_after -ceq $identity
    if (-not $identityVerified) { $Evidence.anomalies.Add('extended observation identity changed or could not be authenticated') }
    if ($identityVerified -and $Sample.observation_started_at -cmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$') {
        $Evidence.observationWindowStartedAt = $Sample.observation_started_at
    } else { $Evidence.anomalies.Add('remote observation start could not be authenticated') }
    foreach ($name in @('privacy_preflight','privacy_live','public_surface','admin_state','activity_state','detail_status')) {
        if ($Sample[$name] -ne 'pass') { $Evidence.anomalies.Add("extended $name check: $($Sample[$name])") }
    }
    if ($Sample.privacy_live -eq 'violation' -and $Sample.privacy_live_probe_id -cmatch '^[0-9a-f]{32}$') {
        $Evidence.findings.Add([ordered]@{
            code = 'privacy-synthetic-marker-recorded'
            classification = 'security-critical'
            proven = $true
            identityVerified = $identityVerified
            evidence = "Unique synthetic probe $($Sample.privacy_live_probe_id) was positively found in the live access log."
        })
    }
    if ($Sample.detail_status -eq 'pass') {
        try {
            $invariants = $Sample.detail_invariants | ConvertFrom-Json
            foreach ($name in @('pass','fail','publishedSamples','failureClusterObservations','unbalancedFailureClusterRows','pgxParseConfigPass','pgxParseConfigFail')) {
                if ([string]$invariants.$name -notmatch '^\d+$') { throw 'invalid detailed invariant' }
            }
            $quality = $Sample.detail_failure_evidence_quality | ConvertFrom-Json
            if ($quality.available -isnot [bool] -or -not $quality.available) { throw 'evidence quality snapshot unavailable' }
            foreach ($name in @('fail','complete','partial','missing','legacyEvidenceIncomplete')) {
                if ([string]$quality.$name -notmatch '^\d+$') { throw 'invalid evidence quality count' }
            }
            if ($invariants.unbalancedFailureClusterRows -ne 0) {
                $Evidence.anomalies.Add('detailed failure-cluster ledger has internally unbalanced rows')
            }
            if ($invariants.fail -gt 0 -and $invariants.failureClusterObservations -le 0) {
                $Evidence.anomalies.Add('detailed failure-cluster materialization is empty while FAIL evidence remains')
            }
            $total = [decimal]$quality.complete + [decimal]$quality.partial + [decimal]$quality.missing + [decimal]$quality.legacyEvidenceIncomplete
            if ([decimal]$quality.fail -ne $total) {
                $Evidence.anomalies.Add('complete + partial + missing + legacy-evidence-incomplete does not equal FAIL')
            }
            # Derived convergence and a single snapshot cannot prove a loss of
            # source data attributable to this activation. Keep these incidents.
        } catch { $Evidence.anomalies.Add('detailed invariant or evidence-quality snapshot is unavailable or malformed') }
    }
}

function Set-ObservationClassification([Collections.IDictionary]$Evidence) {
    $Evidence.rollbackRequested = $false
    $Evidence.classification = if ($Evidence.anomalies.Count -eq 0) { 'none' } else { 'incident-only' }
    foreach ($finding in $Evidence.findings) {
        # Explicit proof allowlist: prose, missing evidence and arbitrary
        # severity labels cannot upgrade an incident into a rollback request.
        if ($finding.code -eq 'privacy-synthetic-marker-recorded' -and
            $finding.classification -eq 'security-critical' -and
            $finding.proven -is [bool] -and $finding.proven -and
            $finding.identityVerified -is [bool] -and $finding.identityVerified -and
            -not [string]::IsNullOrWhiteSpace($finding.evidence)) {
            $Evidence.classification = 'security-critical'
            $Evidence.rollbackRequested = $true
        }
    }
    $Evidence.recommendedAction = if ($Evidence.rollbackRequested) {
        'Primary incident owner: review the proven security evidence and request exact rollback through the canonical production deployment path.'
    } elseif ($Evidence.classification -eq 'incident-only') {
        'Create or continue incident work; do not automatically roll back a healthy exact-SHA server.'
    } else { 'Continue normal production observation.' }
}

function Add-SourceContinuityEvidence([Collections.IDictionary]$Evidence, [string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }
    try {
        $baseline = [IO.File]::ReadAllText($Path) | ConvertFrom-Json
        if ($baseline.targetSha -cne $ExpectedPreviousRevision -or
            [string]$baseline.baselineDeploymentRunId -notmatch '^[1-9][0-9]*$') { throw 'baseline identity mismatch' }
        $current = $Evidence.extended.detail_invariants | ConvertFrom-Json
        $decreased = [Collections.Generic.List[string]]::new()
        foreach ($name in @('pass','fail','publishedSamples','pgxParseConfigPass','pgxParseConfigFail')) {
            if ([string]$baseline.invariants.$name -notmatch '^\d+$' -or [string]$current.$name -notmatch '^\d+$') {
                throw 'baseline or current source count unavailable'
            }
            if ([decimal]$current.$name -lt [decimal]$baseline.invariants.$name) { $decreased.Add($name) }
        }
        $Evidence.sourceContinuity = [ordered]@{
            status = if ($decreased.Count -eq 0) { 'no-decrease-observed' } else { 'decrease-observed' }
            classification = if ($decreased.Count -eq 0) { 'none' } else { 'incident-only' }
            rollbackRequested = $false
            baselineDeploymentRunId = [string]$baseline.baselineDeploymentRunId
            baselineSha = $baseline.targetSha
            baseline = $baseline.invariants
            current = $current
            decreasedCounters = @($decreased)
            reason = 'Compared with an authenticated successful previous-SHA deployment artifact; a decrease needs incident investigation and does not prove this activation caused data loss.'
        }
        if ($decreased.Count -ne 0) {
            $Evidence.anomalies.Add("source counters decreased since the authenticated previous deployment: $($decreased -join ', ')")
        }
    } catch {
        $Evidence.sourceContinuity.reason = 'Baseline or current source counters were unavailable or malformed; source-loss continuity is not assessed.'
    }
}

function Get-StateAnomalies([Collections.IDictionary]$Sample, [string]$ExpectedImageDigest) {
    $found = [Collections.Generic.List[string]]::new()
    if ($Sample.revision -eq $ExpectedPreviousRevision) {
        $found.Add("rollback detected: configured revision returned to the previous production SHA")
    } elseif ($Sample.revision -ne $ExpectedRevision) {
        $found.Add("configured revision drifted from the deployed SHA")
    }
    if ($Sample.image_revision -ne $ExpectedRevision) { $found.Add("OCI image revision does not match the deployed SHA") }
    if ($Sample.served_revision -ne $ExpectedRevision) { $found.Add("served /version revision does not match the deployed SHA") }
    if ($Sample.image_digest -notmatch '^sha256:[0-9a-f]{64}$') {
        $found.Add("live image digest is malformed")
    } elseif ($ExpectedImageDigest -and $Sample.image_digest -ne $ExpectedImageDigest) {
        $found.Add("live image digest changed during observation")
    }
    if ($Sample.migration_version -ne $expectedMigration) { $found.Add("latest migration does not match the checked-out server") }
    if ($Sample.health -ne 'ok') { $found.Add("health is not ok") }
    if ($Sample.container_status -ne 'running') { $found.Add("server container is not running") }
    if ($Sample.server_started_at -ne $ExpectedServerStartedAt) { $found.Add("server start timestamp changed during observation") }
    if ($Sample.restart_count -ne 0 -or $Sample.restart_events -ne 0 -or $Sample.die_events -ne 0) {
        $found.Add("server restart or exit detected during observation")
    }
    if ($Sample.oom_killed -or $Sample.oom_events -ne 0) { $found.Add("server OOM detected during observation") }
    if ($Sample.builder_error_window_status -ne 'complete') {
        $found.Add("builder error observation window could not be authenticated")
    } elseif ($Sample.builder_error_events_during_observation -ne 0) { $found.Add("builder error detected during observation") }
    if ($Sample.pressure_window_status -ne 'complete') { $found.Add('DB-pressure observation window could not be authenticated') }
    return $found
}

function Convert-Percent([object]$Value) {
    $number = 0.0
    if ([double]::TryParse([string]$Value, [Globalization.NumberStyles]::Float,
            [Globalization.CultureInfo]::InvariantCulture, [ref]$number)) { return $number }
    return $null
}

function Update-PressureEvidence([Collections.IDictionary]$Evidence, [Collections.IDictionary]$Sample) {
    foreach ($pair in @(@('cpu_percent','peakCpuPercent'), @('memory_percent','peakMemoryPercent'))) {
        $value = Convert-Percent $Sample[$pair[0]]
        if ($null -ne $value -and ($null -eq $Evidence.pressure[$pair[1]] -or $value -gt $Evidence.pressure[$pair[1]])) {
            $Evidence.pressure[$pair[1]] = $value
        }
    }
    $load1 = Convert-Percent (($Sample.load_average -split ' ')[0])
    if ($null -ne $load1 -and ($null -eq $Evidence.pressure.peakLoad1 -or $load1 -gt $Evidence.pressure.peakLoad1)) {
        $Evidence.pressure.peakLoad1 = $load1
    }
    if ($Sample.builder_error_events -lt $Evidence.events.builderError) {
        $Evidence.anomalies.Add('retained builder error history regressed during observation')
    }
    if ($Sample.builder_error_events -gt $Evidence.events.builderError) {
        $Evidence.events.builderError = $Sample.builder_error_events
    }
    if ($Sample.builder_error_window_status -eq 'complete') {
        foreach ($pair in @(@('builder_error_events_before_observation','builderErrorBeforeObservation'),
                @('builder_error_events_during_observation','builderErrorDuringObservation'))) {
            if ($null -ne $Evidence.events[$pair[1]] -and $Sample[$pair[0]] -lt $Evidence.events[$pair[1]]) {
                $Evidence.anomalies.Add('retained builder error window regressed during observation')
            }
            if ($null -eq $Evidence.events[$pair[1]] -or $Sample[$pair[0]] -gt $Evidence.events[$pair[1]]) {
                $Evidence.events[$pair[1]] = $Sample[$pair[0]]
            }
        }
    }
    if ($Sample.pressure_window_status -eq 'complete') {
        $Evidence.pressure.windowMeasured = $true
        foreach ($pair in @(@('window_pressure_lines','windowPressureLines'), @('window_pool_busy_events','windowPoolBusyEvents'),
                @('window_query_timeout_events','windowQueryTimeoutEvents'), @('window_max_pressure_wait_seconds','windowMaxWaitSeconds'))) {
            if ($Sample[$pair[0]] -lt $Evidence.pressure[$pair[1]]) { $Evidence.anomalies.Add('retained DB-pressure window regressed during observation') }
            if ($Sample[$pair[0]] -gt $Evidence.pressure[$pair[1]]) { $Evidence.pressure[$pair[1]] = $Sample[$pair[0]] }
        }
    }
    if (-not $Sample.detail_collected) { return }
    $Evidence.pressure.measured = $true
    $Evidence.events.measured = $true
    foreach ($name in @('pressureLines','poolBusyEvents','queryTimeoutEvents',
            'poolBusyEventCount','queryTimeoutEventCount','admissionRefusedEventCount','deferredRefusedEventCount')) {
        $source = switch ($name) {
            'pressureLines' { 'pressure_lines' }
            'poolBusyEvents' { 'pool_busy_events' }
            'queryTimeoutEvents' { 'query_timeout_events' }
            'poolBusyEventCount' { 'pool_busy_event_total' }
            'queryTimeoutEventCount' { 'query_timeout_event_total' }
            'admissionRefusedEventCount' { 'admission_refused_event_total' }
            'deferredRefusedEventCount' { 'deferred_refused_event_total' }
        }
        if ($Sample[$source] -gt $Evidence.pressure[$name]) { $Evidence.pressure[$name] = $Sample[$source] }
    }
    if ($null -ne $Sample.max_pressure_wait_seconds -and $Sample.max_pressure_wait_seconds -gt $Evidence.pressure.maxWaitSeconds) {
        $Evidence.pressure.maxWaitSeconds = $Sample.max_pressure_wait_seconds
    }
    foreach ($pair in @(@('restart_events','restart'), @('oom_events','oom'), @('die_events','die'))) {
        if ($Sample[$pair[0]] -gt $Evidence.events[$pair[1]]) {
            $Evidence.events[$pair[1]] = $Sample[$pair[0]]
        }
    }
}

function Add-RouteLatencyEvidence(
    [Collections.IDictionary]$Evidence,
    [Collections.IDictionary]$Sample,
    [ValidateSet('active-builder','settled')][string]$Phase
) {
    if ($Phase -eq 'active-builder') {
        $Evidence.activeBuilder.observed = $true
        $Evidence.activeBuilder.rounds++
    }
    foreach ($name in $latencyPaths.Keys) {
        $status = $Sample["latency_${name}_status"]
        $rawTTFB = $Sample["latency_${name}_ttfb_seconds"]
        $ttfb = if ($rawTTFB -eq 'unavailable') { $null } else { Convert-Percent $rawTTFB }
        $entry = [ordered]@{
            observedAt = $Sample.observed_at
            phase = $Phase
            name = $name
            path = $latencyPaths[$name]
            status = $status
            ttfbSeconds = $ttfb
            contentValid = $Sample["latency_${name}_content_valid"]
        }
        if ($Phase -eq 'active-builder') {
            $Evidence.activeBuilder.requests++
            $Evidence.activeBuilder.samples.Add($entry)
            if ($status -eq '503') { $Evidence.activeBuilder.http503Count++ }
            if ($null -ne $ttfb -and ($null -eq $Evidence.activeBuilder.maxTTFBSeconds -or $ttfb -gt $Evidence.activeBuilder.maxTTFBSeconds)) {
                $Evidence.activeBuilder.maxTTFBSeconds = $ttfb
            }
        } else {
            $Evidence.latencies[$name] = $entry
        }
        if ($status -ne '200') {
            $Evidence.anomalies.Add("$Phase $($latencyPaths[$name]) returned HTTP $status")
        }
        if (-not $entry.contentValid) {
            $Evidence.anomalies.Add("$Phase $($latencyPaths[$name]) did not return its canonical page content")
        }
        if ($null -eq $ttfb) {
            $Evidence.anomalies.Add("$Phase $($latencyPaths[$name]) did not return a bounded TTFB")
        } elseif ($Phase -eq 'active-builder' -and $ttfb -gt $MaxActiveBuilderTTFBSeconds) {
            $Evidence.anomalies.Add("active-builder $($latencyPaths[$name]) TTFB ${ttfb}s exceeded ${MaxActiveBuilderTTFBSeconds}s")
        }
    }
}

function Get-SettledInvariantAnomalies([Collections.IDictionary]$Sample) {
    $found = [Collections.Generic.List[string]]::new()
    if (-not $Sample.detail_collected -or $Sample.settled_invariant_status -ne 'complete') {
        $found.Add("settled failure-cluster invariant was not collected")
        return $found
    }
    if ($null -eq $Sample.settled_failure_cluster_observations -or
        $null -eq $Sample.settled_unbalanced_failure_cluster_rows -or
        $null -eq $Sample.settled_failure_cluster_rows_examined -or $null -eq $Sample.settled_source_rows_examined -or
        ($Sample.settled_failure_cluster_observations -le 0 -and $Sample.settled_unbalanced_failure_cluster_rows -eq 0 -and $null -eq $Sample.settled_fail_observations)) {
        $found.Add("settled failure-cluster invariant has incomplete coverage")
        return $found
    }
    if ($Sample.settled_fail_observations -gt 0 -and $Sample.settled_failure_cluster_observations -le 0) {
        $found.Add("settled failure-cluster materialization is empty while FAIL evidence remains")
    }
    if ($Sample.settled_unbalanced_failure_cluster_rows -ne 0) {
        $found.Add("settled failure-cluster ledger has internally unbalanced rows")
    }
    return $found
}

function Add-SettledObservationEvidence([Collections.IDictionary]$Evidence, [Collections.IDictionary]$Sample) {
    $Evidence.settledInvariant.status = $Sample.settled_invariant_status
    $Evidence.settledInvariant.rowLimit = $Sample.settled_invariant_row_limit
    $Evidence.settledInvariant.sourceRowLimit = $Sample.settled_source_row_limit
    $Evidence.settledInvariant.jsonByteLimit = $Sample.settled_invariant_json_byte_limit
    $Evidence.settledInvariant.exitCode = $Sample.settled_invariant_exit_code
    $Evidence.settledInvariant.elapsedSeconds = $Sample.settled_invariant_seconds
    $Evidence.settledInvariant.clusterRowsExamined = $Sample.settled_failure_cluster_rows_examined
    $Evidence.settledInvariant.sourceRowsExamined = $Sample.settled_source_rows_examined
    if (-not $Sample.builder_fresh -or $Sample.builder_lifecycle_state -ne 'complete') {
        $Evidence.settledInvariant.status = 'not-settled'
        $Evidence.anomalies.Add('terminal sample did not remain settled throughout collection')
        return
    }
    $Evidence.settledInvariant.failObservations = $Sample.settled_fail_observations
    $Evidence.settledInvariant.failureClusterObservations = $Sample.settled_failure_cluster_observations
    $Evidence.settledInvariant.unbalancedRows = $Sample.settled_unbalanced_failure_cluster_rows
    foreach ($anomaly in (Get-SettledInvariantAnomalies $Sample)) { $Evidence.anomalies.Add($anomaly) }
    Add-RouteLatencyEvidence $Evidence $Sample 'settled'
}

function Write-ObservationEvidence([Collections.IDictionary]$Evidence) {
    foreach ($path in @($EvidencePath, $SummaryPath)) {
        $parent = Split-Path -Parent $path
        if (-not [string]::IsNullOrWhiteSpace($parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    }
    $json = $Evidence | ConvertTo-Json -Depth 12
    [IO.File]::WriteAllText($EvidencePath, $json + "`n", [Text.UTF8Encoding]::new($false))

    $latencyLines = [Collections.Generic.List[string]]::new()
    foreach ($name in $latencyPaths.Keys) {
        $latency = $Evidence.latencies[$name]
        if ($null -ne $latency) { $latencyLines.Add("| $($latencyPaths[$name]) | $($latency.status) | $($latency.ttfbSeconds) |") }
    }
    $activeLatencyLines = [Collections.Generic.List[string]]::new()
    foreach ($latency in $Evidence.activeBuilder.samples) {
        $activeLatencyLines.Add("| $($latency.observedAt) | $($latency.path) | $($latency.status) | $($latency.contentValid) | $($latency.ttfbSeconds) |")
    }
    $anomalyText = if ($Evidence.anomalies.Count -eq 0) { "None" } else { ($Evidence.anomalies | ForEach-Object { "- $_" }) -join "`n" }
    $summary = @"
## Post-deploy observation: $($Evidence.conclusion.ToUpperInvariant())

- Deployment run: $DeploymentRunId
- Deployment controller SHA: $DeploymentOperationalSha
- Observation controller SHA: $ObservationControllerSha
- Observation error window starts (remote clock): $($Evidence.observationWindowStartedAt)
- Tracking issue: $TrackingIssue
- Classification: $($Evidence.classification)
- Rollback requested: $($Evidence.rollbackRequested) (request only; never executed by this observer)
- Recommended action: $($Evidence.recommendedAction)
- Source continuity: $($Evidence.sourceContinuity.status). $($Evidence.sourceContinuity.reason)
- Target SHA: $ExpectedRevision
- Image digest: $($Evidence.imageDigest)
- Migration: $expectedMigration
- Builder converged: $($Evidence.converged)
- Builder generatedAt: $($Evidence.builderGeneratedAt)
- Original server age at observed builder completion: $($Evidence.builderCompletionSeconds) seconds (not measured pass duration)
- Active-builder acceptance: $($Evidence.activeBuilder.status)
- Observation samples: $($Evidence.samples.Count)
- Active-builder latency observed: $($Evidence.activeBuilder.observed)
- Active-builder latency rounds: $($Evidence.activeBuilder.rounds)
- Active-builder HTTP 503s: $($Evidence.activeBuilder.http503Count)
- Active-builder max TTFB: $($Evidence.activeBuilder.maxTTFBSeconds) seconds (limit $MaxActiveBuilderTTFBSeconds)
- Peak server CPU: $($Evidence.pressure.peakCpuPercent)%
- Peak server memory: $($Evidence.pressure.peakMemoryPercent)%
- Peak host load (1m): $($Evidence.pressure.peakLoad1)
- Pool-pressure log lines: $($Evidence.pressure.pressureLines)
- Pool-busy observations: $($Evidence.pressure.poolBusyEvents)
- Query-timeout observations: $($Evidence.pressure.queryTimeoutEvents)
- Pool-busy events (server counter): $($Evidence.pressure.poolBusyEventCount)
- Query-timeout events (server counter): $($Evidence.pressure.queryTimeoutEventCount)
- Admission-refused events (server counter): $($Evidence.pressure.admissionRefusedEventCount)
- Deferred-lane refusal events (server counter): $($Evidence.pressure.deferredRefusedEventCount)
- Maximum DB-pressure wait: $($Evidence.pressure.maxWaitSeconds) seconds (limit $MaxPressureWaitSeconds)
- Observation-window pressure measured: $($Evidence.pressure.windowMeasured)
- Observation-window pool-busy / query-timeout log lines: $($Evidence.pressure.windowPoolBusyEvents) / $($Evidence.pressure.windowQueryTimeoutEvents)
- Observation-window maximum DB-pressure wait: $($Evidence.pressure.windowMaxWaitSeconds) seconds (limit $MaxPressureWaitSeconds)
- Pressure/event detail measured: $($Evidence.pressure.measured) (unmeasured defaults are not zero-event proof)
- Builder errors since original server start: $($Evidence.events.builderError)
- Builder errors before observation: $($Evidence.events.builderErrorBeforeObservation)
- Builder errors during observation: $($Evidence.events.builderErrorDuringObservation)
- Restart events: $($Evidence.events.restart)
- OOM events: $($Evidence.events.oom)
- Container exit events: $($Evidence.events.die)
- Settled FAIL observations: $($Evidence.settledInvariant.failObservations)
- Settled failure-cluster observations: $($Evidence.settledInvariant.failureClusterObservations)
- Settled unbalanced cluster rows: $($Evidence.settledInvariant.unbalancedRows)
- Settled invariant coverage: $($Evidence.settledInvariant.status) (blank totals were not measured)
- Settled SQL exit / elapsed seconds: $($Evidence.settledInvariant.exitCode) / $($Evidence.settledInvariant.elapsedSeconds)
- Settled cluster/source rows examined: $($Evidence.settledInvariant.clusterRowsExamined) / $($Evidence.settledInvariant.sourceRowsExamined)

### Latency while builder work was active

| Observed at | Fixed path | HTTP | Canonical content | TTFB seconds |
| --- | --- | ---: | ---: | ---: |
$($activeLatencyLines -join "`n")

### Representative latency after convergence

| Fixed path | HTTP | TTFB seconds |
| --- | ---: | ---: |
$($latencyLines -join "`n")

### Anomalies

$anomalyText
"@
    [IO.File]::WriteAllText($SummaryPath, $summary.TrimEnd() + "`n", [Text.UTF8Encoding]::new($false))
}

$evidence = [ordered]@{
    schemaVersion = 2
    deploymentRunId = $DeploymentRunId
    observationControllerSha = $ObservationControllerSha
    deploymentOperationalSha = $DeploymentOperationalSha
    trackingIssue = $TrackingIssue
    targetSha = $ExpectedRevision
    previousProductionSha = $ExpectedPreviousRevision
    expectedImageDigest = $ExpectedImageDigest
    expectedServerStartedAt = $ExpectedServerStartedAt
    expectedMigrationVersion = $expectedMigration
    observationWindowMinutes = $observationWindowMinutes
    pollAttempts = $BuilderPollAttempts
    pollSeconds = $BuilderPollSeconds
    startedAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
    observationWindowStartedAt = ''
    completedAt = ""
    conclusion = "failure"
    converged = $false
    imageDigest = $ExpectedImageDigest
    builderGeneratedAt = ""
    builderCompletionSeconds = $null
    samples = [Collections.Generic.List[object]]::new()
    latencies = [ordered]@{}
    activeBuilder = [ordered]@{
        status = 'awaiting-natural-builder-evidence'
        observed = $false
        rounds = 0
        requests = 0
        http503Count = 0
        maxTTFBSeconds = $null
        ttfbLimitSeconds = $MaxActiveBuilderTTFBSeconds
        samples = [Collections.Generic.List[object]]::new()
    }
    settledInvariant = [ordered]@{
        status = 'not-collected'
        rowLimit = $null
        sourceRowLimit = $null
        jsonByteLimit = $null
        exitCode = $null
        elapsedSeconds = $null
        clusterRowsExamined = $null
        sourceRowsExamined = $null
        failObservations = $null
        failureClusterObservations = $null
        unbalancedRows = $null
    }
    pressure = [ordered]@{
        measured = $false
        windowMeasured = $false
        windowPressureLines = 0
        windowPoolBusyEvents = 0
        windowQueryTimeoutEvents = 0
        windowMaxWaitSeconds = 0.0
        peakCpuPercent = $null
        peakMemoryPercent = $null
        peakLoad1 = $null
        pressureLines = 0
        poolBusyEvents = 0
        queryTimeoutEvents = 0
        # The line counts above are capped at one per second per class. These
        # are the server's own cumulative counters, so they are the number of
        # requests that were actually refused -- including the refusals that
        # never reached the pool and used to leave no trace at all.
        poolBusyEventCount = 0
        queryTimeoutEventCount = 0
        admissionRefusedEventCount = 0
        deferredRefusedEventCount = 0
        maxWaitSeconds = 0.0
    }
    events = [ordered]@{ builderError = 0; builderErrorBeforeObservation = $null; builderErrorDuringObservation = $null; measured = $false; restart = 0; oom = 0; die = 0 }
    anomalies = [Collections.Generic.List[string]]::new()
    findings = [Collections.Generic.List[object]]::new()
    extended = [ordered]@{}
    classification = 'incident-only'
    rollbackRequested = $false
    recommendedAction = 'Observer incomplete: continue incident work.'
    sourceContinuity = [ordered]@{
        status = 'not-assessed'
        classification = 'incident-only'
        rollbackRequested = $false
        reason = 'No authenticated pre-activation source snapshot is available; post-activation counts cannot prove source-loss continuity. Investigate suspected source loss through the tracking incident.'
    }
}

$observationFailure = $null
try {
    # This bounded phase runs even when the builder never converges, and its
    # diagnostic failure cannot prevent the independent convergence checks.
    try {
        $extendedSample = Read-ObservationSample $false $false $true $ExtendedObservationSeconds
        Add-ExtendedObservationEvidence $evidence $extendedSample
    } catch { $evidence.anomalies.Add('extended observation probe failed before completion') }
    Add-SourceContinuityEvidence $evidence $BaselinePath
    $builderAnomalyStart = $evidence.anomalies.Count
    $builderClock = [Diagnostics.Stopwatch]::StartNew()
    for ($attempt = 1; $attempt -le $BuilderPollAttempts; $attempt++) {
        # The 80-minute budget includes probes, not just sleep intervals.
        # Reserve one full bounded sample so no probe overruns this window.
        if ($builderClock.Elapsed.TotalSeconds + $SampleTimeoutSeconds + 15 -gt $BuilderWindowSeconds) { break }
        # Poll lifecycle cheaply. Only a pass that is active now earns a
        # latency round; the collector then rechecks the same start marker
        # after all six requests before it labels that round active.
        $sample = Read-ObservationSample $false $false
        $evidence.samples.Add($sample)
        $evidence.builderGeneratedAt = $sample.builder_generated_at
        foreach ($anomaly in (Get-StateAnomalies $sample $ExpectedImageDigest)) { $evidence.anomalies.Add($anomaly) }
        Update-PressureEvidence $evidence $sample

        if ($sample.builder_active -and $evidence.activeBuilder.rounds -lt $ActiveBuilderLatencyRounds -and
            $builderClock.Elapsed.TotalSeconds + $SampleTimeoutSeconds + 15 -le $BuilderWindowSeconds) {
            $latencySample = Read-ObservationSample $true $false
            $evidence.samples.Add($latencySample)
            foreach ($anomaly in (Get-StateAnomalies $latencySample $ExpectedImageDigest)) { $evidence.anomalies.Add($anomaly) }
            Update-PressureEvidence $evidence $latencySample
            if ($latencySample.builder_active) {
                Add-RouteLatencyEvidence $evidence $latencySample 'active-builder'
            }
        }

        if ($evidence.anomalies.Count -gt $builderAnomalyStart) { break }
        if ($sample.builder_fresh -and $sample.builder_lifecycle_state -eq 'complete' -and $evidence.activeBuilder.rounds -ge $ActiveBuilderLatencyRounds) {
            $evidence.converged = $true
            $evidence.activeBuilder.status = 'measured'
            $evidence.builderGeneratedAt = $sample.builder_generated_at
            try {
                $serverStart = [DateTimeOffset]::Parse($ExpectedServerStartedAt, [Globalization.CultureInfo]::InvariantCulture)
                # The stats timestamp is the pass start written at completion.
                # The first idle sample after an observed active pass bounds the
                # actual completion to within one polling interval.
                $builderCompleted = [DateTimeOffset]::Parse($sample.observed_at, [Globalization.CultureInfo]::InvariantCulture)
                $completionSeconds = [int64][Math]::Floor(($builderCompleted - $serverStart).TotalSeconds)
                if ($completionSeconds -lt 0) { throw "negative builder completion interval" }
                $evidence.builderCompletionSeconds = $completionSeconds
            } catch {
                $evidence.anomalies.Add("builder completion timestamps are malformed")
            }
            break
        }
        if ($attempt -lt $BuilderPollAttempts -and $builderClock.Elapsed.TotalSeconds + $BuilderPollSeconds -le $BuilderWindowSeconds) {
            Start-Sleep -Seconds $BuilderPollSeconds
        }
    }

    if ($evidence.activeBuilder.rounds -lt $ActiveBuilderLatencyRounds) {
        $evidence.activeBuilder.status = 'insufficient-evidence-awaiting-natural-pass'
        $evidence.anomalies.Add("insufficient active-builder latency rounds: observed $($evidence.activeBuilder.rounds), required $ActiveBuilderLatencyRounds within the bounded 80-minute observation window")
    }
    if (-not $evidence.converged -and $evidence.anomalies.Count -eq $builderAnomalyStart) {
        $evidence.anomalies.Add("builder did not converge within the bounded 80-minute observation window")
    }
    if (-not $evidence.activeBuilder.observed) {
        $evidence.anomalies.Add("no latency sample was captured while builder work was active")
    }
    # Independent settled evidence is valuable even when the active window
    # was missed. It never contributes an active round or changes convergence.
    $final = Read-ObservationSample $true $true
    $evidence.samples.Add($final)
    Update-PressureEvidence $evidence $final
    foreach ($anomaly in (Get-StateAnomalies $final $ExpectedImageDigest)) {
        if (-not $evidence.anomalies.Contains($anomaly)) { $evidence.anomalies.Add($anomaly) }
    }
    Add-SettledObservationEvidence $evidence $final
    if (-not $evidence.pressure.windowMeasured) {
        $evidence.anomalies.Add('DB-pressure observation window was not measured')
    }
    if ($evidence.pressure.windowQueryTimeoutEvents -ne 0) {
        $evidence.anomalies.Add("query timeouts were observed during builder convergence" +
            " ($($evidence.pressure.windowQueryTimeoutEvents) observation-window log lines; lifetime counters are retained separately)")
    }
    if ($evidence.pressure.windowPoolBusyEvents -ne 0) {
        $evidence.anomalies.Add("pool-busy refusals were observed during builder convergence" +
            " ($($evidence.pressure.windowPoolBusyEvents) observation-window log lines; lifetime counters are retained separately)")
    }
    if ($evidence.pressure.windowMaxWaitSeconds -gt $MaxPressureWaitSeconds) {
        $evidence.anomalies.Add("maximum DB-pressure wait exceeded the ${MaxPressureWaitSeconds}s bound")
    }
    if ($evidence.anomalies.Count -eq 0) {
        $evidence.conclusion = "success"
    } else {
        $observationFailure = "post-deploy observation found one or more anomalies"
    }
} catch {
    $observationFailure = $_.Exception.Message
    $evidence.anomalies.Add("observation probe failed before completion")
} finally {
    Set-ObservationClassification $evidence
    $evidence.completedAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
    try { Write-ObservationEvidence $evidence }
    finally {
        [Array]::Clear($collectorBytes, 0, $collectorBytes.Length)
        [Array]::Clear($extendedBytes, 0, $extendedBytes.Length)
    }
}

if ($null -ne $observationFailure) { throw "$observationFailure; see $EvidencePath and $SummaryPath" }
Write-Output "post-deploy observation evidence: $EvidencePath"
Write-Output "post-deploy observation summary: $SummaryPath"
