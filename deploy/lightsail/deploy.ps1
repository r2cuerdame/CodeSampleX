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

$imageTar = Join-Path $env:TEMP "csx-server-image.tar"
if (-not $SkipImage) {
    Write-Output "== building linux/amd64 server image =="
    & docker build --platform linux/amd64 -f (Join-Path $repo "deploy\Dockerfile.server") -t codesamplex/csx-server:latest $repo
    if ($LASTEXITCODE -ne 0) { throw "docker build failed" }
    # No gzip on Windows PowerShell; ship the plain tar (ssh compresses).
    & docker save codesamplex/csx-server:latest -o $imageTar
    if ($LASTEXITCODE -ne 0) { throw "docker save failed" }
}

Write-Output "== shipping bundle to $Ip =="
Invoke-Remote "mkdir -p /opt/codesamplex/deploy/caddy /opt/codesamplex/dist /opt/codesamplex/schemas/v1" | Out-Null
Copy-Remote (Join-Path $repo "deploy\docker-compose.yml") "/opt/codesamplex/deploy/docker-compose.yml"
Copy-Remote (Join-Path $repo "deploy\caddy\Caddyfile") "/opt/codesamplex/deploy/caddy/Caddyfile"
Copy-Remote (Join-Path $repo "deploy\backup.sh") "/opt/codesamplex/deploy/backup.sh"
Copy-Remote (Join-Path $repo "schemas\v1\adapters.json") "/opt/codesamplex/schemas/v1/adapters.json"

$dist = Join-Path $repo "dist"
if (Test-Path $dist) {
    Get-ChildItem $dist -File | ForEach-Object { Copy-Remote $_.FullName "/opt/codesamplex/dist/$($_.Name)" }
    Write-Output "shipped $((Get-ChildItem $dist -File).Count) release artifacts"
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
