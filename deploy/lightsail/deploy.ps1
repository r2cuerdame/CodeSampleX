# Deploy the CodeSampleX stack to the Lightsail host.
# The 2GB host never builds: the linux/amd64 image is built here and shipped.
# Usage: .\deploy.ps1 -Ip <staticIp> -KeyPath <pem> [-Domain codesamplex.dev]
param(
    [Parameter(Mandatory)][string]$Ip,
    [Parameter(Mandatory)][string]$KeyPath,
    [string]$Domain = "codesamplex.dev",
    [string]$User = "ubuntu",
    [switch]$SkipImage
)
$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$remote = "${User}@${Ip}"
$sshArgs = @("-i", $KeyPath, "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=20")

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

$imageTar = Join-Path $env:TEMP "csx-server-image.tar"
if (-not $SkipImage) {
    Write-Output "== building linux/amd64 server image =="
    $dockerfile = Join-Path $repo "deploy\Dockerfile.server"
    Invoke-Native "docker build" {
        & docker build --platform linux/amd64 -f $dockerfile -t codesamplex/csx-server:latest $repo
    }
    # No gzip on Windows PowerShell; ship the plain tar (ssh compresses).
    Invoke-Native "docker save" { & docker save codesamplex/csx-server:latest -o $imageTar }
}

Write-Output "== shipping bundle to $Ip =="
Invoke-Remote "mkdir -p /opt/codesamplex/deploy/caddy /opt/codesamplex/dist /opt/codesamplex/schemas/v1" | Out-Null
Copy-Remote (Join-Path $repo "deploy\docker-compose.yml") "/opt/codesamplex/deploy/docker-compose.yml"
Copy-Remote (Join-Path $repo "deploy\caddy\Caddyfile") "/opt/codesamplex/deploy/caddy/Caddyfile"
Copy-Remote (Join-Path $repo "deploy\backup.sh") "/opt/codesamplex/deploy/backup.sh"
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
$dist = Join-Path $repo "dist"
New-Item -ItemType Directory -Force $dist | Out-Null
Write-Output "== fetching release artifacts =="
$tag = (& gh release view --repo r2cuerdame/CodeSampleX --json tagName --jq .tagName)
if ($LASTEXITCODE -ne 0 -or -not $tag) { throw "could not read the latest release tag" }
Get-ChildItem $dist -File | Remove-Item -Force
Invoke-Native "gh release download" {
    & gh release download $tag --repo r2cuerdame/CodeSampleX --dir $dist --clobber
}
$artifacts = Get-ChildItem $dist -File
if ($artifacts.Count -eq 0) { throw "release $tag produced no artifacts" }
# The checksums the release published, verified before anything is served.
$sums = Join-Path $dist "SHA256SUMS.txt"
if (Test-Path $sums) {
    foreach ($line in Get-Content $sums) {
        $parts = $line -split '\s+', 2
        if ($parts.Count -ne 2) { continue }
        $name = $parts[1].TrimStart('*').Trim()
        $file = Join-Path $dist $name
        if (-not (Test-Path $file)) { continue }
        $have = (Get-FileHash $file -Algorithm SHA256).Hash.ToLower()
        if ($have -ne $parts[0].ToLower()) { throw "checksum mismatch for $name" }
    }
    Write-Output "checksums verified against the release"
}
$artifacts | ForEach-Object { Copy-Remote $_.FullName "/opt/codesamplex/dist/$($_.Name)" }
Write-Output "shipped $($artifacts.Count) artifacts from $tag"

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
Invoke-Remote "cd /opt/codesamplex/deploy && if [ -f .env ]; then rm -f .env.new; echo 'kept existing .env'; else mv .env.new .env; echo 'wrote new .env'; fi" | Out-Null

if (-not $SkipImage) {
    Write-Output "== loading image on host (this takes a minute) =="
    Copy-Remote $imageTar "/opt/codesamplex/csx-server-image.tar"
    Invoke-Remote "docker load -i /opt/codesamplex/csx-server-image.tar && rm -f /opt/codesamplex/csx-server-image.tar" | Out-Null
}

Write-Output "== starting stack =="
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose up -d --no-build --remove-orphans" | Out-Null
Invoke-Remote "cd /opt/codesamplex/deploy && docker compose ps" | ForEach-Object { Write-Output $_ }

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
$landing = Invoke-Remote "cd /opt/codesamplex/deploy && docker compose exec -T server wget -qO- http://127.0.0.1:8080/ | head -c 300"
Write-Output "landing sample: $($landing -join ' ' )"
Write-Output ""
Write-Output "Deployed. http://$Ip is live; https://$Domain follows DNS propagation."
