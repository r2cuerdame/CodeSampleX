# Cross-compile release artifacts into dist/.
# csx: windows/linux/darwin x amd64/arm64 (single-binary client, goal.md §18)
# csx-server: linux/amd64 (container host)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$version = (git describe --tags --always 2>$null); if (-not $version) { $version = "dev" }
$ldflags = "-s -w -X github.com/r2cuerdame/codesamplex/internal/cli.Version=$version"

New-Item -ItemType Directory -Force dist | Out-Null
$targets = @(
    @{os = "windows"; arch = "amd64"; out = "csx-windows-amd64.exe" },
    @{os = "windows"; arch = "arm64"; out = "csx-windows-arm64.exe" },
    @{os = "linux";   arch = "amd64"; out = "csx-linux-amd64" },
    @{os = "linux";   arch = "arm64"; out = "csx-linux-arm64" },
    @{os = "darwin";  arch = "amd64"; out = "csx-darwin-amd64" },
    @{os = "darwin";  arch = "arm64"; out = "csx-darwin-arm64" }
)
foreach ($t in $targets) {
    Write-Output "building csx $($t.os)/$($t.arch)..."
    $env:GOOS = $t.os; $env:GOARCH = $t.arch; $env:CGO_ENABLED = "0"
    go build -trimpath -ldflags $ldflags -o "dist/$($t.out)" ./cmd/csx
    if (-not $?) { throw "build failed for $($t.os)/$($t.arch)" }
}
Write-Output "building csx-server linux/amd64..."
$env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -trimpath -ldflags $ldflags -o "dist/csx-server-linux-amd64" ./cmd/csx-server
if (-not $?) { throw "server build failed" }

Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue

$hashes = Get-ChildItem dist | Where-Object { $_.Name -ne "SHA256SUMS.txt" } | ForEach-Object {
    "{0}  {1}" -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower(), $_.Name
}
Set-Content -Path dist/SHA256SUMS.txt -Value ($hashes -join "`n") -Encoding ascii
Write-Output "done:"; Get-ChildItem dist | Format-Table Name, Length -AutoSize
