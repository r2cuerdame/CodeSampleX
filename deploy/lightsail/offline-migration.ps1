# Shared controller for the host-owned offline migration and smoke lease.
# Dot-sourced by deploy.ps1 after its pinned SSH helpers have been defined.
function Read-CSXMigrationEvidence {
    $raw = Invoke-RemoteScript "set -eu; if [ -f $migrationState/evidence.json ]; then cat $migrationState/evidence.json; else printf '{}'; fi" 15
    $json = ($raw -join "`n").Trim()
    $value = $json | ConvertFrom-Json
    if ($MigrationEvidencePath -ne "") {
        [IO.File]::WriteAllText($MigrationEvidencePath, $json + "`n", [Text.UTF8Encoding]::new($false))
    }
    return $value
}

function Wait-CSXMigrationTerminal {
    $deadline = [DateTime]::UtcNow.AddSeconds(240)
    do {
        $state = (Invoke-RemoteScript "systemctl show $migrationUnit --property=ActiveState --value 2>/dev/null || true" 15 | Out-String).Trim()
        if ($state -notin @("active", "activating", "deactivating")) {
            $script:migrationSupervisorTerminal = $true
            return Read-CSXMigrationEvidence
        }
        if ([DateTime]::UtcNow -ge $deadline) { throw "host migration supervisor has not stopped; deployment lock retained" }
        Start-Sleep -Seconds 2
    } while ($true)
}

function Resolve-CSXOfflineMigrationOutcome {
    if (-not $migrationSupervisorStarted) { return $null }
    # Healthy committed acceptance must survive a lost controller response.
    $observed = Read-CSXMigrationEvidence
    if ($observed.owner -ne $deployLockOwner -or $observed.operationalSha -ne $OperationalRevision -or
        $observed.targetSha -ne $revision -or $observed.imageDigest -ne $migrationImageDigest -or
        $observed.phase -notin @("preflight", "quiescing", "migrating", "activating", "committed",
            "rolling-back", "rolled-back", "rollback-failed")) {
        throw "host migration evidence cannot prove an owned outcome; lock retained"
    }
    if ($observed.phase -eq "committed") { return Wait-CSXMigrationTerminal }
    # Transport failure is not a migration/startup failure. The host owns
    # its deadlines and finalizer; observe it without cancelling healthy work.
    return Wait-CSXMigrationTerminal
}

function Start-CSXOfflineMigration {
    Set-DeployPhase migration-setup 60
    $script:migrationState = "/opt/codesamplex/deploy/.migration-$deployLockOwner"
    $script:migrationUnit = "csx-migration-$deployLockOwner.service"
    # Every directory/file is scoped to this lock owner; no stale helper is adopted.
    $prepare = @'
set -eu
test "$(cat /opt/codesamplex/.deploy-lock/owner)" = __OWNER__
command -v python3 >/dev/null
sudo -n systemd-run --version >/dev/null
umask 077
mkdir __STATE__
chmod 0700 __STATE__
'@
    Invoke-RemoteScript ($prepare.Replace('__OWNER__', $deployLockOwner).Replace('__STATE__', $migrationState)) | Out-Null
    $image = (Invoke-Remote "docker image inspect codesamplex/csx-server:latest --format '{{.Id}}'" | Select-Object -First 1).Trim()
    if ($image -notmatch '^sha256:[0-9a-f]{64}$') { throw "invalid migration image identity" }
    $script:migrationImageDigest = $image
    $config = @{
        targetSha = $revision
        previousSha = $productionStateParts[0]
        operationalSha = $OperationalRevision
        imageDigest = $image
        previousImageDigest = $productionStateParts[1]
        expectedMigration = $expectedMigration
        expectedReleaseTag = $tag
        migrationTimeoutSeconds = $MigrationTimeoutSeconds
    } | ConvertTo-Json -Depth 5
    $restoreDist = if ($distPromoted) { "1" } else { "0" }
    $files = @{
        "config.json" = $config
        "rollback-server.sh" = $rollbackServerTemplate.Replace('__CSX_RESTORE_DIST__', $restoreDist)
        "rollback-caddy.sh" = $rollbackCaddyTemplate
    }
    foreach ($name in $files.Keys) {
        $local = Join-Path ([IO.Path]::GetTempPath()) "csx-migration-$deployLockOwner-$name"
        [IO.File]::WriteAllText($local, $files[$name] + "`n", [Text.UTF8Encoding]::new($false))
        try { Copy-Remote $local "$migrationState/$name" }
        finally { Remove-Item -LiteralPath $local -Force }
    }
    Copy-Remote (Join-Path $PSScriptRoot "offline-migration.py") "$migrationState/offline-migration.py"
    $runtime = $MigrationTimeoutSeconds + 480
    $launch = @'
set -eu
chmod 0600 __STATE__/*
sudo -n systemd-run --quiet --collect --unit=__UNIT__ \
  --property=Type=exec --property=User=__USER__ \
  --property=RuntimeMaxSec=__RUNTIME__ --property=TimeoutStopSec=240 \
  --property=KillMode=mixed \
  --property="ExecStopPost=/usr/bin/python3 __STATE__/offline-migration.py finalize __OWNER__" \
  /usr/bin/python3 __STATE__/offline-migration.py run __OWNER__
'@
    $launch = $launch.Replace('__STATE__', $migrationState).Replace('__UNIT__', $migrationUnit).Replace('__USER__', $User).Replace('__RUNTIME__', [string]$runtime).Replace('__OWNER__', $deployLockOwner)
    # Treat an ambiguous SSH launch result as owned/running until systemd proves
    # otherwise. A disconnect after daemon submission must not release the lock.
    $script:migrationSupervisorStarted = $true
    $script:migrationSupervisorTerminal = $false
    $script:migrationRecoveryVerified = $false
    Invoke-RemoteScript $launch | Out-Null
    Set-DeployPhase offline-migration ($MigrationTimeoutSeconds + 240)
    $activationObserved = $false
    do {
        $observed = Read-CSXMigrationEvidence
        if ($observed.phase -in @("activating", "committed") -and -not $activationObserved) {
            # Separate from migration; host independently enforces the same
            # 180s from startup, including exact acceptance. Never reset it.
            Set-DeployPhase activation-smoke 180
            $activationObserved = $true
        }
        if ($observed.phase -eq "committed") {
            return Wait-CSXMigrationTerminal
        }
        if ($observed.conclusion -eq "failure") { throw "host offline migration failed; exact cleanup/rollback required" }
        [void](Get-DeployBudgetSeconds 15)
        Start-Sleep -Seconds 2
    } while ($true)
}

function Set-CSXHostDeploymentEvidence($Result) {
    if ($Result.phase -ne "committed" -or $Result.conclusion -ne "success" -or
        $Result.acceptanceAuthority -ne "host" -or $Result.controllerSmoke -ne "host-verified" -or
        $Result.owner -ne $deployLockOwner -or $Result.operationalSha -ne $OperationalRevision -or
        $Result.targetSha -ne $revision -or $Result.servedRevision -ne $revision -or
        $Result.releaseTag -cne $tag -or $Result.migrationLedger.version -ne $expectedMigration -or
        $Result.migrationLedger.count -ne 37 -or $Result.migrationVerification -ne "pass" -or
        $Result.imageDigest -ne $migrationImageDigest -or $Result.imageDigest -notmatch '^sha256:[0-9a-f]{64}$' -or
        $Result.health -ne "ok" -or $Result.smoke -ne "pass" -or $Result.cleanup -ne "pass" -or
        $Result.serverStartedAt -notmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|\+00:00)$') {
        throw "host exact activation acceptance evidence is incomplete or mismatched"
    }
    $DeploymentEvidence.deployedSha = $Result.targetSha
    $DeploymentEvidence.imageDigest = $Result.imageDigest
    $DeploymentEvidence.migrationVersion = $Result.migrationLedger.version
    $DeploymentEvidence.servedRevision = $Result.servedRevision
    $DeploymentEvidence.serverStartedAt = $Result.serverStartedAt
    $DeploymentEvidence.health = "ok"
    $DeploymentEvidence.smoke = "pass"
    $DeploymentEvidence.rollback = "not-needed"
    $DeploymentEvidence.acceptanceAuthority = "host"
    $DeploymentEvidence.hostPhaseTimings = $Result.phaseTimings
    $script:migrationRecoveryVerified = $true
}
