# Windows-side backup for a local CodeSampleX stack (development parity of
# deploy/backup.sh). Dumps PostgreSQL and archives blobs into .\backups\<date>\.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$stamp = (Get-Date).ToUniversalTime().ToString("yyyy-MM-dd")
$dest = Join-Path ".." "backups\$stamp"
New-Item -ItemType Directory -Force $dest | Out-Null

docker compose exec -T db pg_dump -U csx -d csx -Fc > (Join-Path $dest "csx.pgdump")

$destAbs = (Resolve-Path $dest).Path
docker run --rm -v codesamplex_blobs:/blobs:ro -v "${destAbs}:/backup" `
  alpine:3.22 tar czf /backup/blobs.tar.gz -C /blobs .

Get-ChildItem ..\backups -Directory | Where-Object { $_.CreationTimeUtc -lt (Get-Date).AddDays(-14) } | Remove-Item -Recurse -Force
Write-Output "backup complete: $dest"
