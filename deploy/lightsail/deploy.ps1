# Deploy the CodeSampleX stack to the Lightsail host.
# The 2GB host never builds: the linux/amd64 image is built here and shipped.
# Usage: .\deploy.ps1 -Ip <staticIp> -KeyPath <pem> -KnownHostsPath <known_hosts> [-Domain codesamplex.dev]
param(
    [Parameter(Mandatory)][string]$Ip,
    [Parameter(Mandatory)][string]$KeyPath,
    [Parameter(Mandatory)][string]$KnownHostsPath,
    [string]$Domain = "codesamplex.dev",
    [string]$User = "ubuntu",
    [string]$ExpectedRevision = "",
    [string]$ExpectedPreviousRevision = "",
    [switch]$RequireNoLegacyAccessLogs, # Compatibility only; deployments never purge logs.
    [hashtable]$DeploymentEvidence = @{},
    [switch]$SkipImage,
    [switch]$ConfigureAdmin,
    [switch]$RotateAdmin,
    [string]$SourceRepoPath = "",
    [string]$OperationalRevision = "",
    [switch]$OfflineMigration,
    [ValidateRange(60,1800)][int]$MigrationTimeoutSeconds = 1200,
    [string]$MigrationEvidencePath = ""
)
$ErrorActionPreference = "Stop"
# Canonical runners use PowerShell 7 for bounded, exact native argv transport.
if ($PSVersionTable.PSVersion.Major -lt 7) { throw "deployment requires PowerShell 7" }
. (Join-Path $PSScriptRoot "deploy-budget.ps1")
Set-DeployPhase preparation 180
$DeploymentEvidence.phaseTimings = $script:deployPhaseTimings
. (Join-Path $PSScriptRoot "deployment-source.ps1")
if ($ExpectedRevision -eq "") { $ExpectedRevision = (& git -C (Join-Path $PSScriptRoot "../..") rev-parse HEAD).Trim() }
$source = Resolve-CSXDeploymentSource $SourceRepoPath $ExpectedRevision $OperationalRevision
$repo = $source.Repository
$OperationalRevision = $source.OperationalRevision
if ($OfflineMigration -and $ConfigureAdmin) { throw "offline migration cannot combine credential rotation with host recovery" }
$script:migrationSupervisorStarted = $false
$script:migrationSupervisorTerminal = $true
$script:migrationRecoveryVerified = $true
$script:migrationState = ""
$script:migrationUnit = ""
$remote = "${User}@${Ip}"
$resolvedKeyPath = (Resolve-Path -LiteralPath $KeyPath).Path
$resolvedKnownHostsPath = (Resolve-Path -LiteralPath $KnownHostsPath).Path
$sshExecutable = (Get-Command ssh -ErrorAction Stop).Source
$scpExecutable = (Get-Command scp -ErrorAction Stop).Source
$sshArgs = @("-i", $resolvedKeyPath, "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=$resolvedKnownHostsPath", "-o", "ConnectTimeout=20")
. (Join-Path $PSScriptRoot "admin-credential.ps1")

if ($ExpectedRevision -ne "" -and $ExpectedRevision -notmatch '^[0-9a-f]{40}$') {
    throw "-ExpectedRevision must be a lowercase immutable commit SHA"
}
if ($ExpectedPreviousRevision -ne "" -and $ExpectedPreviousRevision -notmatch '^[0-9a-f]{40}$') {
    throw "-ExpectedPreviousRevision must be a lowercase immutable commit SHA"
}

if ($RotateAdmin -and -not $ConfigureAdmin) {
    throw "-RotateAdmin requires -ConfigureAdmin"
}
if ($Domain.Length -gt 253 -or $Domain -notmatch '^(?=.{1,253}$)(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$') {
    throw "-Domain must be an ASCII DNS hostname"
}
# This production deploy also has a fixed www canonical redirect and an
# authenticated admin-origin check. Refuse a different host instead of ever
# sending the private credential to, or claiming readiness for, another
# origin. A generalized multi-domain deploy needs those contracts changed as
# one unit.
if (-not [string]::Equals($Domain, "codesamplex.dev", [StringComparison]::OrdinalIgnoreCase)) {
    throw "this production deploy supports only codesamplex.dev"
}
$Domain = $Domain.ToLowerInvariant()
if ($User -notmatch '^[a-z_][a-z0-9_-]{0,31}$') {
    throw "-User must be a simple Linux account name"
}

# ssh and scp write ordinary progress and host-key notices to stderr, which
# PowerShell 5.1 turns into terminating errors under ErrorActionPreference
# Stop. Exit codes are the real signal here.
function Invoke-Remote([string]$Script) {
    return Invoke-RemoteScript $Script
}
function Invoke-RemoteScript([string]$Script, [int]$TimeoutSeconds = 300) {
    if ([string]::IsNullOrWhiteSpace($Script)) { throw "refusing an empty remote script" }
    if ($resolvedKeyPath.Contains('"') -or $resolvedKnownHostsPath.Contains('"')) { throw "SSH configuration path contains an unsupported quote" }
    if ($remote -notmatch '^[A-Za-z0-9._-]+@[A-Za-z0-9.:-]+$') { throw "unsafe SSH destination" }

    # Windows OpenSSH does not preserve nested shell quotes when an entire
    # multiline program is passed as one argv element. Send programs on stdin
    # so regex pipes, quotes and redirects arrive byte-for-byte. Secrets use
    # Invoke-RemoteInput instead.
    #
    # The remote does NOT run `sh -s`, for two measured reasons.
    #
    # A program read from stdin shares that stdin with everything it runs. Most
    # of these programs call `docker compose exec -T db psql`, and on a PIPE
    # docker drains whatever the shell has not buffered yet — so the shell then
    # meets EOF partway through its own source and dies with
    # `Syntax error: Unterminated quoted string`. It is a race, not a constant:
    # the 2,501-byte pre-deploy invariant program failed on one run of a pair
    # and succeeded on the next, and the same bytes redirected from a FILE
    # never failed at all, because the file was already whole. Writing the
    # program to a temp file first and running it with stdin closed removes the
    # shared channel: four consecutive runs clean, where `sh -s` had been
    # flaky. That is also why the release pipeline never saw this — nothing in
    # it drives deploy.ps1 through a pipe on a Windows host.
    #
    # The disposable first line is the guard Invoke-RemoteInput already
    # carries. On .NET Framework — Windows PowerShell 5.1 — reading
    # Process.StandardInput builds a StreamWriter over Console.InputEncoding
    # with AutoFlush on, and that writes the encoding's preamble into the pipe
    # before any byte of ours. Here Console.InputEncoding is UTF-8 with a
    # three-byte preamble, so 2,501 bytes arrived as 2,504 and the remote sh
    # answered `sh: 1: ï»¿set: not found` — with `set -eu` never
    # applied. `printf '#'` makes that first line a comment whatever leads it,
    # so the real program starts at line 2 and its `set -eu` runs.
    #
    # Remote error line numbers are therefore one higher than the here-string's
    # own.
    $commandSeconds = Get-DeployBudgetSeconds $TimeoutSeconds
    $commandWatch = [Diagnostics.Stopwatch]::StartNew()
    $remoteCompleted = $false
    # The cancellation marker is checked UNDER the remote command lock. Late
    # SSH arrivals cannot mutate production after rollback has fenced this run.
    $guard = if ($script:deployRecoveryMode) { "" } else { 'test ! -e "$HOME/.csx-deploy-aborted-' + $deployLockOwner + '" || exit 75' }
    $scriptBytes = (New-Object Text.UTF8Encoding($false)).GetBytes("CSX-SCRIPT-V1`nset -e`n$guard`n" + $Script)
    $psi = New-Object Diagnostics.ProcessStartInfo
    $psi.FileName = $sshExecutable
    # Fail-closed, because the first version was not.
    #
    # `f=$(mktemp); { printf '#'; cat; } >$f; sh $f </dev/null` continues past
    # both failures: with mktemp broken, $f is empty, the redirection fails,
    # and `sh` with no argument reads an already-drained stdin and exits 0.
    # Demonstrated against the production host with TMPDIR pointed at a
    # missing directory — mktemp and the redirection both printed errors and
    # the whole thing still returned 0. A deploy that staged nothing, promoted
    # nothing and took no lock would have been recorded as a successful
    # rollout.
    #
    # Each step now stops the run and says which one failed:
    #   91  mktemp produced no file
    #   92  the program could not be written to it
    #   93  what landed is not the program we sent (the marker is missing)
    #   94  the temp path holds a character that would need quoting
    # and anything else is the remote program's own exit code, which `set -e`
    # carries out and the EXIT trap cleans up behind without replacing.
    #
    # 94 rather than quoting: the runner crosses Windows argv inside double
    # quotes, so a nested `"$f"` would end the argument. Refusing a path that
    # would need quoting is checkable here; mktemp does not produce one.
    # No double quote may appear in this string. It is embedded in
    # $psi.Arguments inside a "..." argv element, so a double quote here ends
    # the remote command early — the first fail-closed runner used
    # `trap "rm -f $f"` and `printf "#"` and the host answered
    # `bash: -c: line 2: syntax error: unexpected end of file`. The deploy
    # stopped without touching production, which is the behaviour this runner
    # exists for, but it stopped on its own quoting rather than on a real
    # failure. TestTheRemoteRunnerSurvivesTheArgvWrapper pins it.
    $remoteRunner = 'set -e; f=$(mktemp) || exit 91; case $f in *[!A-Za-z0-9./_-]*) exit 94;; esac; trap ''rm -f $f'' EXIT; { printf ''#''; cat; } > $f || exit 92; head -n 1 $f | grep -q CSX-SCRIPT-V1 || exit 93; timeout --signal=TERM --kill-after=5 __CSX_COMMAND_SECONDS__ flock -w 5 $HOME/.csx-deploy-command.lock sh $f < /dev/null'
    $remoteRunner = $remoteRunner.Replace('__CSX_COMMAND_SECONDS__', [string][Math]::Max(1, $commandSeconds - 5))
    $psi.Arguments = '-i "' + $resolvedKeyPath + '" -o StrictHostKeyChecking=yes -o UserKnownHostsFile="' + $resolvedKnownHostsPath + '" -o ConnectTimeout=20 ' + $remote + ' "' + $remoteRunner + '"'
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $psi
    try {
        if (-not $process.Start()) { throw "could not start SSH script transport" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        try {
            $write = $process.StandardInput.BaseStream.WriteAsync($scriptBytes, 0, $scriptBytes.Length)
            if (-not $write.Wait([Math]::Min(5000, $commandSeconds * 1000))) { throw "SSH script input timed out" }
        } finally {
            $process.StandardInput.Close()
            [Array]::Clear($scriptBytes, 0, $scriptBytes.Length)
        }
        Wait-DeployProcess $process ([int][Math]::Max(1, $commandSeconds - [Math]::Ceiling($commandWatch.Elapsed.TotalSeconds)))
        if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdoutTask, $stderrTask), 1000)) { throw "SSH script output timed out" }
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        $remoteCompleted = $true
        if ($process.ExitCode -in @(124, 137, 255)) { $script:remoteOutcomeUnknown = $true }
        if ($process.ExitCode -ne 0) {
            $detail = (($stdout, $stderr) -join "`n").Trim()
            if ($detail.Length -gt 4096) { $detail = $detail.Substring($detail.Length - 4096) }
            throw "remote script failed ($($process.ExitCode))`n$detail"
        }
        if (-not [string]::IsNullOrWhiteSpace($stdout)) {
            return @($stdout.TrimEnd() -split "`r?`n")
        }
    } finally {
        if (-not $remoteCompleted) { $script:remoteOutcomeUnknown = $true }
        try { if (-not $process.HasExited) { $process.Kill($true); [void]$process.WaitForExit(5000) } } catch { }
        Write-Host "phase=$($script:deployPhaseName) command=ssh elapsed_seconds=$([Math]::Round($commandWatch.Elapsed.TotalSeconds, 3)) ceiling_seconds=$commandSeconds"
        if ($null -ne $scriptBytes) { [Array]::Clear($scriptBytes, 0, $scriptBytes.Length) }
        $stdout = $null
        $stderr = $null
        $process.Dispose()
    }
}
function Invoke-RemoteInput([string]$Script, [string]$StdinText) {
    if ($StdinText -notmatch '^[0-9a-f]{64}$') {
        throw "refusing malformed remote verifier input"
    }
    if ($resolvedKeyPath.Contains('"') -or $resolvedKnownHostsPath.Contains('"')) { throw "SSH configuration path contains an unsupported quote" }
    if ($remote -notmatch '^[A-Za-z0-9._-]+@[A-Za-z0-9.:-]+$') { throw "unsafe SSH destination" }

    # Windows PowerShell 5 may prepend a UTF-8 BOM while newer hosts may not.
    # The fixed remote command prefixes `#` to a disposable first line, making
    # that line a comment in either case. The verifier remains on stdin only;
    # ProcessStartInfo.Arguments is static.
    $commandSeconds = Get-DeployBudgetSeconds 30
    $commandWatch = [Diagnostics.Stopwatch]::StartNew()
    $remoteCompleted = $false
    $payload = "CSX-STDIN-V1`nhash='$StdinText'`n$Script`n"
    $payloadBytes = (New-Object Text.UTF8Encoding($false)).GetBytes($payload)
    $psi = New-Object Diagnostics.ProcessStartInfo
    $psi.FileName = $sshExecutable
    $psi.Arguments = '-i "' + $resolvedKeyPath + '" -o StrictHostKeyChecking=yes -o UserKnownHostsFile="' + $resolvedKnownHostsPath + '" -o ConnectTimeout=20 ' + $remote + ' "{ printf ''#''; cat; } | timeout --signal=TERM --kill-after=2 20 sh"'
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $psi
    try {
        if (-not $process.Start()) { throw "could not start SSH verifier transport" }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        try {
            $write = $process.StandardInput.BaseStream.WriteAsync($payloadBytes, 0, $payloadBytes.Length)
            if (-not $write.Wait(5000)) { throw "SSH verifier input timed out" }
        } finally {
            $process.StandardInput.Close()
            [Array]::Clear($payloadBytes, 0, $payloadBytes.Length)
            $payload = $null
        }
        Wait-DeployProcess $process ([int][Math]::Max(1, $commandSeconds - [Math]::Ceiling($commandWatch.Elapsed.TotalSeconds)))
        if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdoutTask, $stderrTask), 1000)) { throw "SSH verifier output timed out" }
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        $remoteCompleted = $true
        if ($process.ExitCode -in @(124, 137, 255)) { $script:remoteOutcomeUnknown = $true }
        if ($process.ExitCode -ne 0) {
            # The remote program never echoes the verifier. Keep failures
            # generic anyway so a future edit cannot turn logs into a leak.
            throw "remote verifier installation failed ($($process.ExitCode))"
        }
        if (-not [string]::IsNullOrWhiteSpace($stdout)) { return $stdout.TrimEnd() }
    } finally {
        if (-not $remoteCompleted) { $script:remoteOutcomeUnknown = $true }
        try { if (-not $process.HasExited) { $process.Kill($true); [void]$process.WaitForExit(5000) } } catch { }
        Write-Host "phase=$($script:deployPhaseName) command=ssh-verifier elapsed_seconds=$([Math]::Round($commandWatch.Elapsed.TotalSeconds, 3)) ceiling_seconds=$commandSeconds"
        if ($null -ne $payloadBytes) { [Array]::Clear($payloadBytes, 0, $payloadBytes.Length) }
        $payload = $null
        $stdout = $null
        $stderr = $null
        $process.Dispose()
    }
}
function Copy-Remote([string]$Local, [string]$RemotePath) {
    Invoke-DeployProcess $scpExecutable (@("-i", $resolvedKeyPath, "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=$resolvedKnownHostsPath", "-o", "ConnectTimeout=20", $Local, "${remote}:${RemotePath}")) 300 | Out-Null
}

# The lock covers local credential state, the fixed Docker tag/tar, every
. (Join-Path $PSScriptRoot "offline-migration.ps1")
$rollbackServerTemplate = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot "rollback-server.sh")
$rollbackCaddyTemplate = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot "rollback-caddy.sh")

# The lock covers local credential state, the fixed Docker tag/tar, every
# remote candidate/rollback filename, activation and smoke. Creating the
# parent is the only pre-lock remote mutation and is idempotent for a
# first-ever host.
$deployLockOwner = [guid]::NewGuid().ToString("N")
$script:deployRecoveryMode = $false
$script:retainDeployLock = $false
$script:deployGenerationFenced = $false
$script:remoteOutcomeUnknown = $false
Invoke-Remote "sudo install -d -o $User -g $User /opt/codesamplex" | Out-Null
$acquireDeployLock = @'
set -eu
lock=/opt/codesamplex/.deploy-lock
owner=__CSX_DEPLOY_OWNER__
umask 077
if ! mkdir "$lock" 2>/dev/null; then
  echo "another deploy owns /opt/codesamplex/.deploy-lock; confirm it is no longer running, inspect owner, then remove only owner and the empty directory manually" >&2
  exit 73
fi
printf '%s\n' "$owner" > "$lock/owner"
chmod 0600 "$lock/owner"
'@
$acquireDeployLock = $acquireDeployLock.Replace('__CSX_DEPLOY_OWNER__', $deployLockOwner)
Invoke-RemoteScript $acquireDeployLock | Out-Null
$deployLockHeld = $true
$deployScriptFailure = $null
$imageTar = $null
$remoteImageTar = $null
$localImageTag = $null
$localImageCleanupNeeded = $false
$tagTmp = $null
$envTmp = $null
try {
$revision = ((Invoke-DeployProcess git @("-C", $repo, "rev-parse", "HEAD") 10) -join "").Trim()
if ($revision -notmatch '^[0-9a-f]{40}$') { throw "could not determine the server revision" }
if ($ExpectedRevision -ne "" -and $revision -ne $ExpectedRevision) {
    throw "checked-out revision does not match -ExpectedRevision"
}
# The human-readable half of the build identity. `git describe` names the
# release line this commit sits on and how far past it; the footer renders it
# beside the short revision, and /version serves both. Derived here rather
# than inside the image because the build context deliberately excludes .git.
$buildVersion = ((Invoke-DeployProcess git @("-C", $repo, "describe", "--tags", "--always") 10) -join "").Trim()
if ($buildVersion -eq "") { throw "could not determine the server build version" }
$builtAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")

$expectedMigration = (Get-ChildItem (Join-Path $repo "internal/serverstore/migrations") -Filter "*.sql" -File | Sort-Object Name | Select-Object -Last 1).Name
if ($expectedMigration -notmatch '^[0-9]{4}_[a-z0-9_]+\.sql$') { throw "could not determine the expected migration version" }
$expectedMigrationCount = (Get-ChildItem (Join-Path $repo "internal/serverstore/migrations") -Filter "*.sql" -File).Count

$productionStateBefore = Invoke-RemoteScript @'
set -eu
cd /opt/codesamplex/deploy
if docker container inspect codesamplex-server-1 >/dev/null 2>&1; then
  revision=$(docker inspect codesamplex-server-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^CSX_VERSION=//p' | head -n 1)
  image=$(docker inspect codesamplex-server-1 --format '{{.Image}}')
  printf '%s|%s\n' "$revision" "$image"
else
  printf 'none|none\n'
fi
'@ | Select-Object -First 1
$productionStateParts = (([string]$productionStateBefore).Trim() -split '\|')
if ($productionStateParts.Count -ne 2 -or
    (($productionStateParts[0] -ne "none" -or $productionStateParts[1] -ne "none") -and
     ($productionStateParts[0] -notmatch '^[0-9a-f]{40}$' -or $productionStateParts[1] -notmatch '^sha256:[0-9a-f]{64}$'))) {
    throw "could not identify the current production revision and rollback image"
}
if ($ExpectedPreviousRevision -ne "" -and $productionStateParts[0] -ne $ExpectedPreviousRevision) {
    throw "production revision does not match -ExpectedPreviousRevision"
}
if ($DeploymentEvidence.ContainsKey('previousImageDigest') -and $productionStateParts[1] -ne $DeploymentEvidence.previousImageDigest) {
    throw "production image drifted between identity collection and lock acquisition"
}
Write-Output "previous production SHA: $($productionStateParts[0])"
Write-Output "previous production image: $($productionStateParts[1])"

# Corpus-wide invariants and legacy-log validation run in post-deploy observation.

$adminTokenHash = ""
$adminCredentialPending = $false
$adminCredentialPaths = $null
if ($ConfigureAdmin) {
    Write-Output "== configuring private /admin credential =="
    $adminCredentialPaths = Get-CSXAdminCredentialPaths

    if ($RotateAdmin) {
        if (Test-Path -LiteralPath $adminCredentialPaths.Pending -PathType Leaf) {
            throw "a pending admin credential exists from an incomplete deploy; rerun with -ConfigureAdmin before rotating again"
        }
        $adminSecret = New-CSXAdminCredential
        Save-CSXAdminCredential $adminSecret $adminCredentialPaths.Pending
        $adminCredentialPending = $true
        Write-Output "generated a new 256-bit password in the current user's DPAPI store"
    } elseif (Test-Path -LiteralPath $adminCredentialPaths.Pending -PathType Leaf) {
        $adminSecret = Read-CSXAdminCredential $adminCredentialPaths.Pending
        $adminCredentialPending = $true
        Write-Output "resuming the pending DPAPI-protected admin credential"
    } elseif (Test-Path -LiteralPath $adminCredentialPaths.Active -PathType Leaf) {
        $adminSecret = Read-CSXAdminCredential $adminCredentialPaths.Active
        Write-Output "reusing the current DPAPI-protected admin credential"
    } else {
        $adminSecret = New-CSXAdminCredential
        Save-CSXAdminCredential $adminSecret $adminCredentialPaths.Pending
        $adminCredentialPending = $true
        Write-Output "generated a 256-bit password in the current user's DPAPI store"
    }

    try {
        $adminTokenHash = Get-CSXSecureStringSHA256 $adminSecret
    } finally {
        $adminSecret.Dispose()
    }
}

# Snapshot the exact post-staging local relationship. A failed deployment is
# intentionally resumable: an existing active credential stays active and a
# newly generated/resumed pending credential stays pending. The final DPAPI
# promotion is the only local mutation after this point.
$adminCredentialState = $null
if ($ConfigureAdmin) {
    $activeExisted = Test-Path -LiteralPath $adminCredentialPaths.Active -PathType Leaf
    $pendingExisted = Test-Path -LiteralPath $adminCredentialPaths.Pending -PathType Leaf
    $activeBackup = $adminCredentialPaths.Active + ".deploy-state." + $deployLockOwner
    $pendingBackup = $adminCredentialPaths.Pending + ".deploy-state." + $deployLockOwner
    if ($activeExisted) { [IO.File]::Copy($adminCredentialPaths.Active, $activeBackup, $false) }
    if ($pendingExisted) { [IO.File]::Copy($adminCredentialPaths.Pending, $pendingBackup, $false) }
    $adminCredentialState = [pscustomobject]@{
        ActiveExisted  = $activeExisted
        PendingExisted = $pendingExisted
        ActiveBackup   = $activeBackup
        PendingBackup  = $pendingBackup
        ActiveHash     = if ($activeExisted) { (Get-FileHash -LiteralPath $activeBackup -Algorithm SHA256).Hash } else { "" }
        PendingHash    = if ($pendingExisted) { (Get-FileHash -LiteralPath $pendingBackup -Algorithm SHA256).Hash } else { "" }
    }
}

function Restore-CSXAdminCredentialRelationship($Paths, $State) {
    if ($null -eq $State) { return }
    foreach ($item in @(
        [pscustomobject]@{ Path = $Paths.Active; Backup = $State.ActiveBackup; Existed = $State.ActiveExisted },
        [pscustomobject]@{ Path = $Paths.Pending; Backup = $State.PendingBackup; Existed = $State.PendingExisted }
    )) {
        if (-not $item.Existed) {
            [IO.File]::Delete($item.Path)
            continue
        }
        $restore = $item.Path + ".restore." + [Guid]::NewGuid().ToString("N")
        $discard = $item.Path + ".discard." + [Guid]::NewGuid().ToString("N")
        try {
            [IO.File]::Copy($item.Backup, $restore, $false)
            if ([IO.File]::Exists($item.Path)) {
                [IO.File]::Replace($restore, $item.Path, $discard)
            } else {
                [IO.File]::Move($restore, $item.Path)
            }
        } finally {
            # State replacement has no fallible cleanup after it: leftover
            # files are still DPAPI ciphertext and are retried by this finally.
            try { [IO.File]::Delete($restore) } catch { }
            try { [IO.File]::Delete($discard) } catch { }
        }
    }
    $activeNow = Test-Path -LiteralPath $Paths.Active -PathType Leaf
    $pendingNow = Test-Path -LiteralPath $Paths.Pending -PathType Leaf
    if ($activeNow -ne $State.ActiveExisted -or $pendingNow -ne $State.PendingExisted) {
        throw "local admin credential active/pending relationship was not restored"
    }
    if ($activeNow -and (Get-FileHash -LiteralPath $Paths.Active -Algorithm SHA256).Hash -ne $State.ActiveHash) {
        throw "local active admin credential was not restored exactly"
    }
    if ($pendingNow -and (Get-FileHash -LiteralPath $Paths.Pending -Algorithm SHA256).Hash -ne $State.PendingHash) {
        throw "local pending admin credential was not restored exactly"
    }
}

$requiredReleaseAssets = @(
    "csx-darwin-amd64",
    "csx-darwin-arm64",
    "csx-linux-amd64",
    "csx-linux-arm64",
    "csx-server-linux-amd64",
    "csx-windows-amd64.exe",
    "csx-windows-arm64.exe",
    "csx-launcher-windows-amd64.exe",
    "csx-launcher-windows-arm64.exe",
    "SHA256SUMS.txt",
	"csx-update-stable.json",
    "csx-bootstrap-stable.json",
    "codesamplex-mcp.mcpb",
    "codesamplex-mcp.mcpb.sha256"
)

# Assert-ReleaseDirectory used to verify a local copy of the release here.
# It was removed with the local download: the check that decides anything runs
# on the host, against the bytes that will actually be served -- `sha256sum -c
# SHA256SUMS.txt`, the per-asset `test -f`, and the exact file count. Keeping a
# second copy of it on a machine the artifacts no longer touch would be a
# verification of nothing.

$localImageTag = "codesamplex/csx-server:deploy-$deployLockOwner"
$imageTar = Join-Path ([IO.Path]::GetTempPath()) "csx-server-image-$deployLockOwner.tar"
if (-not $SkipImage) {
	$localImageCleanupNeeded = $true
    Write-Output "== building linux/amd64 server image =="
    $dockerfile = Join-Path (Join-Path $repo "deploy") "Dockerfile.server"
    Invoke-DeployProcess docker @("build", "--platform", "linux/amd64",
        "--build-arg", "CSX_VERSION=$revision", "--build-arg", "CSX_BUILD_VERSION=$buildVersion",
        "--build-arg", "CSX_BUILT_AT=$builtAt", "--build-arg", "CSX_ENV=production",
        "-f", $dockerfile, "-t", $localImageTag, $repo) 150 | ForEach-Object { Write-Output $_ }
    Invoke-DeployProcess docker @("save", $localImageTag, "-o", $imageTar) 60 | Out-Null
}

Set-DeployPhase staging 240
Write-Output "== shipping bundle to $Ip =="
Invoke-Remote "mkdir -p /opt/codesamplex/deploy/caddy /opt/codesamplex/dist /opt/codesamplex/schemas/v1 /opt/codesamplex/backups && sudo chown ${User}:${User} /opt/codesamplex/backups && (sudo chown ${User}:${User} /opt/codesamplex/deploy/backup.sh /opt/codesamplex/deploy/restore-check.sh 2>/dev/null || true)" | Out-Null
# Snapshot the exact live server/config/image state before this deploy changes
# the compose file, image tag, container, or mode-0600 environment. Every
# dimension has one present/absent marker so first-deploy rollback is as
# deterministic as an ordinary rolling rollback.
$snapshotServerConfig = @'
set -eu
umask 077
cd /opt/codesamplex/deploy
rm -f dist.rollback-promoted
# Old proxy snapshots must never be mistaken for this invocation's snapshot
# when its later promotion command cannot be delivered.
rm -f caddy/Caddyfile.rollback-predeploy caddy/Caddyfile.rollback-absent \
  caddy/container.rollback-present caddy/container.rollback-absent \
  caddy/container.rollback-running caddy/container.rollback-stopped caddy/container.rollback-image-id
rm -f docker-compose.yml.rollback-predeploy docker-compose.yml.rollback-absent .env.rollback-predeploy .env.rollback-absent \
  server-container.rollback-present server-container.rollback-absent server-container.rollback-running server-container.rollback-stopped \
  server-image.rollback-id server-latest.rollback-id server-latest.rollback-absent
if [ -f docker-compose.yml ]; then
  cp -p docker-compose.yml docker-compose.yml.rollback-predeploy
else
  : > docker-compose.yml.rollback-absent
fi
if [ -f .env ]; then
  cp -p .env .env.rollback-predeploy
  chmod 0600 .env.rollback-predeploy
else
  : > .env.rollback-absent
fi
docker image rm codesamplex/csx-server:rollback-predeploy codesamplex/csx-server:rollback-latest-predeploy >/dev/null 2>&1 || true
if docker container inspect codesamplex-server-1 >/dev/null 2>&1; then
  test -f docker-compose.yml.rollback-predeploy
  old=$(docker inspect codesamplex-server-1 --format '{{.Image}}')
  printf '%s\n' "$old" | grep -Eq '^sha256:[0-9a-f]{64}$'
  docker image inspect "$old" >/dev/null
  docker tag "$old" codesamplex/csx-server:rollback-predeploy
  printf '%s\n' "$old" > server-image.rollback-id
  : > server-container.rollback-present
  if [ "$(docker inspect codesamplex-server-1 --format '{{.State.Running}}')" = true ]; then
    : > server-container.rollback-running
  else
    : > server-container.rollback-stopped
  fi
else
  : > server-container.rollback-absent
fi
if docker image inspect codesamplex/csx-server:latest >/dev/null 2>&1; then
  latest=$(docker image inspect codesamplex/csx-server:latest --format '{{.Id}}')
  printf '%s\n' "$latest" | grep -Eq '^sha256:[0-9a-f]{64}$'
  docker tag "$latest" codesamplex/csx-server:rollback-latest-predeploy
  printf '%s\n' "$latest" > server-latest.rollback-id
else
  : > server-latest.rollback-absent
fi
'@
Invoke-RemoteScript $snapshotServerConfig | Out-Null
$serverActivationStarted = $false
$caddyPromoted = $false
$distPromoted = $false
try {
Copy-Remote (Join-Path $repo "deploy\docker-compose.yml") "/opt/codesamplex/deploy/docker-compose.yml.candidate"
Copy-Remote (Join-Path $repo "deploy\caddy\Caddyfile") "/opt/codesamplex/deploy/caddy/Caddyfile.candidate"
# Syntax prevents activation failure; dynamic privacy probes run after commit.
Invoke-Remote "docker run --rm -v /opt/codesamplex/deploy/caddy/Caddyfile.candidate:/etc/caddy/Caddyfile:ro caddy:2.11.4-alpine caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile" | Out-Null
Copy-Remote (Join-Path $repo "deploy\backup.sh") "/opt/codesamplex/deploy/backup.sh"
Copy-Remote (Join-Path $repo "deploy\restore-check.sh") "/opt/codesamplex/deploy/restore-check.sh"
Invoke-Remote "chmod 755 /opt/codesamplex/deploy/backup.sh /opt/codesamplex/deploy/restore-check.sh" | Out-Null
Copy-Remote (Join-Path $repo "schemas\v1\adapters.json") "/opt/codesamplex/schemas/v1/adapters.json"

# The download endpoint is fed from the exact release of this revision, never from
# whatever happens to be sitting in dist/.
#
# It used to ship the local folder, and the local folder was last built by
# hand. The result: every deploy re-shipped v0.1.0 to codesamplex.dev/dl
# while GitHub's latest was v0.1.2, so everyone who followed the README got
# a binary two releases old — including the one that inferred consent from
# EOF, which meant `curl ... | sh` enrolled people in evidence sharing
# without anyone answering the question. Two sources of truth, and the
# hand-fed one was the one users actually got.
$releaseTags = @(Invoke-DeployProcess git @("-C", $repo, "tag", "--points-at", $revision, "--list", "v*") 10)
$releaseTags = @($releaseTags | Where-Object { $_ -cmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' })
if ($releaseTags.Count -ne 1) { throw "deployment revision must have exactly one canonical release tag" }
$tag = [string]$releaseTags[0]
$publishedTag = (Invoke-DeployProcess gh @('release', 'view', $tag, '--repo', 'r2cuerdame/CodeSampleX', '--json', 'tagName,isDraft', '--jq', 'select(.isDraft == false) | .tagName') 30)
if ($publishedTag -cne $tag) { throw "deployment release is not published" }
# A directory bind mount pins the old installer's release across host rename.
# The replacement server opens the promoted directory as a new generation.
$mountedReleaseBefore = (Invoke-RemoteScript @'
set -eu
test ! -L /opt/codesamplex/dist
if [ "$(docker inspect codesamplex-server-1 --format '{{.State.Running}}' 2>/dev/null || true)" = true ]; then
  docker exec codesamplex-server-1 cat /data/dist/.release-tag
fi
'@ | Select-Object -First 1)
if ($mountedReleaseBefore -and $mountedReleaseBefore -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') { throw "running server has an invalid mounted release generation" }
$remoteTagLine = Invoke-Remote "cat /opt/codesamplex/dist/.release-tag 2>/dev/null || true" | Select-Object -First 1
$remoteTag = if ($null -eq $remoteTagLine) { "" } else { ([string]$remoteTagLine).Trim() }
$remoteFiles = ($requiredReleaseAssets | ForEach-Object { "test -f /opt/codesamplex/dist/$_" }) -join " && "
$remoteValidation = ('set -eu; {0}; cd /opt/codesamplex/dist; sha256sum -c SHA256SUMS.txt >/dev/null; sha256sum -c codesamplex-mcp.mcpb.sha256 >/dev/null; chmod +x csx-linux-amd64; ./csx-linux-amd64 update verify-release . {2} >/dev/null; test "$(find . -maxdepth 1 -type f ! -name .release-tag | wc -l)" -eq {1}' -f $remoteFiles, $requiredReleaseAssets.Count, $tag)
$releaseReady = $false
if ($remoteTag -eq $tag) {
    try {
        Invoke-Remote $remoteValidation | Out-Null
        $releaseReady = $true
    } catch {
        Write-Output "== remote $tag asset validation failed; refreshing the complete set =="
    }
}
if ($releaseReady) {
    # `gh release download` increments GitHub's public download counter. A
    # code-only server deploy used to download every platform binary again,
    # making that counter mostly measure our own deploys instead of people.
    Write-Output "== release artifacts already served from $tag; skipping download =="
} else {
    # The host fetches its own artifacts. This workstation is not in the path.
    #
    # It used to download the complete asset set here, verify it, and copy it
    # up. Measured 2026-09-01: Windows Defender quarantined
    # dist/csx-windows-amd64.exe out of that staging directory mid-verification
    # -- Get-FileHash returned null on a file Test-Path had just confirmed --
    # and the deploy died. The same ThreatID (2147731250) had taken the
    # installed payload six minutes earlier, and this machine holds 57 of them.
    #
    # A Windows workstation has no business being the courier for Linux and
    # macOS binaries, and for the Windows ones it is the worst possible
    # courier: it is the only machine on the path that inspects and deletes
    # them. Nothing is weakened by moving the fetch, because the integrity
    # check that decides anything was already remote -- `sha256sum -c` against
    # the release's own SHA256SUMS.txt, run below on the bytes that will
    # actually be served. The local pass was a duplicate of it.
    #
    # No Defender setting is touched, no exclusion added, and no quarantined
    # file is restored. The bytes simply never land here.
    Write-Output "== fetching release artifacts for $tag on the host =="
    $stage = "/opt/codesamplex/dist.stage"
    if ($tag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$') { throw "refusing an unexpected release tag: $tag" }
    $assetList = ($requiredReleaseAssets -join " ")
    $fetch = @"
set -eu
rm -rf $stage
mkdir -p $stage
cd $stage
for a in $assetList; do
  curl -fsSL --connect-timeout 5 --max-time 30 --retry 3 --retry-delay 2 --retry-max-time 60 -o "`$a"     "https://github.com/r2cuerdame/CodeSampleX/releases/download/$tag/`$a"
done
"@
    Invoke-RemoteScript $fetch | Out-Null
    $tagTmp = Join-Path ([IO.Path]::GetTempPath()) "csx-release-tag-$deployLockOwner.txt"
    Set-Content -Path $tagTmp -Value $tag -Encoding ascii -NoNewline
    Copy-Remote $tagTmp "$stage/.release-tag"
    $stageFiles = ($requiredReleaseAssets | ForEach-Object { "test -f $stage/$_" }) -join " && "
    $stageValidation = ('set -eu; {0}; cd /opt/codesamplex/dist.stage; sha256sum -c SHA256SUMS.txt >/dev/null; sha256sum -c codesamplex-mcp.mcpb.sha256 >/dev/null; chmod +x csx-linux-amd64; ./csx-linux-amd64 update verify-release . {2} >/dev/null; test "$(find . -maxdepth 1 -type f ! -name .release-tag | wc -l)" -eq {1}' -f $stageFiles, $requiredReleaseAssets.Count, $tag)
    Invoke-Remote $stageValidation | Out-Null
    $distPromoted = $true
    Invoke-Remote "set -eu; rm -f /opt/codesamplex/deploy/dist.rollback-promoted; rm -rf /opt/codesamplex/dist.previous; touch /opt/codesamplex/deploy/dist.rollback-promoted; mv /opt/codesamplex/dist /opt/codesamplex/dist.previous; if mv /opt/codesamplex/dist.stage /opt/codesamplex/dist; then :; else mv /opt/codesamplex/dist.previous /opt/codesamplex/dist; exit 1; fi" | Out-Null
    $distPromoted = $true
    if ($mountedReleaseBefore) {
        $stillMounted = (Invoke-Remote "docker exec codesamplex-server-1 cat /data/dist/.release-tag" | Select-Object -First 1)
        if ($stillMounted -cne $mountedReleaseBefore) { throw "running installer and release assets changed generations before server activation" }
    }
    Write-Output "host fetched and verified $($requiredReleaseAssets.Count) artifacts from $tag; promoted atomically"
}

# .env holds the generated DB password: write once, never overwrite.
$pw = -join ((48..57) + (97..122) | Get-Random -Count 24 | ForEach-Object { [char]$_ })
# Both names must resolve to this host: Caddy asks a CA for a certificate
# per name and an unresolvable one fails its challenge forever.
$envText = @"
CADDY_SITE=$Domain, www.$Domain
CSX_PUBLIC_URL=https://$Domain
CSX_DIST_HOST_DIR=/opt/codesamplex/dist
POSTGRES_PASSWORD=$pw
"@
$envTmp = Join-Path ([IO.Path]::GetTempPath()) "csx-$deployLockOwner.env"
$normalizedEnvText = ($envText -replace "`r`n", "`n").TrimEnd("`r", "`n") + "`n"
[IO.File]::WriteAllText($envTmp, $normalizedEnvText, [Text.Encoding]::ASCII)
Copy-Remote $envTmp "/opt/codesamplex/deploy/.env.new"
Invoke-Remote "cd /opt/codesamplex/deploy && chmod 600 .env.new && if [ -f .env ]; then rm -f .env.new; chmod 600 .env; echo 'kept existing .env'; else mv .env.new .env; echo 'wrote new .env'; fi" | Out-Null

# Generate the activity HMAC key on the host and keep it stable across rolling
# deploys. It travels neither in argv nor output and never touches local disk.
$ensureActivityKey = @'
set -eu
umask 077
cd /opt/codesamplex/deploy
[ -f .env ] || exit 65
chmod 0600 .env
if grep -Eq '^CSX_ACTIVITY_HASH_KEY=[0-9a-f]{64}$' .env; then exit 0; fi
if grep -q '^CSX_ACTIVITY_HASH_KEY=' .env; then exit 64; fi
key=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
[ "${#key}" -eq 64 ] || exit 66
tmp=$(mktemp .env.activity.XXXXXX)
trap 'rm -f "$tmp"' EXIT HUP INT TERM
cat .env > "$tmp"
# A legacy or freshly copied env may lack its final newline. Repair the
# boundary before appending so the key can never become part of the database
# password (and therefore the DSN) on first deploy.
if [ -s "$tmp" ] && [ -n "$(tail -c 1 "$tmp")" ]; then printf '\n' >> "$tmp"; fi
printf '%s\n' "CSX_ACTIVITY_HASH_KEY=$key" >> "$tmp"
chmod 0600 "$tmp"
mv "$tmp" .env
unset key
trap - EXIT HUP INT TERM
'@
Invoke-RemoteScript $ensureActivityKey | Out-Null
Write-Output "activity hash key: present in remote mode-0600 environment"

if ($ConfigureAdmin) {
    # Only the one-way verifier crosses SSH, on stdin. The fixed remote script
    # validates it and atomically replaces just this setting in the existing
    # host-owned .env; neither plaintext nor verifier is printed.
    $installAdminHash = @'
set -eu
umask 077
printf '%s\n' "$hash" | grep -Eq '^[0-9a-f]{64}$' || exit 64
cd /opt/codesamplex/deploy
[ -f .env ] || exit 65
tmp=$(mktemp .env.admin.XXXXXX)
trap 'rm -f "$tmp"' EXIT HUP INT TERM
awk -F= '$1 != "CSX_ADMIN_TOKEN_SHA256"' .env > "$tmp"
printf '%s\n' "CSX_ADMIN_TOKEN_SHA256=$hash" >> "$tmp"
chmod 600 "$tmp"
mv "$tmp" .env
trap - EXIT HUP INT TERM
'@
    Invoke-RemoteInput $installAdminHash $adminTokenHash | Out-Null
    $adminTokenHash = $null
    Write-Output "admin credential hash installed"
}

if (-not $SkipImage) {
    Write-Output "== loading image on host (this takes a minute) =="
    $remoteImageTar = "/opt/codesamplex/csx-server-image-$deployLockOwner.tar"
    Copy-Remote $imageTar $remoteImageTar
    Invoke-Remote "set -eu; docker load -i $remoteImageTar >/dev/null; docker tag $localImageTag codesamplex/csx-server:latest; docker image rm $localImageTag >/dev/null; rm -f $remoteImageTar" | Out-Null
}

    Set-DeployPhase activation 30
    $promoteServerConfig = @'
set -eu
cd /opt/codesamplex/deploy
candidate=docker-compose.yml.candidate
test -f "$candidate"
chmod 0644 "$candidate"
mv -f "$candidate" docker-compose.yml
'@
    Invoke-RemoteScript $promoteServerConfig | Out-Null
    $serverActivationStarted = $true

    # Promote only after every unrelated shipping/build step has succeeded.
    # Keep one exact rollback copy until minimal host acceptance commits.
    $promoteCaddy = @'
set -eu
candidate=/opt/codesamplex/deploy/caddy/Caddyfile.candidate
live=/opt/codesamplex/deploy/caddy/Caddyfile
rollback=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-predeploy
absent=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-absent
container_present=/opt/codesamplex/deploy/caddy/container.rollback-present
container_absent=/opt/codesamplex/deploy/caddy/container.rollback-absent
container_running=/opt/codesamplex/deploy/caddy/container.rollback-running
container_stopped=/opt/codesamplex/deploy/caddy/container.rollback-stopped
image_id=/opt/codesamplex/deploy/caddy/container.rollback-image-id
promoted=0
cleanup() {
  : # Recovery owns snapshots even when the promotion acknowledgement is lost.
}
trap cleanup EXIT HUP INT TERM
test -f "$candidate"
rm -f "$rollback" "$absent" "$container_present" "$container_absent" "$container_running" "$container_stopped" "$image_id"
if [ -f "$live" ]; then
  cp -p "$live" "$rollback"
else
  : > "$absent"
fi
if docker container inspect codesamplex-caddy-1 >/dev/null 2>&1; then
  old=$(docker inspect codesamplex-caddy-1 --format '{{.Image}}')
  printf '%s\n' "$old" | grep -Eq '^sha256:[0-9a-f]{64}$'
  printf '%s\n' "$old" > "$image_id"
  : > "$container_present"
  if [ "$(docker inspect codesamplex-caddy-1 --format '{{.State.Running}}')" = true ]; then : > "$container_running"; else : > "$container_stopped"; fi
else
  : > "$container_absent"
fi
chmod 0644 "$candidate"
mv -f "$candidate" "$live"
promoted=1
'@
    $caddyPromoted = $true
    Invoke-RemoteScript $promoteCaddy | Out-Null

Write-Output "== starting stack =="
# A release refresh swaps the host dist directory atomically. An existing
# bind mount keeps the old directory inode even after the host path is
# replaced, so `compose up` without recreation can keep serving the previous
# release forever. Recreate the server explicitly on every deploy: image
# upgrades need the same guarantee, and its healthcheck bounds the restart.
if ($OfflineMigration) {
    $hostResult = Start-CSXOfflineMigration
    Set-CSXHostDeploymentEvidence $hostResult
    $liveIdentityParts = @($hostResult.targetSha, $hostResult.imageDigest, $hostResult.targetSha,
        $hostResult.migrationLedger.version, $hostResult.servedRevision, $hostResult.serverStartedAt)
} else {
    Set-DeployPhase activation-smoke 180
    Invoke-Remote "cd /opt/codesamplex/deploy && docker compose up -d --no-build --force-recreate server" | Out-Null
if (-not $OfflineMigration) {
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose up -d --no-build --remove-orphans" | Out-Null
# Caddy documents that file-output option changes require a server restart,
# not only a config reload. Recreate this single proxy after the healthy app
# is ready, then reload once more as an explicit live-config validation.
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose up -d --no-build --force-recreate caddy" | Out-Null
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile" | Out-Null
}
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose ps" | ForEach-Object { Write-Output $_ }
if (-not $SkipImage) {
    $imagePair = Invoke-Remote 'set -eu; expected=$(docker image inspect codesamplex/csx-server:latest --format ''{{.Id}}''); actual=$(docker inspect codesamplex-server-1 --format ''{{.Image}}''); echo $expected $actual' | Select-Object -First 1
    $ids = (([string]$imagePair).Trim() -split '\s+')
    if ($ids.Count -ne 2 -or $ids[0] -ne $ids[1]) { throw "server container is not running the image that was just loaded" }
    Write-Output "server image: $($ids[0])"
}

Write-Output "== smoke test =="
$healthCheck = @'
set -eu
cd /opt/codesamplex/deploy
# Leave transport/termination margin inside the 120s command budget so a
# genuinely unhealthy app exits 1 and enters exact rollback, not timeout-unknown.
deadline=$(($(date +%s) + 90))
while :; do
  if docker compose exec -T server wget -q -T 3 -t 1 -O- http://127.0.0.1:8080/healthz 2>/dev/null | grep -qx ok; then break; fi
  if [ "$(date +%s)" -ge "$deadline" ]; then echo 'healthz never returned ok' >&2; exit 1; fi
  sleep 2
done
'@
Invoke-RemoteScript $healthCheck 120 | Out-Null
Write-Output "healthz: ok"
$mountedReleaseAfter = (Invoke-Remote "docker exec codesamplex-server-1 cat /data/dist/.release-tag" | Select-Object -First 1)
if ($mountedReleaseAfter -cne $tag) { throw "activated server installer and release assets have different identities" }
Write-Output "installer generation: $tag (server and directory bind mount agree)"

# Extended privacy/routes/admin/activity probes are observation-only.
if ($ConfigureAdmin) {
    # Prove the local DPAPI credential and remote verifier are the same value.
    # The request is made with a header (never a URL credential), follows no
    # redirects, and exposes only its numeric status.
    $adminCredentialFile = if ($adminCredentialPending) { $adminCredentialPaths.Pending } else { $adminCredentialPaths.Active }
    $adminStatus = 0
    for ($i = 0; $i -lt 10 -and $adminStatus -ne 200; $i++) {
        if ((Get-DeployBudgetSeconds 20) -lt 17) { throw "activation budget cannot accommodate another admin credential probe" }
        $probeSecret = Read-CSXAdminCredential $adminCredentialFile
        try {
            try { $adminStatus = Invoke-CSXAdminAuthenticatedProbe $probeSecret }
            catch { $adminStatus = 0 }
        } finally {
            $probeSecret.Dispose()
        }
        if ($adminStatus -ne 200) { Start-Sleep -Seconds 2 }
    }
    if ($adminStatus -ne 200) { throw "admin authenticated smoke failed (HTTP $adminStatus)" }
    Write-Output "admin authenticated smoke: 200"
}

# The process being healthy is not proof that it serves the commit ProjectOps
# dispatched. Check the running container, immutable image label and migration
# ledger before the transaction is committed, so any mismatch enters the exact
# rollback path below rather than becoming a successful deployment record.
$liveIdentityScript = @'
set -eu
cd /opt/codesamplex/deploy
revision=$(docker inspect codesamplex-server-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^CSX_VERSION=//p' | head -n 1)
image=$(docker inspect codesamplex-server-1 --format '{{.Image}}')
label=$(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
migration=$(docker compose exec -T -e PGOPTIONS="-c statement_timeout=10000 -c lock_timeout=3000" db psql -U csx -d csx -Atqc "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
served=$(docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/version |
  sed -n 's/.*"revision":"\([0-9a-f]\{40\}\)".*/\1/p' | head -n 1)
started=$(docker inspect codesamplex-server-1 --format '{{.State.StartedAt}}')
printf '%s|%s|%s|%s|%s|%s\n' "$revision" "$image" "$label" "$migration" "$served" "$started"
'@
$liveIdentity = (Invoke-RemoteScript $liveIdentityScript | Select-Object -First 1).Trim()
$liveIdentityParts = $liveIdentity -split '\|'
# The container environment and the image label say what was configured
# and what was built. Only /version says what the process now answering
# requests was built from, which is what this deploy is claiming.
if ($liveIdentityParts.Count -ne 6 -or $liveIdentityParts[0] -ne $revision -or $liveIdentityParts[2] -ne $revision -or $liveIdentityParts[4] -ne $revision) {
    throw "served SHA does not match the immutable deployment revision"
}
if ($liveIdentityParts[1] -notmatch '^sha256:[0-9a-f]{64}$') { throw "live image digest is malformed" }
if ($liveIdentityParts[3] -ne $expectedMigration) { throw "latest applied migration does not match the checked-out server" }

if ($liveIdentityParts[5] -notmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$') { throw "server start timestamp is malformed" }
Write-Output "deployed SHA: $($liveIdentityParts[0])"
Write-Output "served /version SHA: $($liveIdentityParts[4])"
Write-Output "image digest: $($liveIdentityParts[1])"
Write-Output "migration version: $($liveIdentityParts[3])"

# Two fixed requests prove proxy routing, rendered content and a JSON handler.
# No retries or cold-path latency target can extend the activation transaction.
$representativeSmoke = @'
set -eu
body=$(mktemp)
trap 'rm -f "$body"' EXIT
check() {
  path="$1"; marker="$2"
  code=$(curl --noproxy '*' --connect-timeout 3 --max-time 10 --resolve '__CSX_DOMAIN__:443:127.0.0.1' -sS -o "$body" -w '%{http_code}' "https://__CSX_DOMAIN__$path")
  if [ "$code" != 200 ] || ! grep -qF "$marker" "$body"; then echo "FAIL representative $path: HTTP $code or invalid content" >&2; exit 1; fi
  echo "ok representative $path"
}
check /features '<link rel="canonical" href="https://__CSX_DOMAIN__/features">'
check /version '"revision":"__CSX_REVISION__"'
'@
$representativeSmoke = $representativeSmoke.Replace('__CSX_DOMAIN__', $Domain).Replace('__CSX_REVISION__', $revision)
Invoke-RemoteScript $representativeSmoke | ForEach-Object { Write-Output $_ }

# Final remote commit proves the exact live state and removes only disposable
# candidates. Predeploy snapshots intentionally remain until the next locked
# deployment takes a fresh snapshot, so an unlikely local DPAPI promotion
# failure can still restore remote .env/config/image state exactly.
$commitDeployment = @'
set -eu
cd /opt/codesamplex/deploy
test -f docker-compose.yml
test -f .env
test ! -e docker-compose.yml.candidate
test ! -e .env.new
test ! -e caddy/Caddyfile.candidate
docker compose config --quiet
test "$(docker inspect codesamplex-server-1 --format '{{.State.Running}}')" = true
test "$(docker inspect codesamplex-caddy-1 --format '{{.State.Running}}')" = true
docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/healthz | grep -q '^ok'
'@
Invoke-RemoteScript $commitDeployment | Out-Null

if ($ConfigureAdmin -and $adminCredentialPending) {
    Commit-CSXAdminCredential $adminCredentialPaths.Pending $adminCredentialPaths.Active
    Write-Output "local admin credential committed after final remote deployment commit"
}
} # The canonical host owns minimal acceptance; legacy direct mode checks above.

    # Publish only the state validated INSIDE the rollback boundary. The wrapper
    # must not run a fallible optional collector after this commit.
    $DeploymentEvidence.deployedSha = $liveIdentityParts[0]
    $DeploymentEvidence.imageDigest = $liveIdentityParts[1]
    $DeploymentEvidence.migrationVersion = $liveIdentityParts[3]
    $DeploymentEvidence.servedRevision = $liveIdentityParts[4]
    $DeploymentEvidence.serverStartedAt = $liveIdentityParts[5]
    $DeploymentEvidence.health = "ok"
    $DeploymentEvidence.smoke = "pass"
    $DeploymentEvidence.rollback = "not-needed"
    $serverActivationStarted = $false
    $caddyPromoted = $false
} catch {
    $deployFailure = $_
    Set-DeployPhase rollback 300
    $DeploymentEvidence.rollback = "attempted"
    $script:deployRecoveryMode = $true
    if ($migrationSupervisorStarted) {
        Set-DeployPhase host-recovery 270
        try {
            $hostResult = Resolve-CSXOfflineMigrationOutcome
            if ($hostResult.phase -eq "committed") {
                # A lost SSH result after host acceptance is not an app failure.
                Set-CSXHostDeploymentEvidence $hostResult
                $serverActivationStarted = $false
                $caddyPromoted = $false
                Write-Output "host committed exact acceptance; recovered terminal evidence"
                return
            }
            if ($hostResult.phase -ne "rolled-back" -or $hostResult.cleanup -ne "pass") {
                throw "host cleanup and exact rollback were not proved"
            }
            $script:migrationRecoveryVerified = $true
            $DeploymentEvidence.rollback = "succeeded"
        } catch {
            $script:retainDeployLock = $true
            $DeploymentEvidence.rollback = "unknown-host-outcome"
            $DeploymentEvidence.failureClass = "controller-unresolved"
            throw [AggregateException]::new("host migration outcome unresolved; no controller rollback requested; lock retained", @($deployFailure.Exception, $_.Exception))
        }
        # The host owns cleanup and exact restoration after its launch. Never
        # race a second controller rollback against its finalizer.
        throw $deployFailure
    }
    try {
        Invoke-RemoteScript ('umask 077; touch "$HOME/.csx-deploy-aborted-' + $deployLockOwner + '"') 20 | Out-Null
        $script:deployGenerationFenced = $true
    } catch {
        $script:retainDeployLock = $true
        throw [AggregateException]::new("deployment failed; remote completion could not be fenced; retain lock for owner", @($deployFailure.Exception, $_.Exception))
    }
    if ($script:remoteOutcomeUnknown) {
        # Flocking fences shell execution, not an accepted daemon operation.
        # Never race Docker recovery with an unresolved create/start request.
        $script:retainDeployLock = $true
        $DeploymentEvidence.rollback = "unverified"
        throw [AggregateException]::new("deployment failed; remote or Docker completion is unknown; owner must reconcile before exact rollback", @($deployFailure.Exception))
    }
    $serverRollbackFailure = $null
    $caddyRollbackFailure = $null
    $credentialRollbackFailure = $null
    $restoreDist = if ($distPromoted) { "1" } else { "0" }
    $rollbackServer = @'
set -eu
cd /opt/codesamplex/deploy
restore_dist=__CSX_RESTORE_DIST__
one_of() {
  count=0
  for marker in "$@"; do if [ -e "$marker" ]; then count=$((count + 1)); fi; done
  test "$count" -eq 1
}
one_of docker-compose.yml.rollback-predeploy docker-compose.yml.rollback-absent
one_of .env.rollback-predeploy .env.rollback-absent
one_of server-container.rollback-present server-container.rollback-absent
one_of server-latest.rollback-id server-latest.rollback-absent
if [ -f server-container.rollback-present ]; then
  one_of server-container.rollback-running server-container.rollback-stopped
  test -f server-image.rollback-id
  old=$(cat server-image.rollback-id)
  printf '%s\n' "$old" | grep -Eq '^sha256:[0-9a-f]{64}$'
  test "$(docker image inspect codesamplex/csx-server:rollback-predeploy --format '{{.Id}}')" = "$old"
else
  test ! -e server-image.rollback-id
  test ! -e server-container.rollback-running
  test ! -e server-container.rollback-stopped
fi
if [ -f .env.rollback-predeploy ]; then
  test ! -e .env.rollback-absent
fi
if [ "$restore_dist" -eq 1 ]; then
  # Requested restoration must never silently keep the candidate generation.
  test -f dist.rollback-promoted
  test -d /opt/codesamplex/dist.previous
  test ! -L /opt/codesamplex/dist.previous
fi
if docker container inspect codesamplex-server-1 >/dev/null 2>&1; then docker rm -f codesamplex-server-1 >/dev/null; fi
if [ -f docker-compose.yml.rollback-predeploy ]; then
  cp -p docker-compose.yml.rollback-predeploy docker-compose.yml
else
  rm -f docker-compose.yml
fi
if [ -f .env.rollback-predeploy ]; then
  cp -p .env.rollback-predeploy .env
  chmod 0600 .env
else
  rm -f .env
fi
rm -f docker-compose.yml.candidate .env.new .env.activity.* .env.admin.* caddy/Caddyfile.candidate
if [ "$restore_dist" -eq 1 ]; then
  rm -rf /opt/codesamplex/dist.rollback-stage /opt/codesamplex/dist.failed-rollback
  cp -a /opt/codesamplex/dist.previous /opt/codesamplex/dist.rollback-stage
  if [ -d /opt/codesamplex/dist ]; then mv /opt/codesamplex/dist /opt/codesamplex/dist.failed-rollback; fi
  if mv /opt/codesamplex/dist.rollback-stage /opt/codesamplex/dist; then
    rm -rf /opt/codesamplex/dist.failed-rollback
  else
    mv /opt/codesamplex/dist.failed-rollback /opt/codesamplex/dist
    exit 68
  fi
fi
if [ -f server-container.rollback-present ]; then
  docker tag codesamplex/csx-server:rollback-predeploy codesamplex/csx-server:latest
  if [ -f server-container.rollback-running ]; then
    docker compose up -d --no-build --no-deps --force-recreate server
    test "$(docker inspect codesamplex-server-1 --format '{{.Image}}')" = "$old"
    i=0
    while [ "$i" -lt 24 ]; do
      if docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/healthz 2>/dev/null | grep -q '^ok'; then break; fi
      i=$((i + 1))
      sleep 5
    done
    test "$i" -lt 24
    test "$(docker inspect codesamplex-server-1 --format '{{.State.Running}}')" = true
    expected=$(docker inspect codesamplex-server-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^CSX_VERSION=//p' | head -n 1)
    served=$(docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/version | sed -n 's/.*"revision":"\([0-9a-f]\{40\}\)".*/\1/p' | head -n 1)
    test "$served" = "$expected"
  else
    docker compose up --no-start --no-build --no-deps --force-recreate server >/dev/null
    test "$(docker inspect codesamplex-server-1 --format '{{.Image}}')" = "$old"
    test "$(docker inspect codesamplex-server-1 --format '{{.State.Running}}')" = false
  fi
else
  ! docker container inspect codesamplex-server-1 >/dev/null 2>&1
fi
if [ -f server-latest.rollback-id ]; then
  latest=$(cat server-latest.rollback-id)
  printf '%s\n' "$latest" | grep -Eq '^sha256:[0-9a-f]{64}$'
  test "$(docker image inspect codesamplex/csx-server:rollback-latest-predeploy --format '{{.Id}}')" = "$latest"
  docker tag codesamplex/csx-server:rollback-latest-predeploy codesamplex/csx-server:latest
  test "$(docker image inspect codesamplex/csx-server:latest --format '{{.Id}}')" = "$latest"
else
  if docker image inspect codesamplex/csx-server:latest >/dev/null 2>&1; then docker image rm codesamplex/csx-server:latest >/dev/null; fi
  ! docker image inspect codesamplex/csx-server:latest >/dev/null 2>&1
fi
if [ -f docker-compose.yml.rollback-predeploy ]; then cmp -s docker-compose.yml.rollback-predeploy docker-compose.yml; else test ! -e docker-compose.yml; fi
if [ -f .env.rollback-predeploy ]; then cmp -s .env.rollback-predeploy .env; else test ! -e .env; fi
test ! -e docker-compose.yml.candidate
test ! -e .env.new
'@
    $rollbackServer = $rollbackServer.Replace('__CSX_RESTORE_DIST__', $restoreDist)
    try {
        Invoke-RemoteScript $rollbackServer 170 | Out-Null
        Write-Output "server rollback: exact prior container/image/config/env state proved"
    } catch {
        $serverRollbackFailure = $_
    }

    if ($caddyPromoted) {
        $rollbackCaddy = @'
set -eu
cd /opt/codesamplex/deploy
live=/opt/codesamplex/deploy/caddy/Caddyfile
rollback=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-predeploy
absent=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-absent
candidate=/opt/codesamplex/deploy/caddy/Caddyfile.candidate
container_present=/opt/codesamplex/deploy/caddy/container.rollback-present
container_absent=/opt/codesamplex/deploy/caddy/container.rollback-absent
container_running=/opt/codesamplex/deploy/caddy/container.rollback-running
container_stopped=/opt/codesamplex/deploy/caddy/container.rollback-stopped
image_id=/opt/codesamplex/deploy/caddy/container.rollback-image-id
one_of() {
  count=0
  for marker in "$@"; do if [ -e "$marker" ]; then count=$((count + 1)); fi; done
  test "$count" -eq 1
}
# No snapshot means promotion never started; the live proxy is unchanged.
if [ ! -e "$rollback" ] && [ ! -e "$absent" ]; then exit 0; fi
one_of "$rollback" "$absent"
one_of "$container_present" "$container_absent"
if [ -f "$container_present" ]; then one_of "$container_running" "$container_stopped"; test -f "$image_id"; fi
if docker container inspect codesamplex-caddy-1 >/dev/null 2>&1; then docker rm -f codesamplex-caddy-1 >/dev/null; fi
if [ -f "$rollback" ]; then
  chmod 0644 "$rollback"
  cp -p "$rollback" "$live"
else
  rm -f "$live" "$candidate"
fi
rm -f "$candidate"
if [ -f "$container_present" ]; then
  test -f "$rollback"
  old=$(cat "$image_id")
  printf '%s\n' "$old" | grep -Eq '^sha256:[0-9a-f]{64}$'
  if [ -f "$container_running" ]; then
    docker compose up -d --no-build --no-deps --force-recreate caddy
    test "$(docker inspect codesamplex-caddy-1 --format '{{.Image}}')" = "$old"
    docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
    test "$(docker inspect codesamplex-caddy-1 --format '{{.State.Running}}')" = true
  else
    docker compose up --no-start --no-build --no-deps --force-recreate caddy >/dev/null
    test "$(docker inspect codesamplex-caddy-1 --format '{{.Image}}')" = "$old"
    test "$(docker inspect codesamplex-caddy-1 --format '{{.State.Running}}')" = false
  fi
else
  ! docker container inspect codesamplex-caddy-1 >/dev/null 2>&1
fi
if [ -f "$rollback" ]; then cmp -s "$rollback" "$live"; else test ! -e "$live"; fi
test ! -e "$candidate"
'@
        try {
            Invoke-RemoteScript $rollbackCaddy 80 | Out-Null
            Write-Output "Caddy rollback: restored rollback-predeploy after failed activation"
        } catch {
            $caddyRollbackFailure = $_
        }
    } else {
        try { Invoke-Remote "rm -f /opt/codesamplex/deploy/caddy/Caddyfile.candidate" | Out-Null }
        catch { $caddyRollbackFailure = $_ }
    }

    if ($ConfigureAdmin) {
        try { Restore-CSXAdminCredentialRelationship $adminCredentialPaths $adminCredentialState }
        catch { $credentialRollbackFailure = $_ }
    }

    # Rollbacks are independent and all are attempted. If any rollback fails,
    # aggregate it with the original deployment exception rather than hiding
    # either error behind a warning or a replacement throw.
    $allFailures = New-Object 'System.Collections.Generic.List[System.Exception]'
    $allFailures.Add($deployFailure.Exception)
    foreach ($rollbackFailure in @($serverRollbackFailure, $caddyRollbackFailure, $credentialRollbackFailure)) {
        if ($null -ne $rollbackFailure) { $allFailures.Add($rollbackFailure.Exception) }
    }
    if ($allFailures.Count -gt 1) {
        # A timed-out recovery command may still be queued at the remote
        # flock. Keep the transaction lock until an owner reconciles it.
        $script:retainDeployLock = $true
        throw [AggregateException]::new("deployment failed and one or more exact rollbacks failed", $allFailures.ToArray())
    }
    $DeploymentEvidence.rollback = "succeeded"
    throw $deployFailure
}
Write-Output ""
Write-Output "Deployed. http://$Ip is live; https://$Domain follows DNS propagation."
} catch {
    $deployScriptFailure = $_
    if (-not $script:deployGenerationFenced -and -not $script:retainDeployLock) {
        Set-DeployPhase failure-fence 20
        $script:deployRecoveryMode = $true
        try {
            Invoke-RemoteScript ('umask 077; touch "$HOME/.csx-deploy-aborted-' + $deployLockOwner + '"') 10 | Out-Null
            $script:deployGenerationFenced = $true
        } catch { $script:retainDeployLock = $true }
        if ($script:remoteOutcomeUnknown) { $script:retainDeployLock = $true }
    }
    throw
} finally {
    Set-DeployPhase cleanup 60
    try {
    if ($null -ne $adminCredentialState -and $null -eq $credentialRollbackFailure) {
        # These are DPAPI ciphertext copies, retained only long enough to
        # prove/restore the coordinated local state transition.
        [IO.File]::Delete($adminCredentialState.ActiveBackup)
        [IO.File]::Delete($adminCredentialState.PendingBackup)
    }
    # Clean only per-invocation artifacts whose names contain this lock
    # owner's validated random token. Cleanup errors are warnings; lock
    # release below remains mandatory and gets its own error handling.
    $expectedImageTar = Join-Path ([IO.Path]::GetTempPath()) "csx-server-image-$deployLockOwner.tar"
    if ($null -ne $imageTar -and $imageTar -eq $expectedImageTar -and (Test-Path -LiteralPath $imageTar -PathType Leaf)) {
        try { Remove-Item -LiteralPath $imageTar -Force }
        catch { Write-Warning "could not remove the per-deploy local image tar" }
    }
    if ($localImageCleanupNeeded -and $localImageTag -eq "codesamplex/csx-server:deploy-$deployLockOwner") {
        try { Invoke-DeployProcess docker @("image", "rm", $localImageTag) 15 | Out-Null }
        catch { Write-Warning "could not remove the per-deploy local image tag" }
    }
    foreach ($temporaryFile in @($tagTmp, $envTmp)) {
        if ($null -ne $temporaryFile -and (Test-Path -LiteralPath $temporaryFile -PathType Leaf)) {
            try { Remove-Item -LiteralPath $temporaryFile -Force }
            catch { Write-Warning "could not remove a per-deploy local temporary file" }
        }
    }
    if ($migrationSupervisorStarted -and (-not $migrationSupervisorTerminal -or -not $migrationRecoveryVerified)) {
        $script:retainDeployLock = $true
        Write-Warning "host migration recovery unresolved; deployment lock retained"
    }
    if ($deployLockHeld -and -not $script:retainDeployLock) {
        $releaseDeployLock = @'
set -eu
lock=/opt/codesamplex/.deploy-lock
owner=__CSX_DEPLOY_OWNER__
test "$(readlink -f "$lock")" = /opt/codesamplex/.deploy-lock
test -f "$lock/owner"
test ! -L "$lock/owner"
test "$(cat "$lock/owner")" = "$owner"
test "$(find "$lock" -mindepth 1 -maxdepth 1 | wc -l)" -eq 1
rm -f "$lock/owner"
rmdir "$lock"
'@
        $releaseDeployLock = $releaseDeployLock.Replace('__CSX_DEPLOY_OWNER__', $deployLockOwner)
        try {
            Invoke-RemoteScript $releaseDeployLock 15 | Out-Null
            $deployLockHeld = $false
        } catch {
            if ($null -ne $deployScriptFailure) {
                Write-Warning "deploy failed and its exact remote lock could not be released; original failure is preserved and an operator must inspect the lock owner"
            } else {
                throw
            }
        }
    }
    if (-not $script:retainDeployLock -and $null -ne $remoteImageTar -and $remoteImageTar -eq "/opt/codesamplex/csx-server-image-$deployLockOwner.tar") {
        try { Invoke-Remote "rm -f $remoteImageTar; docker image rm $localImageTag >/dev/null 2>&1 || true" | Out-Null }
        catch { Write-Warning "could not remove per-deploy remote image artifacts" }
    }
    } finally { Complete-DeployPhase }
}
