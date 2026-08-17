# Deploy the CodeSampleX stack to the Lightsail host.
# The 2GB host never builds: the linux/amd64 image is built here and shipped.
# Usage: .\deploy.ps1 -Ip <staticIp> -KeyPath <pem> [-Domain codesamplex.dev]
param(
    [Parameter(Mandatory)][string]$Ip,
    [Parameter(Mandatory)][string]$KeyPath,
    [string]$Domain = "codesamplex.dev",
    [string]$User = "ubuntu",
    [switch]$SkipImage,
    [switch]$ConfigureAdmin,
    [switch]$RotateAdmin
)
$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$remote = "${User}@${Ip}"
$sshArgs = @("-i", $KeyPath, "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=20")
. (Join-Path $PSScriptRoot "admin-credential.ps1")

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
        $out = & ssh @sshArgs $remote $Script 2>&1 | ForEach-Object { "$_" }
    } finally { $ErrorActionPreference = $prev }
    if ($LASTEXITCODE -ne 0) { throw "remote command failed ($LASTEXITCODE): $Script`n$($out -join "`n")" }
    return $out
}
function Invoke-RemoteScript([string]$Script) {
    if ([string]::IsNullOrWhiteSpace($Script)) { throw "refusing an empty remote script" }
    if ($KeyPath.Contains('"')) { throw "SSH key path contains an unsupported quote" }
    if ($remote -notmatch '^[A-Za-z0-9._-]+@[A-Za-z0-9.:-]+$') { throw "unsafe SSH destination" }

    # Windows OpenSSH does not preserve nested shell quotes when an entire
    # multiline program is passed as one argv element. Send programs on stdin
    # to a fixed `sh -s` command so regex pipes, quotes and redirects arrive
    # byte-for-byte. Secrets use Invoke-RemoteInput instead.
    $scriptBytes = (New-Object Text.UTF8Encoding($false)).GetBytes($Script)
    $psi = New-Object Diagnostics.ProcessStartInfo
    $psi.FileName = (Get-Command ssh.exe -ErrorAction Stop).Source
    $psi.Arguments = '-i "' + $KeyPath + '" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 ' + $remote + ' "sh -s"'
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
    if ($KeyPath.Contains('"')) { throw "SSH key path contains an unsupported quote" }
    if ($remote -notmatch '^[A-Za-z0-9._-]+@[A-Za-z0-9.:-]+$') { throw "unsafe SSH destination" }

    # Windows PowerShell 5 may prepend a UTF-8 BOM while newer hosts may not.
    # The fixed remote command prefixes `#` to a disposable first line, making
    # that line a comment in either case. The verifier remains on stdin only;
    # ProcessStartInfo.Arguments is static.
    $payload = "CSX-STDIN-V1`nhash='$StdinText'`n$Script`n"
    $payloadBytes = (New-Object Text.UTF8Encoding($false)).GetBytes($payload)
    $psi = New-Object Diagnostics.ProcessStartInfo
    $psi.FileName = (Get-Command ssh.exe -ErrorAction Stop).Source
    $psi.Arguments = '-i "' + $KeyPath + '" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 ' + $remote + ' "{ printf ''#''; cat; } | sh"'
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
        & scp -i $KeyPath -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 $Local "${remote}:${RemotePath}" 2>&1 | Out-Null
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
try {
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

$requiredReleaseAssets = @(
    "csx-darwin-amd64",
    "csx-darwin-arm64",
    "csx-linux-amd64",
    "csx-linux-arm64",
    "csx-server-linux-amd64",
    "csx-windows-amd64.exe",
    "csx-windows-arm64.exe",
    "SHA256SUMS.txt",
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
    foreach ($name in $requiredReleaseAssets[0..6]) {
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
$imageTar = Join-Path $env:TEMP "csx-server-image-$deployLockOwner.tar"
if (-not $SkipImage) {
	$localImageCleanupNeeded = $true
    Write-Output "== building linux/amd64 server image =="
    $dockerfile = Join-Path $repo "deploy\Dockerfile.server"
    $revision = (& git -C $repo rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $revision -notmatch '^[0-9a-f]{40}$') { throw "could not determine the server revision" }
    Invoke-Native "docker build" {
        & docker build --platform linux/amd64 --build-arg "CSX_VERSION=$revision" -f $dockerfile -t $localImageTag $repo
    }
    # No gzip on Windows PowerShell; ship the plain tar (ssh compresses).
    Invoke-Native "docker save" { & docker save $localImageTag -o $imageTar }
}

Write-Output "== shipping bundle to $Ip =="
Invoke-Remote "mkdir -p /opt/codesamplex/deploy/caddy /opt/codesamplex/dist /opt/codesamplex/schemas/v1 /opt/codesamplex/backups && sudo chown ${User}:${User} /opt/codesamplex/backups && (sudo chown ${User}:${User} /opt/codesamplex/deploy/backup.sh /opt/codesamplex/deploy/restore-check.sh 2>/dev/null || true)" | Out-Null
Copy-Remote (Join-Path $repo "deploy\docker-compose.yml") "/opt/codesamplex/deploy/docker-compose.yml"
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
docker exec "$name" sh -c '
  test "$(stat -c %a /var/log/caddy-safe/access-safe.log)" = 644
  grep -q '"csx_route":"samples"' /var/log/caddy-safe/access-safe.log
  grep -q '"csx_method":"get_head"' /var/log/caddy-safe/access-safe.log
  ! grep -Eq 'known-id-must-not-log|query-must-not-log|encoded-id-must-not-log|unknown-secret-must-not-log|remote_ip|client_ip|headers|user_id|"request"' /var/log/caddy-safe/access-safe.log
  ! grep -q '?' /var/log/caddy-safe/access-safe.log
'
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
    $tagTmp = Join-Path $env:TEMP "csx-release-tag.txt"
    Set-Content -Path $tagTmp -Value $tag -Encoding ascii -NoNewline
    Copy-Remote $tagTmp "$stage/.release-tag"
    $stageFiles = ($requiredReleaseAssets | ForEach-Object { "test -f $stage/$_" }) -join " && "
    $stageValidation = ('set -eu; {0}; cd /opt/codesamplex/dist.stage; sha256sum -c SHA256SUMS.txt >/dev/null; sha256sum -c codesamplex-mcp.mcpb.sha256 >/dev/null; test "$(find . -maxdepth 1 -type f ! -name .release-tag | wc -l)" -eq {1}' -f $stageFiles, $requiredReleaseAssets.Count)
    Invoke-Remote $stageValidation | Out-Null
    Invoke-Remote "set -eu; rm -rf /opt/codesamplex/dist.previous; mv /opt/codesamplex/dist /opt/codesamplex/dist.previous; if mv /opt/codesamplex/dist.stage /opt/codesamplex/dist; then :; else mv /opt/codesamplex/dist.previous /opt/codesamplex/dist; exit 1; fi" | Out-Null
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
$envTmp = Join-Path $env:TEMP "csx.env"
Set-Content -Path $envTmp -Value ($envText -replace "`r`n", "`n") -Encoding ascii -NoNewline
Copy-Remote $envTmp "/opt/codesamplex/deploy/.env.new"
Invoke-Remote "cd /opt/codesamplex/deploy && chmod 600 .env.new && if [ -f .env ]; then rm -f .env.new; chmod 600 .env; echo 'kept existing .env'; else mv .env.new .env; echo 'wrote new .env'; fi" | Out-Null

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
    $oldImage = Invoke-Remote "docker image inspect codesamplex/csx-server:latest --format '{{.Id}}' 2>/dev/null || true" | Select-Object -First 1
    if ($oldImage) {
        Invoke-Remote "docker tag codesamplex/csx-server:latest codesamplex/csx-server:rollback-predeploy" | Out-Null
        Write-Output "rollback-predeploy: $(([string]$oldImage).Trim())"
    }
    $remoteImageTar = "/opt/codesamplex/csx-server-image-$deployLockOwner.tar"
    Copy-Remote $imageTar $remoteImageTar
    Invoke-Remote "set -eu; docker load -i $remoteImageTar >/dev/null; docker tag $localImageTag codesamplex/csx-server:latest; docker image rm $localImageTag >/dev/null; rm -f $remoteImageTar" | Out-Null
}

$caddyPromoted = $false
try {
    # Promote only after every unrelated shipping/build step has succeeded.
    # Keep one exact rollback copy until the live privacy smoke passes.
    $promoteCaddy = @'
set -eu
candidate=/opt/codesamplex/deploy/caddy/Caddyfile.candidate
live=/opt/codesamplex/deploy/caddy/Caddyfile
rollback=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-predeploy
absent=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-absent
promoted=0
cleanup() {
  if [ "$promoted" -eq 0 ]; then rm -f "$rollback" "$absent"; fi
}
trap cleanup EXIT HUP INT TERM
test -f "$candidate"
rm -f "$rollback" "$absent"
if [ -f "$live" ]; then
  cp -p "$live" "$rollback"
else
  : > "$absent"
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
    $health = & ssh @sshArgs $remote "cd /opt/codesamplex/deploy && docker compose exec -T server wget -qO- http://127.0.0.1:8080/healthz 2>/dev/null || true" 2>&1 | ForEach-Object { "$_" }
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
docker compose exec -T caddy sh -c '
  umask 022
  marker=/var/log/caddy-safe/access-safe.log.since
  if [ ! -f "$marker" ]; then
    tmp=$(mktemp /var/log/caddy-safe/.access-safe.since.XXXXXX)
    date -u +%Y-%m-%dT%H:%M:%SZ > "$tmp"
    chmod 0644 "$tmp"
    mv "$tmp" "$marker"
  fi
'
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
docker compose exec -T caddy sh -c '
  test -f /var/log/caddy-safe/access-safe.log
  test "$(stat -c %a /var/log/caddy-safe/access-safe.log)" = 644
  test "$(stat -c %a /var/log/caddy-safe/access-safe.log.since)" = 644
  ! grep -q "discard-this-query" /var/log/caddy-safe/access-safe.log
  ! grep -q "encoded-marker-must-not-log" /var/log/caddy-safe/access-safe.log
  ! grep -q "secret-marker-must-not-log" /var/log/caddy-safe/access-safe.log
  ! grep -q '?' /var/log/caddy-safe/access-safe.log
  grep -q '"csx_method":"get_head"' /var/log/caddy-safe/access-safe.log
  ! grep -Eq 'remote_ip|client_ip|headers|user_id|"request"' /var/log/caddy-safe/access-safe.log
'
docker compose exec -T server sh -c '
  test -r /var/log/caddy-safe/access-safe.log
  test -r /var/log/caddy-safe/access-safe.log.since
  test ! -e /var/log/caddy/access.log
'
'@
$safeAccessLogSmoke = $safeAccessLogSmoke.Replace('__CSX_DOMAIN__', $Domain)
Invoke-RemoteScript $safeAccessLogSmoke | ForEach-Object { Write-Output $_ }
    Invoke-Remote "rm -f /opt/codesamplex/deploy/caddy/Caddyfile.rollback-predeploy /opt/codesamplex/deploy/caddy/Caddyfile.rollback-absent /opt/codesamplex/deploy/caddy/Caddyfile.candidate" | Out-Null
    $caddyPromoted = $false
} catch {
    $deployFailure = $_
    if ($caddyPromoted) {
        $rollbackCaddy = @'
set -eu
cd /opt/codesamplex/deploy
live=/opt/codesamplex/deploy/caddy/Caddyfile
rollback=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-predeploy
absent=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-absent
candidate=/opt/codesamplex/deploy/caddy/Caddyfile.candidate
if [ -f "$rollback" ]; then
  chmod 0644 "$rollback"
  mv -f "$rollback" "$live"
  rm -f "$absent" "$candidate"
  docker compose up -d --no-build --no-deps --force-recreate caddy
  docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
elif [ -f "$absent" ]; then
  rm -f "$live" "$absent" "$candidate"
  docker compose rm -sf caddy >/dev/null 2>&1 || true
else
  exit 69
fi
'@
        try {
            Invoke-RemoteScript $rollbackCaddy | Out-Null
            Write-Output "Caddy config: restored rollback-predeploy after failed activation"
        } catch {
            Write-Warning "Caddy activation failed and automatic rollback also failed; rollback-predeploy needs operator attention"
        }
    }
    throw $deployFailure
}
Write-Output "privacy-safe API access log: query-free, bounded, server-readable"

# The former logger is now inactive and its files can contain historical
# queries. Irrecoverably remove only its exact current/roll filename family,
# only after the replacement config is committed and rollback is no longer
# needed. An interrupted cleanup is safe to retry on the next deploy.
$legacyAccessPurge = @'
set -eu
cd /opt/codesamplex/deploy
removed=$(docker compose exec -T caddy sh -c '
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
')
printf 'legacy query-bearing access files irrecoverably removed: %s\n' "$removed"
'@
Invoke-RemoteScript $legacyAccessPurge | ForEach-Object { Write-Output $_ }
if ($ConfigureAdmin) {
    # An unauthenticated 401 proves that the exact private route was mounted;
    # a missing or malformed verifier deliberately leaves it at 404.
    $adminProbe = @'
cd /opt/codesamplex/deploy
response=$(docker compose exec -T server wget -S -O /dev/null http://127.0.0.1:8080/admin 2>&1 || true)
# Windows PowerShell 5.1's native SSH argument marshalling can strip the
# quotes around the captured response, leaving each whitespace-delimited
# token on its own line. Match the status token itself so both renderings are
# accepted; this response is from the fixed loopback /admin endpoint only.
printf '%s\n' "$response" | grep -q '401'
'@
    Invoke-RemoteScript $adminProbe | Out-Null
    Write-Output "admin route: configured (401 without credentials)"

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
    if ($adminCredentialPending) {
        Commit-CSXAdminCredential $adminCredentialPaths.Pending $adminCredentialPaths.Active
        Write-Output "local admin credential committed to the current user's DPAPI store"
    }
}
$landing = Invoke-Remote "cd /opt/codesamplex/deploy && docker compose exec -T server wget -qO- http://127.0.0.1:8080/ | head -c 300"
Write-Output "landing sample: $($landing -join ' ' )"
Write-Output ""
Write-Output "Deployed. http://$Ip is live; https://$Domain follows DNS propagation."
} catch {
    $deployScriptFailure = $_
    throw
} finally {
    # Clean only per-invocation artifacts whose names contain this lock
    # owner's validated random token. Cleanup errors are warnings; lock
    # release below remains mandatory and gets its own error handling.
    $expectedImageTar = Join-Path $env:TEMP "csx-server-image-$deployLockOwner.tar"
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
