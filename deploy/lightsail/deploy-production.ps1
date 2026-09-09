param(
    [Parameter(Mandatory)][string]$Ip,
    [Parameter(Mandatory)][string]$KeyPath,
    [Parameter(Mandatory)][string]$KnownHostsPath,
    [Parameter(Mandatory)][string]$ExpectedRevision,
    [Parameter(Mandatory)][string]$ExpectedPreviousRevision,
    [Alias("LinearIssue")]
    [Parameter(Mandatory)][string]$TrackingIssue,
    [Parameter(Mandatory)][string]$EvidencePath,
    [string]$SourceRepoPath = "",
    [string]$OperationalRevision = "",
    [ValidateRange(60,1800)][int]$MigrationTimeoutSeconds = 1200,
    [string]$User = "ubuntu"
)
$ErrorActionPreference = "Stop"
if ($PSVersionTable.PSVersion.Major -lt 7) { throw "deployment requires PowerShell 7" }
. (Join-Path $PSScriptRoot "deployment-source.ps1")
$source = Resolve-CSXDeploymentSource $SourceRepoPath $ExpectedRevision $OperationalRevision
$OperationalRevision = $source.OperationalRevision
$migrationEvidencePath = $EvidencePath + ".migration.json"
$collector = Join-Path $PSScriptRoot "collect-deploy-identity.sh"
$ssh = (Get-Command ssh -ErrorAction Stop).Source
foreach ($sha in @($ExpectedRevision, $ExpectedPreviousRevision)) {
    if ($sha -notmatch '^[0-9a-f]{40}$') { throw "production revisions must be lowercase immutable SHAs" }
}
if ($TrackingIssue -notmatch '^(?:#?[1-9][0-9]*|https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/issues/[1-9][0-9]*)$') {
    throw "invalid canonical GitHub tracking issue identifier"
}
if ($User -notmatch '^[a-z_][a-z0-9_-]{0,31}$' -or $Ip -notmatch '^[A-Za-z0-9.:-]+$') { throw "unsafe SSH destination" }
foreach ($path in @($KeyPath, $KnownHostsPath, $collector)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "required production deploy file is missing" }
}
function Read-ProductionState {
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $bytes = [Text.Encoding]::UTF8.GetBytes("CSX-IDENTITY-V1`n" + [IO.File]::ReadAllText($collector))
    $psi = [Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = $ssh
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    foreach ($arg in @("-i", $KeyPath, "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=$KnownHostsPath",
        "-o", "ConnectTimeout=5", "${User}@${Ip}", "{ printf '#'; cat; } | timeout --signal=TERM --kill-after=2 20 sh")) {
        [void]$psi.ArgumentList.Add($arg)
    }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $psi
    $started = $false
    try {
        if (-not $process.Start()) { throw "could not start production identity probe" }
        $started = $true
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        $write = $process.StandardInput.BaseStream.WriteAsync($bytes, 0, $bytes.Length)
        if (-not $write.Wait(5000)) { throw "production identity input timed out" }
        $process.StandardInput.Close()
        $remainingMs = [Math]::Max(1, 27000 - [int]$watch.ElapsedMilliseconds)
        if (-not $process.WaitForExit($remainingMs)) { throw "production identity probe exceeded 30s ceiling" }
        if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdout, $stderr), 1000)) { throw "production identity output timed out" }
        if ($process.ExitCode -ne 0) { throw "production identity probe failed ($($process.ExitCode))" }
        $state = @{}
        foreach ($line in ($stdout.Result -split "`r?`n")) {
            if ([string]::IsNullOrWhiteSpace($line)) { continue }
            $pair = $line -split '=', 2
            if ($pair.Count -ne 2 -or $state.ContainsKey($pair[0])) { throw "malformed production identity evidence" }
            $state[$pair[0]] = $pair[1]
        }
        foreach ($key in @('revision', 'image_revision', 'served_revision')) {
            if ($state[$key] -notmatch '^[0-9a-f]{40}$') { throw "production identity missing or malformed: $key" }
        }
        if ($state.image_digest -notmatch '^sha256:[0-9a-f]{64}$' -or $state.health -ne 'ok') { throw "production identity or health is invalid" }
        return $state
    } finally {
        if ($started -and -not $process.HasExited) { $process.Kill($true); [void]$process.WaitForExit(2000) }
        [Array]::Clear($bytes, 0, $bytes.Length)
        $process.Dispose()
        Write-Host "phase=identity-evidence elapsed_seconds=$([Math]::Round($watch.Elapsed.TotalSeconds, 3)) ceiling_seconds=30"
    }
}
$evidence = @{
    schemaVersion = 2
    workflowRunId = $env:GITHUB_RUN_ID
    workflowRunUrl = if ($env:GITHUB_SERVER_URL -and $env:GITHUB_REPOSITORY -and $env:GITHUB_RUN_ID) {
        "$($env:GITHUB_SERVER_URL)/$($env:GITHUB_REPOSITORY)/actions/runs/$($env:GITHUB_RUN_ID)"
    } else { "" }
    trackingIssue = $TrackingIssue
    targetSha = $ExpectedRevision
    operationalSha = $OperationalRevision
    offlineMigration = $null
    previousProductionSha = $ExpectedPreviousRevision
    conclusion = "failure"
    deployedSha = ""
    imageDigest = ""
    migrationVersion = ""
    health = "not-started"
    servedRevision = "unavailable"
    smoke = "not-started"
    rollback = "not-attempted"
    serverStartedAt = ""
    observation = "pending-independent-workflow"
    failureClass = "pre-activation"
    criticalPathCeilingsSeconds = @{ preparation = 600; staging = 300; activation = 240; offlineMigration = $MigrationTimeoutSeconds + 300; activationSmoke = 240; rollback = 300; hostRecovery = 540; cleanup = 60; identityProbe = 30 }
}
$failure = $null
$before = $null
try {
    $before = Read-ProductionState
    if ($before.revision -ne $ExpectedPreviousRevision -or $before.image_revision -ne $ExpectedPreviousRevision -or
        $before.served_revision -ne $ExpectedPreviousRevision) { throw "production drifted from the expected exact rollback SHA" }
    $evidence.previousImageDigest = $before.image_digest

    & (Join-Path $PSScriptRoot "deploy.ps1") `
        -Ip $Ip -User $User -KeyPath $KeyPath -KnownHostsPath $KnownHostsPath `
        -ExpectedRevision $ExpectedRevision -ExpectedPreviousRevision $ExpectedPreviousRevision `
        -SourceRepoPath $source.Repository -OperationalRevision $OperationalRevision -OfflineMigration `
        -MigrationTimeoutSeconds $MigrationTimeoutSeconds -MigrationEvidencePath $migrationEvidencePath `
        -DeploymentEvidence $evidence -RequireNoLegacyAccessLogs

    # Identity, migration, health and representative requests were checked
    # inside deploy.ps1's exact rollback scope. No optional work after commit.
    $evidence.conclusion = "success"
    $evidence.failureClass = "none"
} catch {
    $failure = $_
    $evidence.failure = $_.Exception.Message
    if ($evidence.rollback -in @('attempted', 'succeeded', 'unverified')) {
        $evidence.failureClass = "rollback-critical"
        try {
            $current = Read-ProductionState
            $evidence.deployedSha = $current.revision
            $evidence.imageDigest = $current.image_digest
            $evidence.servedRevision = $current.served_revision
            $evidence.health = $current.health
            if ($current.revision -ne $ExpectedPreviousRevision -or $current.image_revision -ne $ExpectedPreviousRevision -or
                $current.served_revision -ne $ExpectedPreviousRevision -or $current.image_digest -ne $before.image_digest -or
                $evidence.rollback -ne 'succeeded') { throw "exact rollback was not proved" }
        } catch { $evidence.rollback = "unverified" }
    } elseif ($evidence.rollback -eq 'not-needed') {
        # Cleanup failure after commit is incident-only, never an automatic rollback.
        $evidence.failureClass = "incident-only"
    }
} finally {
    if (Test-Path -LiteralPath $migrationEvidencePath) {
        try { $evidence.offlineMigration = Get-Content -Raw -LiteralPath $migrationEvidencePath | ConvertFrom-Json }
        catch { $evidence.migrationEvidenceRead = "unavailable" }
    }
    $parent = Split-Path -Parent $EvidencePath
    if ($parent) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    [IO.File]::WriteAllText($EvidencePath, ($evidence | ConvertTo-Json -Depth 10) + "`n", [Text.UTF8Encoding]::new($false))
}
if ($null -ne $failure) { throw $failure }
Write-Output "production evidence: $EvidencePath"
