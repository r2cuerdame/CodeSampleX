# Shared controller for the host-owned offline migration and smoke lease.
# Dot-sourced by deploy.ps1 after its pinned SSH helpers have been defined.
function Read-CSXMigrationEvidence {
    $raw = Invoke-RemoteScript "set -eu; if [ -f $migrationState/evidence.json ]; then cat $migrationState/evidence.json; else printf '{}'; fi"
    $json = ($raw -join "`n").Trim()
    $value = $json | ConvertFrom-Json
    if ($MigrationEvidencePath -ne "") {
        [IO.File]::WriteAllText($MigrationEvidencePath, $json + "`n", [Text.UTF8Encoding]::new($false))
    }
    return $value
}

function Wait-CSXMigrationTerminal {
    $deadline = [DateTime]::UtcNow.AddSeconds(510)
    do {
        $state = (Invoke-Remote "systemctl show $migrationUnit --property=ActiveState --value 2>/dev/null || true" | Out-String).Trim()
        if ($state -notin @("active", "activating", "deactivating")) {
            $script:migrationSupervisorTerminal = $true
            return Read-CSXMigrationEvidence
        }
        if ([DateTime]::UtcNow -ge $deadline) { throw "host migration supervisor has not stopped; deployment lock retained" }
        Start-Sleep -Seconds 2
    } while ($true)
}

function Stop-CSXOfflineMigration {
    if (-not $migrationSupervisorStarted) { return $null }
    $stopUnit = @'
set -eu
state=$(systemctl show __UNIT__ --property=ActiveState --value 2>/dev/null || true)
case "$state" in active|activating|deactivating) sudo -n systemctl stop --no-block __UNIT__ ;; esac
'@
    Invoke-RemoteScript ($stopUnit.Replace('__UNIT__', $migrationUnit)) | Out-Null
    return Wait-CSXMigrationTerminal
}

function Start-CSXOfflineMigration {
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
    $config = @{
        targetSha = $revision
        previousSha = $productionStateParts[0]
        operationalSha = $OperationalRevision
        imageDigest = $image
        previousImageDigest = $productionStateParts[1]
        expectedMigration = $expectedMigration
        migrationTimeoutSeconds = $MigrationTimeoutSeconds
    } | ConvertTo-Json -Depth 5
    $restoreDist = if ($distPromoted) { "1" } else { "0" }
    $files = @{
        "config.json" = $config
        "rollback-server.sh" = $rollbackServerTemplate.Replace('__CSX_RESTORE_DIST__', $restoreDist)
        "rollback-caddy.sh" = $rollbackCaddyTemplate
        "safe-log-smoke.sh" = (Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot "safe-log-smoke.sh")).Replace('__CSX_DOMAIN__', $Domain)
    }
    foreach ($name in $files.Keys) {
        $local = Join-Path ([IO.Path]::GetTempPath()) "csx-migration-$deployLockOwner-$name"
        [IO.File]::WriteAllText($local, $files[$name] + "`n", [Text.UTF8Encoding]::new($false))
        try { Copy-Remote $local "$migrationState/$name" }
        finally { Remove-Item -LiteralPath $local -Force }
    }
    Copy-Remote (Join-Path $PSScriptRoot "offline-migration.py") "$migrationState/offline-migration.py"
    $runtime = $MigrationTimeoutSeconds + 1080
    $launch = @'
set -eu
chmod 0600 __STATE__/*
sudo -n systemd-run --quiet --collect --unit=__UNIT__ \
  --property=Type=exec --property=User=__USER__ \
  --property=RuntimeMaxSec=__RUNTIME__ --property=TimeoutStopSec=480 \
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
    $deadline = [DateTime]::UtcNow.AddSeconds($MigrationTimeoutSeconds + 480)
    do {
        $observed = Read-CSXMigrationEvidence
        if ($observed.phase -eq "candidate-ready") {
            Write-Output "offline migration passed; candidate ready; host awaits final smoke acknowledgement"
            return
        }
        if ($observed.conclusion -eq "failure") { throw "host offline migration failed; exact cleanup/rollback required" }
        if ([DateTime]::UtcNow -ge $deadline) { throw "host offline migration readiness deadline exceeded" }
        Start-Sleep -Seconds 5
    } while ($true)
}

function Complete-CSXOfflineMigration {
    $observed = Read-CSXMigrationEvidence
    if ($observed.phase -ne "candidate-ready") { throw "host does not hold a candidate-ready smoke lease" }
    $ack = @{
        owner = $deployLockOwner
        operationalSha = $OperationalRevision
        targetSha = $revision
        imageDigest = $observed.imageDigest
    } | ConvertTo-Json -Compress
    $local = Join-Path ([IO.Path]::GetTempPath()) "csx-migration-$deployLockOwner-commit.json"
    [IO.File]::WriteAllText($local, $ack + "`n", [Text.UTF8Encoding]::new($false))
    try {
        Copy-Remote $local "$migrationState/commit.pending"
        Invoke-Remote "chmod 0600 $migrationState/commit.pending && mv $migrationState/commit.pending $migrationState/commit.json" | Out-Null
    } finally { Remove-Item -LiteralPath $local -Force }
    $result = Wait-CSXMigrationTerminal
    if ($result.phase -ne "committed" -or $result.controllerSmoke -ne "acknowledged") {
        throw "host did not accept the exact final smoke acknowledgement"
    }
    $script:migrationRecoveryVerified = $true
}
