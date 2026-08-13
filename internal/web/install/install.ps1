# CodeSampleX installer for Windows.
# Usage:  irm https://codesamplex.dev/install.ps1 | iex
# Downloads the single csx binary, adds it to your user PATH, then runs
# `csx init` (one question, everything else automatic).

$ErrorActionPreference = 'Stop'

$base = '__CSX_BASE_URL__'

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'ARM64' { 'arm64' }
    default { 'amd64' }
}

$dir = Join-Path $env:LOCALAPPDATA 'csx'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir 'csx.exe'

Write-Host "Downloading csx (windows/$arch) from $base ..."
Invoke-WebRequest -UseBasicParsing -Uri "$base/dl/csx-windows-$arch.exe" -OutFile $exe

# Add the install dir to the user PATH once.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($null -eq $userPath) { $userPath = '' }
if (($userPath -split ';') -notcontains $dir) {
    $newPath = if ($userPath) { "$userPath;$dir" } else { $dir }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Write-Host "Added $dir to your user PATH (new terminals pick it up automatically)."
}
# Make csx available in this session too.
if (($env:Path -split ';') -notcontains $dir) {
    $env:Path = "$env:Path;$dir"
}

Write-Host 'csx installed. Starting setup...'
& $exe init
