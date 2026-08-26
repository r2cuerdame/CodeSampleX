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
    [switch]$RequireNoLegacyAccessLogs,
    [switch]$SkipImage,
    [switch]$ConfigureAdmin,
    [switch]$RotateAdmin
)
$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
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
    $prev = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $out = & $sshExecutable @sshArgs $remote $Script 2>&1 | ForEach-Object { "$_" }
    } finally { $ErrorActionPreference = $prev }
    if ($LASTEXITCODE -ne 0) { throw "remote command failed ($LASTEXITCODE): $Script`n$($out -join "`n")" }
    return $out
}
function Invoke-RemoteScript([string]$Script) {
    if ([string]::IsNullOrWhiteSpace($Script)) { throw "refusing an empty remote script" }
    if ($resolvedKeyPath.Contains('"') -or $resolvedKnownHostsPath.Contains('"')) { throw "SSH configuration path contains an unsupported quote" }
    if ($remote -notmatch '^[A-Za-z0-9._-]+@[A-Za-z0-9.:-]+$') { throw "unsafe SSH destination" }

    # Windows OpenSSH does not preserve nested shell quotes when an entire
    # multiline program is passed as one argv element. Send programs on stdin
    # to a fixed `sh -s` command so regex pipes, quotes and redirects arrive
    # byte-for-byte. Secrets use Invoke-RemoteInput instead.
    $scriptBytes = (New-Object Text.UTF8Encoding($false)).GetBytes($Script)
    $psi = New-Object Diagnostics.ProcessStartInfo
    $psi.FileName = $sshExecutable
    $psi.Arguments = '-i "' + $resolvedKeyPath + '" -o StrictHostKeyChecking=yes -o UserKnownHostsFile="' + $resolvedKnownHostsPath + '" -o ConnectTimeout=20 ' + $remote + ' "sh -s"'
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
            $process.StandardInput.BaseStream.Write($scriptBytes, 0, $scriptBytes.Length)
        } finally {
            $process.StandardInput.Close()
            [Array]::Clear($scriptBytes, 0, $scriptBytes.Length)
        }
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) {
            $detail = (($stdout, $stderr) -join "`n").Trim()
            if ($detail.Length -gt 4096) { $detail = $detail.Substring($detail.Length - 4096) }
            throw "remote script failed ($($process.ExitCode))`n$detail"
        }
        if (-not [string]::IsNullOrWhiteSpace($stdout)) {
            return @($stdout.TrimEnd() -split "`r?`n")
        }
    } finally {
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
    $payload = "CSX-STDIN-V1`nhash='$StdinText'`n$Script`n"
    $payloadBytes = (New-Object Text.UTF8Encoding($false)).GetBytes($payload)
    $psi = New-Object Diagnostics.ProcessStartInfo
    $psi.FileName = $sshExecutable
    $psi.Arguments = '-i "' + $resolvedKeyPath + '" -o StrictHostKeyChecking=yes -o UserKnownHostsFile="' + $resolvedKnownHostsPath + '" -o ConnectTimeout=20 ' + $remote + ' "{ printf ''#''; cat; } | sh"'
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
            $process.StandardInput.BaseStream.Write($payloadBytes, 0, $payloadBytes.Length)
        } finally {
            $process.StandardInput.Close()
            [Array]::Clear($payloadBytes, 0, $payloadBytes.Length)
            $payload = $null
        }
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) {
            # The remote program never echoes the verifier. Keep failures
            # generic anyway so a future edit cannot turn logs into a leak.
            throw "remote verifier installation failed ($($process.ExitCode))"
        }
        if (-not [string]::IsNullOrWhiteSpace($stdout)) { return $stdout.TrimEnd() }
    } finally {
        if ($null -ne $payloadBytes) { [Array]::Clear($payloadBytes, 0, $payloadBytes.Length) }
        $payload = $null
        $stdout = $null
        $stderr = $null
        $process.Dispose()
    }
}
function Copy-Remote([string]$Local, [string]$RemotePath) {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & $scpExecutable -i $resolvedKeyPath -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$resolvedKnownHostsPath" -o ConnectTimeout=20 $Local "${remote}:${RemotePath}" 2>&1 | Out-Null
    } finally { $ErrorActionPreference = $prev }
    if ($LASTEXITCODE -ne 0) { throw "scp failed: $Local -> $RemotePath" }
}

# docker writes its whole build log to stderr, which PowerShell 5.1 turns
# into a terminating error under ErrorActionPreference Stop even on exit 0.
# Same treatment as ssh/scp: run it under Continue, judge by exit code.
function Invoke-Native([string]$What, [scriptblock]$Run) {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try { & $Run 2>&1 | ForEach-Object { Write-Output "$_" } }
    finally { $ErrorActionPreference = $prev }
    if ($LASTEXITCODE -ne 0) { throw "$What failed ($LASTEXITCODE)" }
}

# The lock covers local credential state, the fixed Docker tag/tar, every
# remote candidate/rollback filename, activation and smoke. Creating the
# parent is the only pre-lock remote mutation and is idempotent for a
# first-ever host.
Invoke-Remote "sudo install -d -o $User -g $User /opt/codesamplex" | Out-Null
$deployLockOwner = [guid]::NewGuid().ToString("N")
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
$revision = (& git -C $repo rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $revision -notmatch '^[0-9a-f]{40}$') { throw "could not determine the server revision" }
if ($ExpectedRevision -ne "" -and $revision -ne $ExpectedRevision) {
    throw "checked-out revision does not match -ExpectedRevision"
}
# The human-readable half of the build identity. `git describe` names the
# release line this commit sits on and how far past it; the footer renders it
# beside the short revision, and /version serves both. Derived here rather
# than inside the image because the build context deliberately excludes .git.
$buildVersion = (& git -C $repo describe --tags --always).Trim()
if ($LASTEXITCODE -ne 0 -or $buildVersion -eq "") { throw "could not determine the server build version" }
$builtAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")

$expectedMigration = (Get-ChildItem (Join-Path $repo "internal/serverstore/migrations") -Filter "*.sql" -File | Sort-Object Name | Select-Object -Last 1).Name
if ($expectedMigration -notmatch '^[0-9]{4}_[a-z0-9_]+\.sql$') { throw "could not determine the expected migration version" }

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
Write-Output "previous production SHA: $($productionStateParts[0])"
Write-Output "previous production image: $($productionStateParts[1])"

$collectInvariantScript = @'
set -eu
cd /opt/codesamplex/deploy
server_started=$(docker inspect codesamplex-server-1 --format '{{.State.StartedAt}}')
builder_generated=$(docker compose exec -T db psql -U csx -d csx -Atqc \
  "SELECT COALESCE(stats->>'generatedAt','') FROM stats_daily ORDER BY day DESC LIMIT 1")
server_epoch=$(date -u -d "$server_started" +%s 2>/dev/null || true)
builder_epoch=$(date -u -d "$builder_generated" +%s 2>/dev/null || true)
builder_fresh=0
if [ -n "$server_epoch" ] && [ -n "$builder_epoch" ] && [ "$builder_epoch" -ge "$server_epoch" ]; then
  builder_fresh=1
fi
# Read the materialization only after the completion marker. If the first
# full pass finishes between these probes, this iteration still reports
# builder_fresh=0 and the caller retries; it can never bless a mid-pass tuple.
values=$(docker compose exec -T db psql -U csx -d csx -At -F '|' -c "
SELECT
  COALESCE(SUM(observation_count) FILTER (WHERE result='PASS'),0),
  COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0),
  (SELECT count(*) FROM samples WHERE status='PUBLISHED'),
  (SELECT COALESCE(SUM(observation_count),0) FROM failure_clusters
    WHERE COALESCE(evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete')
       OR COALESCE(error_fp,'') = ''),
  COALESCE(SUM(observation_count) FILTER (WHERE purl='pkg:golang/github.com/jackc/pgx/v5@v5.10.0' AND symbol='ParseConfig' AND result='PASS'),0),
  COALESCE(SUM(observation_count) FILTER (WHERE purl='pkg:golang/github.com/jackc/pgx/v5@v5.10.0' AND symbol='ParseConfig' AND result='FAIL'),0),
  (SELECT count(*) FROM failure_clusters fc
    WHERE (COALESCE(fc.evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete')
           OR COALESCE(fc.error_fp,'') = '')
      AND (fc.observation_count <= 0
           OR EXISTS (
             SELECT 1 FROM jsonb_each(fc.evidence_breakdown) AS item(key, value)
             WHERE item.key NOT IN ('complete','partial','missing','legacy-evidence-incomplete')
                OR jsonb_typeof(item.value) <> 'number'
                OR CASE WHEN jsonb_typeof(item.value) = 'number'
                        THEN (item.value::text)::numeric < 0 ELSE false END)
           OR fc.observation_count::numeric <> COALESCE((
             SELECT SUM((item.value::text)::numeric)
             FROM jsonb_each(fc.evidence_breakdown) AS item(key, value)
             WHERE jsonb_typeof(item.value) = 'number'), 0)))
FROM evidence_agg")
printf '%s|%s\n' "$values" "$builder_fresh"
'@
$invariantsBefore = if ($productionStateParts[0] -eq "none") { "0|0|0|0|0|0|0|0" } else { (Invoke-RemoteScript $collectInvariantScript | Select-Object -First 1).Trim() }
if ($invariantsBefore -notmatch '^\d+\|\d+\|\d+\|\d+\|\d+\|\d+\|\d+\|[01]$') { throw "malformed pre-deploy invariants" }
$beforeValues = @($invariantsBefore -split '\|' | ForEach-Object { [int64]$_ })
Write-Output "deployment invariants before: $invariantsBefore"

$assertNoLegacyAccessLogs = @'
set -eu
cd /opt/codesamplex/deploy
docker compose exec -T caddy sh -s <<'CSX_LEGACY_ACCESS_PREFLIGHT'
set -eu
old_dir=/var/log/caddy
test "$(readlink -f "$old_dir")" = /var/log/caddy
for old in "$old_dir"/access.log "$old_dir"/access-*.log "$old_dir"/access-*.log.gz; do
  [ ! -e "$old" ] || { printf 'legacy query-bearing access log requires a manual privacy cleanup\n' >&2; exit 69; }
done
CSX_LEGACY_ACCESS_PREFLIGHT
'@
if ($RequireNoLegacyAccessLogs) {
    Invoke-RemoteScript $assertNoLegacyAccessLogs | Out-Null
    Write-Output "automatic deploy irreversible-cleanup preflight: clear"
}

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
    "codesamplex-mcp.mcpb",
    "codesamplex-mcp.mcpb.sha256"
)

function Assert-ReleaseDirectory([string]$Directory) {
    $actual = @(Get-ChildItem $Directory -File | ForEach-Object { $_.Name } | Sort-Object)
    $expected = @($requiredReleaseAssets | Sort-Object)
    $difference = @(Compare-Object $expected $actual)
    if ($difference.Count -ne 0) {
        throw "release asset set is incomplete or unexpected: $($difference | Out-String)"
    }

    $verified = @{}
    foreach ($line in Get-Content (Join-Path $Directory "SHA256SUMS.txt")) {
        $parts = $line -split '\s+', 2
        if ($parts.Count -ne 2) { throw "malformed SHA256SUMS.txt line" }
        $name = $parts[1].TrimStart('*').Trim()
        $file = Join-Path $Directory $name
        if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "checksum names missing asset: $name" }
        $have = (Get-FileHash $file -Algorithm SHA256).Hash.ToLower()
        if ($have -ne $parts[0].ToLower()) { throw "checksum mismatch for $name" }
        $verified[$name] = $true
    }
    foreach ($name in $requiredReleaseAssets[0..8]) {
        if (-not $verified[$name]) { throw "SHA256SUMS.txt does not cover $name" }
    }

    $bundleLine = @(Get-Content (Join-Path $Directory "codesamplex-mcp.mcpb.sha256"))
    if ($bundleLine.Count -ne 1) { throw "malformed MCPB checksum file" }
    $bundleParts = $bundleLine[0] -split '\s+', 2
    if ($bundleParts.Count -ne 2 -or $bundleParts[1].TrimStart('*').Trim() -ne "codesamplex-mcp.mcpb") {
        throw "MCPB checksum names the wrong asset"
    }
    $bundleHash = (Get-FileHash (Join-Path $Directory "codesamplex-mcp.mcpb") -Algorithm SHA256).Hash.ToLower()
    if ($bundleHash -ne $bundleParts[0].ToLower()) { throw "MCPB checksum mismatch" }
}

$localImageTag = "codesamplex/csx-server:deploy-$deployLockOwner"
$imageTar = Join-Path ([IO.Path]::GetTempPath()) "csx-server-image-$deployLockOwner.tar"
if (-not $SkipImage) {
	$localImageCleanupNeeded = $true
    Write-Output "== building linux/amd64 server image =="
    $dockerfile = Join-Path (Join-Path $repo "deploy") "Dockerfile.server"
    Invoke-Native "docker build" {
        & docker build --platform linux/amd64 `
            --build-arg "CSX_VERSION=$revision" `
            --build-arg "CSX_BUILD_VERSION=$buildVersion" `
            --build-arg "CSX_BUILT_AT=$builtAt" `
            --build-arg "CSX_ENV=production" `
            -f $dockerfile -t $localImageTag $repo
    }
    # No gzip on Windows PowerShell; ship the plain tar (ssh compresses).
    Invoke-Native "docker save" { & docker save $localImageTag -o $imageTar }
}

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
# Validate syntax and privacy semantics with the exact production Caddy image
# before touching the running proxy. The probe log lives only in tmpfs. It
# proves a known API is coarsened and an unknown user-controlled route is not
# logged at all, while the old in-memory production config keeps serving.
$caddyConfigPreflight = @'
set -eu
name=csx-caddy-config-preflight
candidate=/opt/codesamplex/deploy/caddy/Caddyfile.candidate
passed=0
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  if [ "$passed" -eq 0 ]; then rm -f "$candidate"; fi
}
trap cleanup EXIT HUP INT TERM
docker rm -f "$name" >/dev/null 2>&1 || true
test -f "$candidate"
docker run -d --name "$name" --tmpfs /var/log/caddy-safe:rw,mode=755 \
  -e CADDY_SITE=:18080 \
  -v "$candidate":/etc/caddy/Caddyfile:ro \
  caddy:2.11.4-alpine >/dev/null
i=0
while [ "$i" -lt 10 ]; do
  docker exec "$name" wget -q -T 3 -t 1 -O /dev/null 'http://127.0.0.1:18080/v1/samples/known-id-must-not-log?query-must-not-log=1' >/dev/null 2>&1 || true
  if docker exec "$name" sh -c "grep -q '\"csx_route\":\"samples\"' /var/log/caddy-safe/access-safe.log 2>/dev/null"; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
docker exec "$name" wget -q -T 3 -t 1 -O /dev/null 'http://127.0.0.1:18080/v1/samples%2Fencoded-id-must-not-log/path' >/dev/null 2>&1 || true
docker exec "$name" wget -q -T 3 -t 1 -O /dev/null 'http://127.0.0.1:18080/v1/unknown-secret-must-not-log/path' >/dev/null 2>&1 || true
docker exec -i "$name" sh -s <<'CSX_CADDY_PREFLIGHT_SMOKE'
  test "$(stat -c %a /var/log/caddy-safe/access-safe.log)" = 644
  grep -q '"csx_route":"samples"' /var/log/caddy-safe/access-safe.log
  grep -q '"csx_method":"get_head"' /var/log/caddy-safe/access-safe.log
  ! grep -Eq 'known-id-must-not-log|query-must-not-log|encoded-id-must-not-log|unknown-secret-must-not-log|remote_ip|client_ip|headers|user_id|"request"' /var/log/caddy-safe/access-safe.log
  ! grep -q '?' /var/log/caddy-safe/access-safe.log
CSX_CADDY_PREFLIGHT_SMOKE
docker rm -f "$name" >/dev/null 2>&1
passed=1
'@
Invoke-RemoteScript $caddyConfigPreflight | Out-Null
Write-Output "Caddy privacy-safe log preflight: passed"
Copy-Remote (Join-Path $repo "deploy\backup.sh") "/opt/codesamplex/deploy/backup.sh"
Copy-Remote (Join-Path $repo "deploy\restore-check.sh") "/opt/codesamplex/deploy/restore-check.sh"
Invoke-Remote "chmod 755 /opt/codesamplex/deploy/backup.sh /opt/codesamplex/deploy/restore-check.sh" | Out-Null
Copy-Remote (Join-Path $repo "schemas\v1\adapters.json") "/opt/codesamplex/schemas/v1/adapters.json"

# The download endpoint is fed from the LATEST GITHUB RELEASE, never from
# whatever happens to be sitting in dist/.
#
# It used to ship the local folder, and the local folder was last built by
# hand. The result: every deploy re-shipped v0.1.0 to codesamplex.dev/dl
# while GitHub's latest was v0.1.2, so everyone who followed the README got
# a binary two releases old — including the one that inferred consent from
# EOF, which meant `curl ... | sh` enrolled people in evidence sharing
# without anyone answering the question. Two sources of truth, and the
# hand-fed one was the one users actually got.
$tag = (& gh release view --repo r2cuerdame/CodeSampleX --json tagName --jq .tagName)
if ($LASTEXITCODE -ne 0 -or -not $tag) { throw "could not read the latest release tag" }
if ($tag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$') { throw "unsafe release tag: $tag" }
$remoteTagLine = Invoke-Remote "cat /opt/codesamplex/dist/.release-tag 2>/dev/null || true" | Select-Object -First 1
$remoteTag = if ($null -eq $remoteTagLine) { "" } else { ([string]$remoteTagLine).Trim() }
$remoteFiles = ($requiredReleaseAssets | ForEach-Object { "test -f /opt/codesamplex/dist/$_" }) -join " && "
$remoteValidation = ('set -eu; {0}; cd /opt/codesamplex/dist; sha256sum -c SHA256SUMS.txt >/dev/null; sha256sum -c codesamplex-mcp.mcpb.sha256 >/dev/null; test "$(find . -maxdepth 1 -type f ! -name .release-tag | wc -l)" -eq {1}' -f $remoteFiles, $requiredReleaseAssets.Count)
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
    $dist = Join-Path $repo "dist"
    New-Item -ItemType Directory -Force $dist | Out-Null
    Write-Output "== fetching release artifacts for $tag =="
    Get-ChildItem $dist -File | Remove-Item -Force
    Invoke-Native "gh release download" {
        & gh release download $tag --repo r2cuerdame/CodeSampleX --dir $dist --clobber
    }
    Assert-ReleaseDirectory $dist
    Write-Output "checksums and exact release asset set verified"

    # Stage beside the live directory. The server keeps its old bind mount
    # while these files transfer, so it can never serve a partially copied
    # executable. The compose restart below remounts the atomically swapped
    # directory only after every checksum passes on the host too.
    $stage = "/opt/codesamplex/dist.stage"
    Invoke-Remote "rm -rf /opt/codesamplex/dist.stage && mkdir -p /opt/codesamplex/dist.stage" | Out-Null
    Get-ChildItem $dist -File | ForEach-Object { Copy-Remote $_.FullName "$stage/$($_.Name)" }
    $tagTmp = Join-Path ([IO.Path]::GetTempPath()) "csx-release-tag-$deployLockOwner.txt"
    Set-Content -Path $tagTmp -Value $tag -Encoding ascii -NoNewline
    Copy-Remote $tagTmp "$stage/.release-tag"
    $stageFiles = ($requiredReleaseAssets | ForEach-Object { "test -f $stage/$_" }) -join " && "
    $stageValidation = ('set -eu; {0}; cd /opt/codesamplex/dist.stage; sha256sum -c SHA256SUMS.txt >/dev/null; sha256sum -c codesamplex-mcp.mcpb.sha256 >/dev/null; test "$(find . -maxdepth 1 -type f ! -name .release-tag | wc -l)" -eq {1}' -f $stageFiles, $requiredReleaseAssets.Count)
    Invoke-Remote $stageValidation | Out-Null
    Invoke-Remote "set -eu; rm -rf /opt/codesamplex/dist.previous; mv /opt/codesamplex/dist /opt/codesamplex/dist.previous; if mv /opt/codesamplex/dist.stage /opt/codesamplex/dist; then :; else mv /opt/codesamplex/dist.previous /opt/codesamplex/dist; exit 1; fi" | Out-Null
    $distPromoted = $true
    Write-Output "atomically shipped $($requiredReleaseAssets.Count) artifacts from $tag"
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
    # Keep one exact rollback copy until the live privacy smoke passes.
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
  if [ "$promoted" -eq 0 ]; then rm -f "$rollback" "$absent" "$container_present" "$container_absent" "$container_running" "$container_stopped" "$image_id"; fi
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
    Invoke-RemoteScript $promoteCaddy | Out-Null
    $caddyPromoted = $true

Write-Output "== starting stack =="
# A release refresh swaps the host dist directory atomically. An existing
# bind mount keeps the old directory inode even after the host path is
# replaced, so `compose up` without recreation can keep serving the previous
# release forever. Recreate the server explicitly on every deploy: image
# upgrades need the same guarantee, and its healthcheck bounds the restart.
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose up -d --no-build --force-recreate server" | Out-Null
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose up -d --no-build --remove-orphans" | Out-Null
# Caddy documents that file-output option changes require a server restart,
# not only a config reload. Recreate this single proxy after the healthy app
# is ready, then reload once more as an explicit live-config validation.
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose up -d --no-build --force-recreate caddy" | Out-Null
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile" | Out-Null
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose ps" | ForEach-Object { Write-Output $_ }
if (-not $SkipImage) {
    $imagePair = Invoke-Remote 'set -eu; expected=$(docker image inspect codesamplex/csx-server:latest --format ''{{.Id}}''); actual=$(docker inspect codesamplex-server-1 --format ''{{.Image}}''); echo $expected $actual' | Select-Object -First 1
    $ids = (([string]$imagePair).Trim() -split '\s+')
    if ($ids.Count -ne 2 -or $ids[0] -ne $ids[1]) { throw "server container is not running the image that was just loaded" }
    Write-Output "server image: $($ids[0])"
}

Write-Output "== smoke test =="
$ok = $false
$prevEAP = $ErrorActionPreference
$ErrorActionPreference = "Continue"
for ($i = 0; $i -lt 24; $i++) {
    Start-Sleep -Seconds 5
    # Ask the app container directly: through Caddy a production deployment
    # answers 308 (HTTP→HTTPS) and following that from the host re-enters
    # TLS with the public hostname, which proves nothing about this build.
    $health = & $sshExecutable @sshArgs $remote "cd /opt/codesamplex/deploy && docker compose exec -T server wget -qO- http://127.0.0.1:8080/healthz 2>/dev/null || true" 2>&1 | ForEach-Object { "$_" }
    if ($health -match "ok") { $ok = $true; break }
}
$ErrorActionPreference = $prevEAP
if (-not $ok) {
    Invoke-Remote "cd /opt/codesamplex/deploy && docker compose logs --tail 40 server" | ForEach-Object { Write-Output $_ }
    throw "healthz never returned ok"
}
Write-Output "healthz: ok"

# Start the privacy-safe log's collection epoch once, then prove the live
# encoder strips queries and path IDs before disk. The application container
# must read the 0644 safe log through its read-only dedicated volume; the
# historical query-bearing access.log is not mounted into its namespace.
$safeAccessLogSmoke = @'
set -eu
cd /opt/codesamplex/deploy
docker compose exec -T caddy sh -s <<'CSX_SAFE_LOG_EPOCH'
  umask 022
  marker=/var/log/caddy-safe/access-safe.log.since
  if [ ! -f "$marker" ]; then
    tmp=$(mktemp /var/log/caddy-safe/.access-safe.since.XXXXXX)
    date -u +%Y-%m-%dT%H:%M:%SZ > "$tmp"
    chmod 0644 "$tmp"
    mv "$tmp" "$marker"
  fi
CSX_SAFE_LOG_EPOCH
curl --noproxy '*' --connect-timeout 5 --max-time 10 --resolve '__CSX_DOMAIN__:443:127.0.0.1' -sS -o /dev/null 'https://__CSX_DOMAIN__/v1/stats?csx_safe_log_smoke=discard-this-query'
curl --noproxy '*' --connect-timeout 5 --max-time 10 --resolve '__CSX_DOMAIN__:443:127.0.0.1' -sS -o /dev/null 'https://__CSX_DOMAIN__/v1/samples%2Fencoded-marker-must-not-log/path'
curl --noproxy '*' --connect-timeout 5 --max-time 10 --resolve '__CSX_DOMAIN__:443:127.0.0.1' -sS -o /dev/null 'https://__CSX_DOMAIN__/v1/secret-marker-must-not-log/path'
i=0
while [ "$i" -lt 10 ]; do
  if docker compose exec -T caddy sh -c "grep -q '\"csx_route\":\"stats\"' /var/log/caddy-safe/access-safe.log 2>/dev/null"; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
docker compose exec -T caddy sh -s <<'CSX_SAFE_LOG_VERIFY'
  test -f /var/log/caddy-safe/access-safe.log
  test "$(stat -c %a /var/log/caddy-safe/access-safe.log)" = 644
  test "$(stat -c %a /var/log/caddy-safe/access-safe.log.since)" = 644
  ! grep -q "discard-this-query" /var/log/caddy-safe/access-safe.log
  ! grep -q "encoded-marker-must-not-log" /var/log/caddy-safe/access-safe.log
  ! grep -q "secret-marker-must-not-log" /var/log/caddy-safe/access-safe.log
  ! grep -q '?' /var/log/caddy-safe/access-safe.log
  grep -q '"csx_method":"get_head"' /var/log/caddy-safe/access-safe.log
  ! grep -Eq 'remote_ip|client_ip|headers|user_id|"request"' /var/log/caddy-safe/access-safe.log
CSX_SAFE_LOG_VERIFY
docker compose exec -T server sh -s <<'CSX_SAFE_LOG_SERVER_VERIFY'
  test -r /var/log/caddy-safe/access-safe.log
  test -r /var/log/caddy-safe/access-safe.log.since
  test ! -e /var/log/caddy/access.log
CSX_SAFE_LOG_SERVER_VERIFY
'@
$safeAccessLogSmoke = $safeAccessLogSmoke.Replace('__CSX_DOMAIN__', $Domain)
Invoke-RemoteScript $safeAccessLogSmoke | ForEach-Object { Write-Output $_ }
Write-Output "privacy-safe API access log: query-free, bounded, server-readable"

# The pages and files a stranger actually lands on.
#
# /healthz above proves the process is up, which is a different question from
# whether the site works: a template that fails to parse, a route that stops
# being registered, a static file that stops being served, or a Caddy rule
# that swallows a path all leave healthz green and the public surface broken.
# Nothing checked them, so "the code is right but the deployment is broken"
# was a class of failure that could only be found by a user.
#
# Each check goes through Caddy on the real hostname (--resolve pins it to the
# loopback so this is THIS box, not whatever DNS currently points at) and
# asserts three things: 200, the right content type, and one marker that only
# that page's own template can produce. The canonical link is that marker
# because it is per-path and never translated — a soft-404 that renders the
# landing page instead has the landing page's canonical, and fails here.
$publicSurfaceSmoke = @'
set -u
DOMAIN=__CSX_DOMAIN__
fail=0
check() {
    path="$1"; want_type="$2"; marker="$3"
    hdr=$(mktemp); body=$(mktemp)
    # No `|| echo 000` here. curl already writes 000 to %{http_code} when it
    # never got a response, so the fallback appended a SECOND 000 and the
    # failure line read "HTTP 000000" — a status that does not exist, on the
    # one line someone reads when a deploy has just broken the site.
    code=$(curl --noproxy '*' --connect-timeout 5 --max-time 25 \
        --resolve "$DOMAIN:443:127.0.0.1" \
        -sS -D "$hdr" -o "$body" -w '%{http_code}' "https://$DOMAIN$path")
    [ -n "$code" ] || code=000
    if [ "$code" != "200" ]; then
        echo "FAIL $path: HTTP $code"; fail=1
    elif ! grep -qi "^content-type: *$want_type" "$hdr"; then
        echo "FAIL $path: content-type $(grep -i '^content-type:' "$hdr" | tr -d '\r')," \
             "want $want_type"; fail=1
    elif ! grep -qF "$marker" "$body"; then
        echo "FAIL $path: body is missing its own marker ($marker)"; fail=1
    else
        echo "ok   $path ($(wc -c < "$body") bytes)"
    fi
    rm -f "$hdr" "$body"
}

canonical() { printf '<link rel="canonical" href="https://%s%s">' "$DOMAIN" "$1"; }

check /          'text/html'        "$(canonical /)"
check /wanted    'text/html'        "$(canonical /wanted)"
check /records   'text/html'        "$(canonical /records)"
check /findings  'text/html'        "$(canonical /findings)"
check /features  'text/html'        "$(canonical /features)"
check /v1/stats  'application/json' '"packages":'
check /v1/wanted 'application/json' '"schemaVersion"'
# The install one-liners the README and every directory listing publish. A
# 404 here is the worst possible first impression and is invisible from the
# inside: `curl ... | sh` exits 0 on a missing file.
check /install.sh  'text/plain' 'SHA256SUMS.txt'
check /install.ps1 'text/plain' 'Get-FileHash'

if [ "$fail" -ne 0 ]; then
    echo "public surface smoke FAILED"
    exit 1
fi
echo "public surface smoke: all ok"
'@
$publicSurfaceSmoke = $publicSurfaceSmoke.Replace('__CSX_DOMAIN__', $Domain)
Invoke-RemoteScript $publicSurfaceSmoke | ForEach-Object { Write-Output $_ }

    # Match the route state to the verifier in the live environment on every
    # rollout. Configured admin must be 401 without credentials; deliberately
    # unconfigured admin must remain 404.
    $adminProbe = @'
set -eu
cd /opt/codesamplex/deploy
response=$(docker compose exec -T server wget -S -O /dev/null http://127.0.0.1:8080/admin 2>&1 || true)
if grep -Eq '^CSX_ADMIN_TOKEN_SHA256=[0-9a-f]{64}$' .env; then
  printf '%s\n' "$response" | grep -q '401'
else
  printf '%s\n' "$response" | grep -q '404'
fi
'@
    Invoke-RemoteScript $adminProbe | Out-Null
    Write-Output "admin route state: matches live verifier configuration"

if ($ConfigureAdmin) {
    # Prove the local DPAPI credential and remote verifier are the same value.
    # The request is made with a header (never a URL credential), follows no
    # redirects, and exposes only its numeric status.
    $adminCredentialFile = if ($adminCredentialPending) { $adminCredentialPaths.Pending } else { $adminCredentialPaths.Active }
    $adminStatus = 0
    for ($i = 0; $i -lt 10 -and $adminStatus -ne 200; $i++) {
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

# Prove the dedicated key reached the server and migration 0008 is queryable.
# After an authenticated admin smoke, also require fresh owner rows for both
# current epochs; this verifies retroactive exclusion without persisting a raw
# address or polluting the external estimate with a synthetic network.
$activityOwnerCheck = ":"
if ($ConfigureAdmin) {
    $activityOwnerCheck = @'
owner_epochs=$(docker compose exec -T db psql -U csx -d csx -Atqc "SELECT COUNT(DISTINCT kind) FROM activity_buckets WHERE owner AND last_seen >= now() - interval '5 minutes' AND ((kind='day' AND epoch=to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD')) OR (kind='month' AND epoch=to_char(now() AT TIME ZONE 'UTC','YYYY-MM')))" )
test "$owner_epochs" = 2
'@
}
$activitySmoke = @'
set -eu
cd /opt/codesamplex/deploy
docker compose exec -T server sh -c 'printf "%s\n" "$CSX_ACTIVITY_HASH_KEY" | grep -Eq "^[0-9a-f]{64}$"'
present=$(docker compose exec -T db psql -U csx -d csx -Atqc "SELECT to_regclass('public.activity_buckets') IS NOT NULL")
test "$present" = t
health_present=$(docker compose exec -T db psql -U csx -d csx -Atqc "SELECT to_regclass('public.activity_health') IS NOT NULL")
test "$health_present" = t
columns=$(docker compose exec -T db psql -U csx -d csx -Atqc "SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name='activity_buckets'")
test "$columns" = kind,epoch,bucket,owner,first_seen,last_seen
__CSX_ACTIVITY_OWNER_CHECK__
'@
$activitySmoke = $activitySmoke.Replace('__CSX_ACTIVITY_OWNER_CHECK__', $activityOwnerCheck)
Invoke-RemoteScript $activitySmoke | Out-Null
Write-Output "activity estimate smoke: key and migration ready$(if ($ConfigureAdmin) { '; fresh owner exclusion recorded' } else { '' })"

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
migration=$(docker compose exec -T db psql -U csx -d csx -Atqc "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
served=$(docker compose exec -T server wget -qO- http://127.0.0.1:8080/version |
  sed -n 's/.*"revision":"\([0-9a-f]\{40\}\)".*/\1/p' | head -n 1)
printf '%s|%s|%s|%s|%s\n' "$revision" "$image" "$label" "$migration" "$served"
'@
$liveIdentity = (Invoke-RemoteScript $liveIdentityScript | Select-Object -First 1).Trim()
$liveIdentityParts = $liveIdentity -split '\|'
# The container environment and the image label say what was configured
# and what was built. Only /version says what the process now answering
# requests was built from, which is what this deploy is claiming.
if ($liveIdentityParts.Count -ne 5 -or $liveIdentityParts[0] -ne $revision -or $liveIdentityParts[2] -ne $revision -or $liveIdentityParts[4] -ne $revision) {
    throw "served SHA does not match the immutable deployment revision"
}
if ($liveIdentityParts[1] -notmatch '^sha256:[0-9a-f]{64}$') { throw "live image digest is malformed" }
if ($liveIdentityParts[3] -ne $expectedMigration) { throw "latest applied migration does not match the checked-out server" }

$invariantsAfter = ""
$afterValues = @()
for ($attempt = 1; $attempt -le 90; $attempt++) {
    $invariantsAfter = (Invoke-RemoteScript $collectInvariantScript | Select-Object -First 1).Trim()
    if ($invariantsAfter -notmatch '^\d+\|\d+\|\d+\|\d+\|\d+\|\d+\|\d+\|[01]$') { throw "malformed post-deploy invariants" }
    $afterValues = @($invariantsAfter -split '\|' | ForEach-Object { [int64]$_ })
    if ($afterValues[7] -eq 1) { break }
    if ($attempt -lt 90) { Start-Sleep -Seconds 2 }
}
if ($afterValues[7] -ne 1) { throw "the new server did not complete a fresh full builder pass" }
$sourceInvariantIndexes = @(0, 1, 2, 4, 5)
foreach ($i in $sourceInvariantIndexes) {
    if ($afterValues[$i] -lt $beforeValues[$i]) {
        throw "a production PASS/FAIL/sample source invariant decreased"
    }
}
if ($afterValues[1] -gt 0 -and $afterValues[3] -le 0) {
    throw "failure-cluster materialization disappeared while FAIL evidence remains"
}
if ($afterValues[6] -ne 0) { throw "post-deploy failure-cluster ledger is internally inconsistent" }
$failureClusterObservationDelta = $afterValues[3] - $beforeValues[3]
Write-Output "deployed SHA: $($liveIdentityParts[0])"
Write-Output "served /version SHA: $($liveIdentityParts[4])"
Write-Output "image digest: $($liveIdentityParts[1])"
Write-Output "migration version: $($liveIdentityParts[3])"
Write-Output "deployment invariants after: $invariantsAfter"
Write-Output "failure-cluster observation delta: $failureClusterObservationDelta"

$failureEvidenceBalance = (Invoke-RemoteScript @'
set -eu
cd /opt/codesamplex/deploy
has_quality=$(docker compose exec -T db psql -U csx -d csx -Atqc "SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='evidence_agg' AND column_name='evidence_quality')")
if [ "$has_quality" != t ]; then
  printf 'unavailable\n'
  exit 0
fi
docker compose exec -T db psql -U csx -d csx -At -F '|' -c "
SELECT
  COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0),
  COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality IN ('complete','partial','missing','legacy-evidence-incomplete')),0),
  COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality NOT IN ('complete','partial','missing','legacy-evidence-incomplete')),0)
FROM evidence_agg"
'@ | Select-Object -First 1).Trim()
if ($failureEvidenceBalance -ne "unavailable") {
    if ($failureEvidenceBalance -notmatch '^\d+\|\d+\|\d+$') { throw "malformed failure evidence quality invariant" }
    $qualityValues = @($failureEvidenceBalance -split '\|' | ForEach-Object { [int64]$_ })
    if ($qualityValues[0] -ne $qualityValues[1] -or $qualityValues[2] -ne 0) {
        throw "complete + partial + missing + legacy-evidence-incomplete does not equal FAIL"
    }
}
Write-Output "failure evidence quality balance: $failureEvidenceBalance"

$landing = Invoke-Remote "cd /opt/codesamplex/deploy && docker compose exec -T server wget -qO- http://127.0.0.1:8080/ | head -c 300"
Write-Output "landing sample: $($landing -join ' ' )"

# The former logger can contain historical queries. Purge only after the new
# server and Caddy pass their live smokes, but before committing either side of
# the coordinated remote/DPAPI transaction.
$legacyAccessPurge = @'
set -eu
cd /opt/codesamplex/deploy
removed=$(docker compose exec -T caddy sh -s <<'CSX_LEGACY_ACCESS_PURGE'
  set -eu
  old_dir=/var/log/caddy
  test "$(readlink -f "$old_dir")" = /var/log/caddy
  count=0
  for old in "$old_dir"/access.log "$old_dir"/access-*.log "$old_dir"/access-*.log.gz; do
    [ -e "$old" ] || continue
    [ ! -L "$old" ] || exit 66
    [ -f "$old" ] || exit 67
    base=${old##*/}
    case "$base" in
      access.log|access-*.log|access-*.log.gz) ;;
      *) exit 68 ;;
    esac
    rm -f -- "$old"
    count=$((count + 1))
  done
  printf "%s" "$count"
CSX_LEGACY_ACCESS_PURGE
)
printf 'legacy query-bearing access files irrecoverably removed: %s\n' "$removed"
'@
if ($RequireNoLegacyAccessLogs) {
    Invoke-RemoteScript $assertNoLegacyAccessLogs | Out-Null
    Write-Output "automatic deploy performed no irreversible legacy-log cleanup"
} else {
    Invoke-RemoteScript $legacyAccessPurge | ForEach-Object { Write-Output $_ }
}

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
docker compose exec -T server wget -qO- http://127.0.0.1:8080/healthz | grep -q '^ok'
'@
Invoke-RemoteScript $commitDeployment | Out-Null

if ($ConfigureAdmin -and $adminCredentialPending) {
    Commit-CSXAdminCredential $adminCredentialPaths.Pending $adminCredentialPaths.Active
    Write-Output "local admin credential committed after final remote deployment commit"
}
    $serverActivationStarted = $false
    $caddyPromoted = $false
} catch {
    $deployFailure = $_
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
if [ "$restore_dist" -eq 1 ]; then test -d /opt/codesamplex/dist.previous; fi
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
  mv /opt/codesamplex/dist /opt/codesamplex/dist.failed-rollback
  if mv /opt/codesamplex/dist.rollback-stage /opt/codesamplex/dist; then
    rm -rf /opt/codesamplex/dist.failed-rollback
  else
    mv /opt/codesamplex/dist.failed-rollback /opt/codesamplex/dist
    exit 68
  fi
fi
if [ -f server-container.rollback-present ]; then
  docker tag codesamplex/csx-server:rollback-predeploy codesamplex/csx-server:latest
  docker compose up -d --no-build --no-deps --force-recreate server
  test "$(docker inspect codesamplex-server-1 --format '{{.Image}}')" = "$old"
  if [ -f server-container.rollback-running ]; then
    i=0
    while [ "$i" -lt 24 ]; do
      if docker compose exec -T server wget -qO- http://127.0.0.1:8080/healthz 2>/dev/null | grep -q '^ok'; then break; fi
      i=$((i + 1))
      sleep 5
    done
    test "$i" -lt 24
    test "$(docker inspect codesamplex-server-1 --format '{{.State.Running}}')" = true
  else
    docker compose stop server >/dev/null
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
        Invoke-RemoteScript $rollbackServer | Out-Null
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
  docker compose up -d --no-build --no-deps --force-recreate caddy
  test "$(docker inspect codesamplex-caddy-1 --format '{{.Image}}')" = "$old"
  if [ -f "$container_running" ]; then
    docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
    test "$(docker inspect codesamplex-caddy-1 --format '{{.State.Running}}')" = true
  else
    docker compose stop caddy >/dev/null
    test "$(docker inspect codesamplex-caddy-1 --format '{{.State.Running}}')" = false
  fi
else
  ! docker container inspect codesamplex-caddy-1 >/dev/null 2>&1
fi
if [ -f "$rollback" ]; then cmp -s "$rollback" "$live"; else test ! -e "$live"; fi
test ! -e "$candidate"
'@
        try {
            Invoke-RemoteScript $rollbackCaddy | Out-Null
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
        throw [AggregateException]::new("deployment failed and one or more exact rollbacks failed", $allFailures.ToArray())
    }
    throw $deployFailure
}
Write-Output ""
Write-Output "Deployed. http://$Ip is live; https://$Domain follows DNS propagation."
} catch {
    $deployScriptFailure = $_
    throw
} finally {
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
        $cleanupEAP = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        try { & docker image rm $localImageTag 2>&1 | Out-Null }
        finally { $ErrorActionPreference = $cleanupEAP }
    }
    foreach ($temporaryFile in @($tagTmp, $envTmp)) {
        if ($null -ne $temporaryFile -and (Test-Path -LiteralPath $temporaryFile -PathType Leaf)) {
            try { Remove-Item -LiteralPath $temporaryFile -Force }
            catch { Write-Warning "could not remove a per-deploy local temporary file" }
        }
    }
    if ($null -ne $remoteImageTar -and $remoteImageTar -eq "/opt/codesamplex/csx-server-image-$deployLockOwner.tar") {
        try { Invoke-Remote "rm -f $remoteImageTar; docker image rm $localImageTag >/dev/null 2>&1 || true" | Out-Null }
        catch { Write-Warning "could not remove per-deploy remote image artifacts" }
    }
    if ($deployLockHeld) {
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
            Invoke-RemoteScript $releaseDeployLock | Out-Null
            $deployLockHeld = $false
        } catch {
            if ($null -ne $deployScriptFailure) {
                Write-Warning "deploy failed and its exact remote lock could not be released; original failure is preserved and an operator must inspect the lock owner"
            } else {
                throw
            }
        }
    }
}
