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

# Clean up a previous upgrade's displaced binary, now that nothing holds it.
Get-ChildItem -Path $dir -Filter 'csx.exe.old-*' -ErrorAction SilentlyContinue |
    ForEach-Object { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue }

Write-Host "Downloading csx (windows/$arch) from $base ..."
$staged = "$exe.new"
Invoke-WebRequest -UseBasicParsing -Uri "$base/dl/csx-windows-$arch.exe" -OutFile $staged

# Windows will not let anything WRITE a running executable, and after
# `csx init` the MCP server is exactly that: a long-running process your
# editor started, holding csx.exe open. Downloading straight over the top
# therefore failed on every upgrade an existing user attempted — the one
# case that matters — with an IO error that says nothing about why.
#
# Windows does allow a running executable to be RENAMED. So the old binary
# is moved aside and the new one takes its place; the running server keeps
# the file it already opened, and the displaced copy is deleted on the next
# install, once nothing holds it.
$replaced = $false
if (Test-Path $exe) {
    $aside = Join-Path $dir ("csx.exe.old-" + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    try {
        Move-Item -Path $exe -Destination $aside -Force
        $replaced = $true
    } catch {
        Remove-Item $staged -Force -ErrorAction SilentlyContinue
        throw "csx.exe is in use and could not be replaced. Close your editor (it runs the csx MCP server) and run the installer again."
    }
}
Move-Item -Path $staged -Destination $exe -Force

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

# A replaced binary means an MCP server may still be running the old one.
# It keeps answering with the old code until the editor restarts it, and
# nothing else in the flow would ever mention that.
if ($replaced) {
    Write-Host ''
    Write-Host 'You upgraded an existing install. If your editor is open, restart it:'
    Write-Host 'the csx MCP server it started is still running the previous build.'
}
