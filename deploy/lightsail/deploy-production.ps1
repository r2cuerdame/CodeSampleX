param(
    [Parameter(Mandatory)][string]$Ip,
    [Parameter(Mandatory)][string]$KeyPath,
    [Parameter(Mandatory)][string]$KnownHostsPath,
    [Parameter(Mandatory)][string]$ExpectedRevision,
    [Parameter(Mandatory)][string]$ExpectedPreviousRevision,
    [Parameter(Mandatory)][string]$LinearIssue,
    [Parameter(Mandatory)][string]$EvidencePath,
    [string]$User = "ubuntu"
)

$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$collector = Join-Path $PSScriptRoot "collect-production-evidence.sh"
$ssh = (Get-Command ssh -ErrorAction Stop).Source

foreach ($sha in @($ExpectedRevision, $ExpectedPreviousRevision)) {
    if ($sha -notmatch '^[0-9a-f]{40}$') { throw "production revisions must be lowercase immutable SHAs" }
}
if ($LinearIssue -notmatch '^[A-Z][A-Z0-9]+-[0-9]+$') { throw "invalid Linear issue identifier" }
foreach ($path in @($KeyPath, $KnownHostsPath, $collector)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "required production deploy file is missing" }
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

function Read-ProductionState {
    $bytes = [IO.File]::ReadAllBytes($collector)
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
        if (-not $process.Start()) { throw "could not start production evidence probe" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        try { $process.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length) }
        finally { $process.StandardInput.Close() }
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) { throw "production evidence probe failed ($($process.ExitCode))" }
        $state = @{}
        foreach ($line in ($stdout -split "`r?`n")) {
            if ([string]::IsNullOrWhiteSpace($line)) { continue }
            $pair = $line -split '=', 2
            if ($pair.Count -ne 2) { throw "malformed production evidence" }
            $state[$pair[0]] = $pair[1]
        }
        foreach ($required in @('revision','image_digest','image_revision','migration_version','migration_count','health','invariants','modern_failure_clusters','failure_evidence_quality')) {
            if (-not $state.ContainsKey($required)) { throw "production evidence is missing $required" }
        }
        $state['invariants'] = $state['invariants'] | ConvertFrom-Json
        $state['failure_evidence_quality'] = $state['failure_evidence_quality'] | ConvertFrom-Json
        return $state
    } finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
        $stdout = $null
        $stderr = $null
        $process.Dispose()
    }
}

function Write-Evidence([hashtable]$Evidence) {
    $parent = Split-Path -Parent $EvidencePath
    if (-not [string]::IsNullOrWhiteSpace($parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    $json = $Evidence | ConvertTo-Json -Depth 8
    [IO.File]::WriteAllText($EvidencePath, $json + "`n", [Text.UTF8Encoding]::new($false))
}

$evidence = @{
    schemaVersion = 1
    workflowRunId = $env:GITHUB_RUN_ID
    workflowRunUrl = if ($env:GITHUB_SERVER_URL -and $env:GITHUB_REPOSITORY -and $env:GITHUB_RUN_ID) {
        "$($env:GITHUB_SERVER_URL)/$($env:GITHUB_REPOSITORY)/actions/runs/$($env:GITHUB_RUN_ID)"
    } else { "" }
    linearIssue = $LinearIssue
    targetSha = $ExpectedRevision
    previousProductionSha = $ExpectedPreviousRevision
    conclusion = "failure"
    deployedSha = ""
    imageDigest = ""
    migrationVersion = ""
    migrationCount = 0
    health = "unknown"
    smoke = "failed"
    rollback = "unknown"
    invariants = @{ before = $null; after = $null }
    modernFailureClusters = 0
    failureEvidenceQuality = $null
}

$failure = $null
try {
    $before = Read-ProductionState
    if ($before.revision -ne $ExpectedPreviousRevision) {
        throw "production drifted from the ProjectOps rollback SHA"
    }
    $evidence.invariants.before = $before.invariants
    $evidence.previousImageDigest = $before.image_digest

    & (Join-Path $PSScriptRoot "deploy.ps1") `
        -Ip $Ip -User $User -KeyPath $KeyPath -KnownHostsPath $KnownHostsPath `
        -ExpectedRevision $ExpectedRevision -ExpectedPreviousRevision $ExpectedPreviousRevision `
        -RequireNoLegacyAccessLogs

    $after = Read-ProductionState
    $evidence.deployedSha = $after.revision
    $evidence.imageDigest = $after.image_digest
    $evidence.migrationVersion = $after.migration_version
    $evidence.migrationCount = [int]$after.migration_count
    $evidence.health = $after.health
    $evidence.invariants.after = $after.invariants
    $evidence.modernFailureClusters = [int]$after.modern_failure_clusters
    $evidence.failureEvidenceQuality = $after.failure_evidence_quality
    if ($after.revision -ne $ExpectedRevision -or $after.image_revision -ne $ExpectedRevision) {
        throw "served SHA does not match the requested immutable commit"
    }
    if ($after.health -ne "ok") { throw "post-deploy health is not ok" }
    $evidence.conclusion = "success"
    $evidence.smoke = "pass"
    $evidence.rollback = "not-needed"
} catch {
    $failure = $_
    try {
        $current = Read-ProductionState
        $evidence.deployedSha = $current.revision
        $evidence.imageDigest = $current.image_digest
        $evidence.migrationVersion = $current.migration_version
        $evidence.migrationCount = [int]$current.migration_count
        $evidence.health = $current.health
        $evidence.invariants.after = $current.invariants
        $evidence.modernFailureClusters = [int]$current.modern_failure_clusters
        $evidence.failureEvidenceQuality = $current.failure_evidence_quality
        $evidence.rollback = if ($current.revision -eq $ExpectedPreviousRevision -and $current.health -eq "ok") { "succeeded" } else { "failed" }
    } catch {
        $evidence.rollback = "unverified"
    }
} finally {
    Write-Evidence $evidence
}

if ($null -ne $failure) { throw $failure }
Write-Output "production evidence: $EvidencePath"
