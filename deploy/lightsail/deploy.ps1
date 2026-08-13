# Deploy the CodeSampleX stack to the Lightsail host.
# Builds the linux/amd64 server image locally, ships it with the compose
# bundle over SSH, and starts the stack (2GB host never builds images itself).
# Usage: .\deploy.ps1 -Ip <staticIp> -KeyPath <pem> [-Domain codesamplex.dev]
param(
    [Parameter(Mandatory)][string]$Ip,
    [Parameter(Mandatory)][string]$KeyPath,
    [string]$Domain = "codesamplex.dev",
    [string]$User = "ubuntu"
)
$ErrorActionPreference = "Stop"
$repo = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$ssh = "ssh -i `"$KeyPath`" -o StrictHostKeyChecking=accept-new $User@$Ip"

Write-Output "Building linux/amd64 server image..."
docker build --platform linux/amd64 -f (Join-Path $repo "deploy\Dockerfile.server") -t codesamplex/csx-server:latest $repo
docker save codesamplex/csx-server:latest | gzip > "$env:TEMP\csx-server-image.tar.gz"

Write-Output "Shipping bundle to $Ip..."
Invoke-Expression "$ssh 'mkdir -p /opt/codesamplex/deploy/caddy /opt/codesamplex/deploy/lightsail'"
scp -i "$KeyPath" "$env:TEMP\csx-server-image.tar.gz" "${User}@${Ip}:/opt/codesamplex/"
scp -i "$KeyPath" (Join-Path $repo "deploy\docker-compose.yml") "${User}@${Ip}:/opt/codesamplex/deploy/"
scp -i "$KeyPath" (Join-Path $repo "deploy\caddy\Caddyfile") "${User}@${Ip}:/opt/codesamplex/deploy/caddy/"
scp -i "$KeyPath" (Join-Path $repo "deploy\backup.sh") "${User}@${Ip}:/opt/codesamplex/deploy/"

# Release binaries for /dl and the install scripts (optional but expected).
$dist = Join-Path $repo "dist"
if (Test-Path $dist) {
    Invoke-Expression "$ssh 'mkdir -p /opt/codesamplex/dist'"
    Get-ChildItem $dist -File | ForEach-Object {
        scp -i "$KeyPath" $_.FullName "${User}@${Ip}:/opt/codesamplex/dist/"
    }
}
# adapters.json is read from disk beside the server workdir (web /adapters page).
Invoke-Expression "$ssh 'mkdir -p /opt/codesamplex/schemas/v1'"
scp -i "$KeyPath" (Join-Path $repo "schemas\v1\adapters.json") "${User}@${Ip}:/opt/codesamplex/schemas/v1/"

$envFile = @"
CADDY_SITE=$Domain, www.$Domain
CSX_PUBLIC_URL=https://$Domain
CSX_DIST_HOST_DIR=/opt/codesamplex/dist
POSTGRES_PASSWORD=$( -join ((48..57)+(97..122) | Get-Random -Count 24 | ForEach-Object {[char]$_}) )
"@
$envPath = "$env:TEMP\csx.env"
# Never overwrite an existing server .env (it holds the DB password).
Set-Content -Path $envPath -Value $envFile -Encoding ascii
scp -i "$KeyPath" $envPath "${User}@${Ip}:/opt/codesamplex/deploy/.env.new"

Invoke-Expression "$ssh 'cd /opt/codesamplex/deploy && ([ -f .env ] || mv .env.new .env) && rm -f .env.new && gunzip -c ../csx-server-image.tar.gz | docker load && docker compose up -d --no-build && docker compose ps'"

Write-Output "Smoke test..."
Start-Sleep 10
$health = Invoke-Expression "$ssh 'curl -fsS http://127.0.0.1/healthz'"
Write-Output "healthz: $health"
Write-Output "Deployed. Point DNS A records for $Domain and www to $Ip."
