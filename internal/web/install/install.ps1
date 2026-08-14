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

# Add the install dir to the user PATH once, without changing anything else
# about it.
#
# [Environment]::SetEnvironmentVariable writes REG_SZ. A user PATH is
# normally REG_EXPAND_SZ, so that one call silently converts it and every
# %USERPROFILE%-style entry already in it stops expanding — permanently,
# system-wide, for a reason nobody would ever trace back to installing csx.
# Writing through the registry lets the existing value kind be preserved.
$envKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
try {
    $userPath = ''
    $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
    if ($null -ne $envKey) {
        # DoNotExpandEnvironmentNames: read it back as written, so an
        # existing %VAR% is not baked into a literal on the way through.
        $userPath = [string]$envKey.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        try { $kind = $envKey.GetValueKind('Path') } catch { }
    }

    # Compare normalized: a trailing backslash or a quoted entry is the same
    # directory, and treating it as a different one appended a duplicate.
    $normalized = $userPath -split ';' | ForEach-Object { $_.Trim().Trim('"').TrimEnd('') }
    $want = $dir.Trim().Trim('"').TrimEnd('')
    if ($normalized -notcontains $want) {
        $newPath = if ($userPath) { "$userPath;$dir" } else { $dir }
        if ($null -ne $envKey) {
            $envKey.SetValue('Path', $newPath, $kind)
        } else {
            [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        }
        Write-Host "Added $dir to your user PATH (new terminals pick it up automatically)."
    }
} finally {
    if ($null -ne $envKey) { $envKey.Close() }
}
# Make csx available in this session too.
if (($env:Path -split ';') -notcontains $dir) {
    $env:Path = "$env:Path;$dir"
}

Write-Host 'csx installed. Starting setup...'
& $exe init
