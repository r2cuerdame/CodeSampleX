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
Set-CSXHostDeploymentEvidence ([pscustomobject]$valid)
if ($DeploymentEvidence.rollback -ne 'not-needed' -or
    $DeploymentEvidence.deployedSha -ne $revision -or -not $migrationRecoveryVerified) {
    throw 'valid host acceptance not published'
}
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
