<#
.SYNOPSIS
    Generates and validates WinGet manifest files for CodeSampleX releases.
.DESCRIPTION
    Creates the version, installer, and defaultLocale manifests according to
    the official Microsoft WinGet schema (1.9.0) using immutable GitHub release
    evidence and SHA256SUMS.txt.
.PARAMETER Version
    The release version without the 'v' prefix (e.g. "0.1.134").
.PARAMETER ReleaseDate
    The release date in YYYY-MM-DD format. If omitted, defaults to today.
.PARAMETER OutputDir
    Destination directory for generated manifests.
.PARAMETER Validate
    If specified, runs 'winget validate --manifest' against the output directory.
#>
param(
    [Parameter(Mandatory = $true)]
    [string]$Version,

    [string]$ReleaseDate = (Get-Date -Format 'yyyy-MM-dd'),

    [string]$OutputDir,

    [switch]$Validate
)

$ErrorActionPreference = 'Stop'

if ($Version -match '^v') {
    $Version = $Version.Substring(1)
}

$PackageIdentifier = "r2cuerdame.CodeSampleX"
$ManifestVersion = "1.9.0"
$ReleaseBase = "https://github.com/r2cuerdame/CodeSampleX/releases/download/v$Version"

if (-not $OutputDir) {
    $OutputDir = Join-Path $PSScriptRoot "..\packaging\winget\manifests\r\r2cuerdame\CodeSampleX\$Version"
}

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

Write-Host "Fetching SHA256SUMS.txt from $ReleaseBase/SHA256SUMS.txt ..."
$checksumsUrl = "$ReleaseBase/SHA256SUMS.txt"
$rawContent = (Invoke-WebRequest -UseBasicParsing -Uri $checksumsUrl).Content
if ($rawContent -is [byte[]]) {
    $checksums = [Text.Encoding]::UTF8.GetString($rawContent)
} else {
    $checksums = [string]$rawContent
}

$x64Line = $checksums -split "`r?`n" | Where-Object { $_ -match "\s\*?csx-windows-amd64\.exe$" } | Select-Object -First 1
$arm64Line = $checksums -split "`r?`n" | Where-Object { $_ -match "\s\*?csx-windows-arm64\.exe$" } | Select-Object -First 1

if (-not $x64Line -or -not $arm64Line) {
    throw "SHA256SUMS.txt does not contain entries for both amd64 and arm64 Windows binaries."
}

$x64Sha256 = (($x64Line -split '\s+')[0]).ToUpperInvariant()
$arm64Sha256 = (($arm64Line -split '\s+')[0]).ToUpperInvariant()

# 1. Version manifest
$versionYaml = @"
# yaml-language-server: `$schema=https://aka.ms/winget-manifest.version.$ManifestVersion.schema.json

PackageIdentifier: $PackageIdentifier
PackageVersion: $Version
DefaultLocale: en-US
ManifestType: version
ManifestVersion: $ManifestVersion
"@

# 2. Installer manifest
$installerYaml = @"
# yaml-language-server: `$schema=https://aka.ms/winget-manifest.installer.$ManifestVersion.schema.json

PackageIdentifier: $PackageIdentifier
PackageVersion: $Version
InstallerType: portable
Commands:
  - csx
ReleaseDate: $ReleaseDate
Installers:
  - Architecture: x64
    InstallerUrl: $ReleaseBase/csx-windows-amd64.exe
    InstallerSha256: $x64Sha256
  - Architecture: arm64
    InstallerUrl: $ReleaseBase/csx-windows-arm64.exe
    InstallerSha256: $arm64Sha256
ManifestType: installer
ManifestVersion: $ManifestVersion
"@

# 3. Locale manifest
$localeYaml = @"
# yaml-language-server: `$schema=https://aka.ms/winget-manifest.defaultLocale.$ManifestVersion.schema.json

PackageIdentifier: $PackageIdentifier
PackageVersion: $Version
PackageLocale: en-US
Publisher: r2cuerdame
PublisherUrl: https://github.com/r2cuerdame
PublisherSupportUrl: https://github.com/r2cuerdame/CodeSampleX/issues
PackageName: CodeSampleX
PackageUrl: https://codesamplex.dev
License: Apache-2.0
LicenseUrl: https://github.com/r2cuerdame/CodeSampleX/blob/main/LICENSE
ShortDescription: An open compatibility testing network for developer libraries and runtimes
Description: >-
  CodeSampleX is an open compatibility testing network for developer libraries, runtimes,
  and toolchains. It records what real builds actually did in recorded environments,
  answering whether an API runs on your version, OS, and runtime with real build evidence
  and verified contracts.
Moniker: csx
Tags:
  - cli
  - compatibility
  - developer-tools
  - go
  - mcp
  - testing
ReleaseNotes: >-
  v$Version release of CodeSampleX portable CLI tools and stdio MCP server.
ReleaseNotesUrl: https://github.com/r2cuerdame/CodeSampleX/releases/tag/v$Version
ManifestType: defaultLocale
ManifestVersion: $ManifestVersion
"@

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[IO.File]::WriteAllText((Join-Path $OutputDir "$PackageIdentifier.yaml"), ($versionYaml.TrimEnd() + "`n"), $utf8NoBom)
[IO.File]::WriteAllText((Join-Path $OutputDir "$PackageIdentifier.installer.yaml"), ($installerYaml.TrimEnd() + "`n"), $utf8NoBom)
[IO.File]::WriteAllText((Join-Path $OutputDir "$PackageIdentifier.locale.en-US.yaml"), ($localeYaml.TrimEnd() + "`n"), $utf8NoBom)

Write-Host "Generated manifests in $OutputDir"

if ($Validate) {
    Write-Host "Validating with winget validate ..."
    winget validate --manifest $OutputDir
    if ($LASTEXITCODE -ne 0) {
        throw "winget validate failed with exit code $LASTEXITCODE"
    }
    Write-Host "winget validation succeeded!"
}
