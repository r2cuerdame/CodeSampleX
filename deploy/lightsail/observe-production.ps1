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
    [Parameter(Mandatory)][string]$EvidencePath,
    [Parameter(Mandatory)][string]$SummaryPath,
    [string]$User = "ubuntu"
)

$ErrorActionPreference = "Stop"
$BuilderPollAttempts = 240
$BuilderPollSeconds = 20
$observationWindowMinutes = [int](($BuilderPollAttempts * $BuilderPollSeconds) / 60)
$repo = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$collector = Join-Path $PSScriptRoot "collect-post-deploy-observation.sh"
$ssh = (Get-Command ssh -ErrorAction Stop).Source

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
foreach ($path in @($KeyPath, $KnownHostsPath, $collector)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "required observation file is missing" }
}
if ([IO.Path]::GetFullPath($EvidencePath) -eq [IO.Path]::GetFullPath($SummaryPath)) {
    throw "JSON evidence and Markdown summary paths must differ"
}

$expectedMigration = (Get-ChildItem (Join-Path $repo "internal/serverstore/migrations") -Filter "*.sql" -File |
    Sort-Object Name | Select-Object -Last 1).Name
if ($expectedMigration -notmatch '^[0-9]{4}_[a-z0-9_]+\.sql$') {
    throw "could not determine the expected migration version"
}

$remote = "${User}@${Ip}"
$sshArgs = @(
    "-i", $KeyPath,
    "-o", "StrictHostKeyChecking=yes",
    "-o", "UserKnownHostsFile=$KnownHostsPath",
    "-o", "ConnectTimeout=20",
    $remote,
    "{ printf '#'; cat; } | sh"
)
$collectorBytes = [IO.File]::ReadAllBytes($collector)

function Read-ObservationSample([bool]$IncludeLatency, [bool]$IncludeDetail) {
    $mode = if ($IncludeLatency) { "1" } else { "0" }
    $detailMode = if ($IncludeDetail) { "1" } else { "0" }
    # The leading marker is intentionally consumed by the remote '#'. It also
    # neutralizes the UTF-8 preamble Windows PowerShell may put on stdin. The
    # validated timestamp contains no shell metacharacters.
    $prefix = "CSX-OBSERVE-V1`nCSX_OBSERVE_LATENCY=$mode`nCSX_OBSERVE_DETAIL=$detailMode`nCSX_OBSERVE_SINCE=$ExpectedServerStartedAt`n"
    $prefixBytes = [Text.UTF8Encoding]::new($false).GetBytes($prefix)
    $payload = [byte[]]::new($prefixBytes.Length + $collectorBytes.Length)
    [Array]::Copy($prefixBytes, 0, $payload, 0, $prefixBytes.Length)
    [Array]::Copy($collectorBytes, 0, $payload, $prefixBytes.Length, $collectorBytes.Length)

    $psi = [Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = $ssh
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    foreach ($arg in $sshArgs) { [void]$psi.ArgumentList.Add($arg) }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $psi
    try {
        if (-not $process.Start()) { throw "could not start production observation probe" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        try { $process.StandardInput.BaseStream.Write($payload, 0, $payload.Length) }
        finally { $process.StandardInput.Close() }
        $process.WaitForExit()
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
        $required = @(
            'observed_at','revision','image_digest','image_revision','migration_version','health','served_revision',
            'server_started_at','restart_count','oom_killed','container_status','builder_generated_at','builder_fresh',
            'cpu_percent','memory_usage','memory_percent','load_average','detail_collected','pressure_lines','pool_busy_events',
            'query_timeout_events','oom_events','restart_events','die_events','settled_fail_observations',
            'die_event_first_epoch','die_event_last_epoch','settled_failure_cluster_observations',
            'settled_unbalanced_failure_cluster_rows'
        )
        if ($IncludeLatency) {
            foreach ($name in @('healthz','landing','stats','wanted')) {
                $required += "latency_${name}_status", "latency_${name}_seconds"
            }
        }
        foreach ($name in $required) {
            if (-not $state.Contains($name)) { throw "production observation evidence is missing $name" }
        }
        foreach ($name in @('restart_count','pressure_lines','pool_busy_events','query_timeout_events','oom_events','restart_events','die_events',
                'die_event_first_epoch','die_event_last_epoch','settled_fail_observations',
                'settled_failure_cluster_observations','settled_unbalanced_failure_cluster_rows')) {
            if ($state[$name] -notmatch '^\d+$') { throw "production observation evidence has malformed $name" }
            $state[$name] = [int64]$state[$name]
        }
        foreach ($name in @('builder_fresh','oom_killed','detail_collected')) {
            if ($state[$name] -notin @('true','false')) { throw "production observation evidence has malformed $name" }
            $state[$name] = $state[$name] -eq 'true'
        }
        if ($IncludeLatency) {
            foreach ($name in @('healthz','landing','stats','wanted')) {
                if ($state["latency_${name}_status"] -notmatch '^\d{3}$') {
                    throw "production observation evidence has malformed $name latency status"
                }
                if ($state["latency_${name}_seconds"] -ne 'unavailable' -and
                    $state["latency_${name}_seconds"] -notmatch '^\d+(?:\.\d+)?$') {
                    throw "production observation evidence has malformed $name latency"
                }
            }
        }
        return $state
    } finally {
        [Array]::Clear($payload, 0, $payload.Length)
        [Array]::Clear($prefixBytes, 0, $prefixBytes.Length)
        $stdout = $null
        $stderr = $null
        $process.Dispose()
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
    foreach ($name in @('pressureLines','poolBusyEvents','queryTimeoutEvents')) {
        $source = switch ($name) {
            'pressureLines' { 'pressure_lines' }
            'poolBusyEvents' { 'pool_busy_events' }
            'queryTimeoutEvents' { 'query_timeout_events' }
        }
        if ($Sample[$source] -gt $Evidence.pressure[$name]) { $Evidence.pressure[$name] = $Sample[$source] }
    }
    foreach ($pair in @(@('restart_events','restart'), @('oom_events','oom'), @('die_events','die'))) {
        if ($Sample[$pair[0]] -gt $Evidence.events[$pair[1]]) {
            $Evidence.events[$pair[1]] = $Sample[$pair[0]]
        }
    }
}

function Get-SettledInvariantAnomalies([Collections.IDictionary]$Sample) {
    $found = [Collections.Generic.List[string]]::new()
    if (-not $Sample.detail_collected) {
        $found.Add("settled failure-cluster invariant was not collected")
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

function Write-ObservationEvidence([Collections.IDictionary]$Evidence) {
    foreach ($path in @($EvidencePath, $SummaryPath)) {
        $parent = Split-Path -Parent $path
        if (-not [string]::IsNullOrWhiteSpace($parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    }
    $json = $Evidence | ConvertTo-Json -Depth 12
    [IO.File]::WriteAllText($EvidencePath, $json + "`n", [Text.UTF8Encoding]::new($false))

    $latencyLines = [Collections.Generic.List[string]]::new()
    $latencyPaths = [ordered]@{ healthz = '/healthz'; landing = '/'; stats = '/v1/stats'; wanted = '/v1/wanted' }
    foreach ($name in $latencyPaths.Keys) {
        $latency = $Evidence.latencies[$name]
        if ($null -ne $latency) { $latencyLines.Add("| $($latencyPaths[$name]) | $($latency.status) | $($latency.seconds) |") }
    }
    $anomalyText = if ($Evidence.anomalies.Count -eq 0) { "None" } else { ($Evidence.anomalies | ForEach-Object { "- $_" }) -join "`n" }
    $summary = @"
## Post-deploy observation: $($Evidence.conclusion.ToUpperInvariant())

- Deployment run: $DeploymentRunId
- Tracking issue: $TrackingIssue
- Target SHA: $ExpectedRevision
- Image digest: $($Evidence.imageDigest)
- Migration: $expectedMigration
- Builder converged: $($Evidence.converged)
- Builder generatedAt: $($Evidence.builderGeneratedAt)
- Builder completion: $($Evidence.builderCompletionSeconds) seconds
- Observation samples: $($Evidence.samples.Count)
- Peak server CPU: $($Evidence.pressure.peakCpuPercent)%
- Peak server memory: $($Evidence.pressure.peakMemoryPercent)%
- Peak host load (1m): $($Evidence.pressure.peakLoad1)
- Pool-pressure log lines: $($Evidence.pressure.pressureLines)
- Pool-busy observations: $($Evidence.pressure.poolBusyEvents)
- Query-timeout observations: $($Evidence.pressure.queryTimeoutEvents)
- Restart events: $($Evidence.events.restart)
- OOM events: $($Evidence.events.oom)
- Container exit events: $($Evidence.events.die)
- Settled FAIL observations: $($Evidence.settledInvariant.failObservations)
- Settled failure-cluster observations: $($Evidence.settledInvariant.failureClusterObservations)
- Settled unbalanced cluster rows: $($Evidence.settledInvariant.unbalancedRows)

### Representative latency after convergence

| Fixed path | HTTP | Seconds |
| --- | ---: | ---: |
$($latencyLines -join "`n")

### Anomalies

$anomalyText
"@
    [IO.File]::WriteAllText($SummaryPath, $summary.TrimEnd() + "`n", [Text.UTF8Encoding]::new($false))
}

$evidence = [ordered]@{
    schemaVersion = 1
    deploymentRunId = $DeploymentRunId
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
    completedAt = ""
    conclusion = "failure"
    converged = $false
    imageDigest = $ExpectedImageDigest
    builderGeneratedAt = ""
    builderCompletionSeconds = $null
    samples = [Collections.Generic.List[object]]::new()
    latencies = [ordered]@{}
    settledInvariant = [ordered]@{
        failObservations = $null
        failureClusterObservations = $null
        unbalancedRows = $null
    }
    pressure = [ordered]@{
        peakCpuPercent = $null
        peakMemoryPercent = $null
        peakLoad1 = $null
        pressureLines = 0
        poolBusyEvents = 0
        queryTimeoutEvents = 0
    }
    events = [ordered]@{ restart = 0; oom = 0; die = 0 }
    anomalies = [Collections.Generic.List[string]]::new()
}

$observationFailure = $null
try {
    for ($attempt = 1; $attempt -le $BuilderPollAttempts; $attempt++) {
        $sample = Read-ObservationSample $false $false
        $evidence.samples.Add($sample)
        $evidence.builderGeneratedAt = $sample.builder_generated_at
        foreach ($anomaly in (Get-StateAnomalies $sample $ExpectedImageDigest)) { $evidence.anomalies.Add($anomaly) }
        Update-PressureEvidence $evidence $sample

        if ($evidence.anomalies.Count -ne 0) { break }
        if ($sample.builder_fresh) {
            $evidence.converged = $true
            $evidence.builderGeneratedAt = $sample.builder_generated_at
            try {
                $serverStart = [DateTimeOffset]::Parse($ExpectedServerStartedAt, [Globalization.CultureInfo]::InvariantCulture)
                $builderCompleted = [DateTimeOffset]::Parse($sample.builder_generated_at, [Globalization.CultureInfo]::InvariantCulture)
                $completionSeconds = [int64][Math]::Floor(($builderCompleted - $serverStart).TotalSeconds)
                if ($completionSeconds -lt 0) { throw "negative builder completion interval" }
                $evidence.builderCompletionSeconds = $completionSeconds
            } catch {
                $evidence.anomalies.Add("builder completion timestamps are malformed")
            }
            break
        }
        if ($attempt -lt $BuilderPollAttempts) { Start-Sleep -Seconds $BuilderPollSeconds }
    }

    if (-not $evidence.converged -and $evidence.anomalies.Count -eq 0) {
        $evidence.anomalies.Add("builder did not converge within the bounded 80-minute observation window")
    }
    if (-not $evidence.converged) {
        # Expensive log/event scans run once at the terminal failure rather
        # than on every cheap freshness poll.
        $final = Read-ObservationSample $false $true
        $evidence.samples.Add($final)
        Update-PressureEvidence $evidence $final
        foreach ($anomaly in (Get-StateAnomalies $final $ExpectedImageDigest)) {
            if (-not $evidence.anomalies.Contains($anomaly)) { $evidence.anomalies.Add($anomaly) }
        }
    } elseif ($evidence.anomalies.Count -eq 0) {
        # Latency is deliberately sampled only after convergence so these
        # representative requests do not compete with the first full pass.
        $final = Read-ObservationSample $true $true
        $evidence.samples.Add($final)
        Update-PressureEvidence $evidence $final
        foreach ($anomaly in (Get-StateAnomalies $final $ExpectedImageDigest)) { $evidence.anomalies.Add($anomaly) }
        $evidence.settledInvariant.failObservations = $final.settled_fail_observations
        $evidence.settledInvariant.failureClusterObservations = $final.settled_failure_cluster_observations
        $evidence.settledInvariant.unbalancedRows = $final.settled_unbalanced_failure_cluster_rows
        foreach ($anomaly in (Get-SettledInvariantAnomalies $final)) { $evidence.anomalies.Add($anomaly) }
        foreach ($name in @('healthz','landing','stats','wanted')) {
            $status = $final["latency_${name}_status"]
            $seconds = $final["latency_${name}_seconds"]
            $evidence.latencies[$name] = [ordered]@{ status = $status; seconds = $seconds }
            if ($status -ne '200') { $evidence.anomalies.Add("representative /$name latency probe returned HTTP $status") }
        }
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
    $evidence.completedAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
    try { Write-ObservationEvidence $evidence }
    finally { [Array]::Clear($collectorBytes, 0, $collectorBytes.Length) }
}

if ($null -ne $observationFailure) { throw "$observationFailure; see $EvidencePath and $SummaryPath" }
Write-Output "post-deploy observation evidence: $EvidencePath"
Write-Output "post-deploy observation summary: $SummaryPath"
