package lightsail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostCommitEvidenceAndLostControllerResponse(t *testing.T) {
	pwsh := os.Getenv("CSX_TEST_PWSH")
	if pwsh == "" {
		pwsh, _ = exec.LookPath("pwsh")
	}
	if pwsh == "" {
		t.Skip("PowerShell 7 required")
	}
	helper, err := filepath.Abs("offline-migration.ps1")
	if err != nil {
		t.Fatal(err)
	}
	program := `param([string]$Helper)
$ErrorActionPreference='Stop'
. $Helper
$deployLockOwner='a'*32
$OperationalRevision='b'*40
$revision='c'*40
$tag='v0.1.151'
$expectedMigration='0036_builder_projections.sql'
$migrationImageDigest='sha256:' + ('d'*64)
$DeploymentEvidence=@{}
$valid=@{
    phase='committed'; conclusion='success'; acceptanceAuthority='host'
    controllerSmoke='host-verified'; owner=$deployLockOwner
    operationalSha=$OperationalRevision; targetSha=$revision; servedRevision=$revision
    releaseTag=$tag; migrationLedger=@{version=$expectedMigration;count=37}
    migrationVerification='pass'; imageDigest=$migrationImageDigest
    health='ok'; smoke='pass'; cleanup='pass'
    serverStartedAt='2026-09-09T05:00:00.123Z'; phaseTimings=@{}
}
$migrationState='/fixture/owned-state'
$MigrationEvidencePath=Join-Path $PSScriptRoot 'migration-evidence.json'
# Exercise the real JSON/SSH boundary. Direct PSCustomObject fixtures bypass
# ConvertFrom-Json's automatic DateTime conversion and hid this production bug.
function Invoke-RemoteScript {
    param([string]$RemoteScript, [int]$Timeout)
    if ($Timeout -ne 15 -or -not $RemoteScript.Contains("$migrationState/evidence.json")) {
        throw 'unexpected evidence read'
    }
    return $script:hostEvidenceJSON -split [char]10
}
foreach ($timestamp in @('2026-09-11T14:15:58.802547Z', '2026-09-09T05:00:00Z',
    '2026-09-09T05:00:00.123456789Z', '2026-09-09T05:00:00.123456789+00:00')) {
    $fixture=$valid.Clone(); $fixture.serverStartedAt=$timestamp
    $script:hostEvidenceJSON=$fixture | ConvertTo-Json -Depth 10
    $result=Read-CSXMigrationEvidence
    if ($result.serverStartedAt -isnot [string] -or $result.serverStartedAt -cne $timestamp) {
        throw 'host timestamp changed at the JSON boundary'
    }
    Set-CSXHostDeploymentEvidence $result
    if ($DeploymentEvidence.serverStartedAt -isnot [string] -or $DeploymentEvidence.serverStartedAt -cne $timestamp) {
        throw 'published host timestamp changed'
    }
    if ([IO.File]::ReadAllText($MigrationEvidencePath) -cne ($script:hostEvidenceJSON.Trim() + [char]10)) {
        throw 'raw host evidence was rewritten'
    }
}
if ($DeploymentEvidence.rollback -ne 'not-needed' -or
    $DeploymentEvidence.deployedSha -ne $revision -or -not $migrationRecoveryVerified) {
    throw 'valid host acceptance not published'
}
foreach ($case in @(
    @{name='missing'; value=$null}, @{name='null'; value=$null},
    @{name='number'; value=20260911}, @{name='boolean'; value=$true},
    @{name='array'; value=@($valid.serverStartedAt)}, @{name='object'; value=@{timestamp=$valid.serverStartedAt}},
    @{name='non-UTC offset'; value='2026-09-09T14:00:00.123+09:00'},
    @{name='missing zone'; value='2026-09-09T05:00:00.123'},
    @{name='excess precision'; value='2026-09-09T05:00:00.1234567890Z'})) {
    $fixture=$valid.Clone(); $fixture.serverStartedAt=$case.value
    if ($case.name -eq 'missing') { $fixture.Remove('serverStartedAt') }
    $script:hostEvidenceJSON=$fixture | ConvertTo-Json -Depth 10
    $rejected=$false
    try { Set-CSXHostDeploymentEvidence (Read-CSXMigrationEvidence) } catch { $rejected=$true }
    if (-not $rejected) { throw "invalid JSON timestamp accepted: $($case.name)" }
}
$typedTimestamp=$valid.Clone(); $typedTimestamp.serverStartedAt=[DateTime]::UtcNow
$rejected=$false
try { Set-CSXHostDeploymentEvidence ([pscustomobject]$typedTimestamp) } catch { $rejected=$true }
if (-not $rejected) { throw 'typed timestamp accepted without exact JSON string evidence' }
foreach ($field in @('phase','conclusion','acceptanceAuthority','controllerSmoke','owner',
    'operationalSha','targetSha','servedRevision','releaseTag','migrationVerification',
    'imageDigest','health','smoke','cleanup','serverStartedAt')) {
    $broken=$valid.Clone()
    $broken[$field]='wrong'
    $rejected=$false
    try { Set-CSXHostDeploymentEvidence ([pscustomobject]$broken) } catch { $rejected=$true }
    if (-not $rejected) { throw "invalid evidence accepted: $field" }
}
foreach ($ledger in @(@{version=$expectedMigration;count=36},@{version='wrong';count=37})) {
    $broken=$valid.Clone();$broken.migrationLedger=$ledger
    $rejected=$false
    try { Set-CSXHostDeploymentEvidence ([pscustomobject]$broken) } catch { $rejected=$true }
    if (-not $rejected) { throw 'wrong migration ledger accepted' }
}
$expectedMigration='0037_slow_query_indexes.sql'
$valid37 = $valid.Clone()
$valid37.migrationLedger = @{version=$expectedMigration;count=38}
Set-CSXHostDeploymentEvidence ([pscustomobject]$valid37)
foreach ($ledger in @(@{version=$expectedMigration;count=37},@{version='0036_builder_projections.sql';count=38},@{version='wrong';count=38})) {
    $broken=$valid37.Clone();$broken.migrationLedger=$ledger
    $rejected=$false
    try { Set-CSXHostDeploymentEvidence ([pscustomobject]$broken) } catch { $rejected=$true }
    if (-not $rejected) { throw 'wrong migration ledger accepted for 0037' }
}
$expectedMigration='0036_builder_projections.sql'
$broken=$valid.Clone();$broken.imageDigest='sha256:'+('e'*64)
$rejected=$false
try { Set-CSXHostDeploymentEvidence ([pscustomobject]$broken) } catch { $rejected=$true }
if (-not $rejected) { throw 'different valid-shaped image digest accepted' }
# A lost controller result after commit must never stop a validated service.
$migrationSupervisorStarted=$true
function Read-CSXMigrationEvidence { return [pscustomobject]$valid }
function Wait-CSXMigrationTerminal { $script:migrationSupervisorTerminal=$true; return [pscustomobject]$valid }
function Invoke-RemoteScript { throw 'committed host was sent a stop command' }
$result=Resolve-CSXOfflineMigrationOutcome
Set-CSXHostDeploymentEvidence $result
if (-not $migrationSupervisorTerminal) { throw 'unit termination not checked' }
# A transient transport error while a valid migration is pending must also
# send no stop request; only host critical failures own rollback.
$pending=$valid.Clone();$pending.phase='migrating';$pending.conclusion='pending'
function Read-CSXMigrationEvidence { return [pscustomobject]$pending }
function Wait-CSXMigrationTerminal { return [pscustomobject]$valid }
$result=Resolve-CSXOfflineMigrationOutcome
if ($result.phase -ne 'committed') { throw 'pending host outcome not observed' }
foreach ($bad in @([pscustomobject]@{}, [pscustomobject]@{phase='migrating';owner='foreign'})) {
    function Read-CSXMigrationEvidence { return $bad }
    $rejected=$false
    try { Resolve-CSXOfflineMigrationOutcome | Out-Null } catch {
        if ($_.Exception.Message -notmatch 'cannot prove an owned outcome') { throw }
        $rejected=$true
    }
    if (-not $rejected) { throw 'unknown host evidence authorized stop' }
}
Write-Output 'PASS host commit transport recovery'
`
	path := filepath.Join(t.TempDir(), "host-commit.ps1")
	if err := os.WriteFile(path, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, pwsh, "-NoProfile", "-File", path, helper).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "PASS host commit transport recovery") {
		t.Fatalf("host evidence/transport regression: %v\n%s", err, output)
	}
}
