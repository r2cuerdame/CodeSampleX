# CodeSampleX installer for Windows.
# Usage:  irm https://codesamplex.dev/install.ps1 | iex
# Downloads the single csx binary, adds it to your user PATH, then runs
# `csx init` (one question, everything else automatic).

$ErrorActionPreference = 'Stop'

$base = '__CSX_BASE_URL__'

function Write-Durable([string]$Path, [string]$Text) {
    $data = [Text.Encoding]::UTF8.GetBytes($Text)
    $file = [IO.File]::Open($Path, [IO.FileMode]::Create, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $file.Write($data, 0, $data.Length); $file.Flush($true) } finally { $file.Dispose() }
}

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'ARM64' { 'arm64' }
    default { 'amd64' }
}

$dir = Join-Path $env:LOCALAPPDATA 'csx'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir 'csx.exe'
$journal = Join-Path $dir 'migration.json'
if (Test-Path $journal) {
    try {
        $pending = Get-Content $journal -Raw | ConvertFrom-Json
		$backupPattern = '^' + [regex]::Escape($dir + [IO.Path]::DirectorySeparatorChar + 'csx.exe.legacy-') + '[0-9a-f]{8}$'
        if ($pending.schema -ne 1 -or $pending.phase -ne 'old-renamed' -or $pending.backup -notmatch $backupPattern) { throw 'invalid migration journal' }
        if (-not (Test-Path $exe) -and (Test-Path $pending.backup)) { Move-Item $pending.backup $exe -Force }
    } finally { Remove-Item $journal -Force -ErrorAction SilentlyContinue }
}

# Clean up a previous upgrade's displaced binary, now that nothing holds it.
Get-ChildItem -Path $dir -Filter 'csx.exe.old-*' -ErrorAction SilentlyContinue |
    ForEach-Object { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue }

Write-Host "Downloading csx payload and stable launcher (windows/$arch) from $base ..."
$staged = Join-Path $dir 'csx-payload.new.exe'
$launcherStaged = Join-Path $dir 'csx-launcher.new.exe'
$checksums = "$exe.checksums"
try {
    Invoke-WebRequest -UseBasicParsing -Uri "$base/dl/csx-windows-$arch.exe" -OutFile $staged
    Invoke-WebRequest -UseBasicParsing -Uri "$base/dl/csx-launcher-windows-$arch.exe" -OutFile $launcherStaged
    $flush = [IO.File]::Open($staged, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::Read)
    try { $flush.Flush($true) } finally { $flush.Dispose() }
    $flush = [IO.File]::Open($launcherStaged, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::Read)
    try { $flush.Flush($true) } finally { $flush.Dispose() }
    Invoke-WebRequest -UseBasicParsing -Uri "$base/dl/SHA256SUMS.txt" -OutFile $checksums
    $asset = "csx-windows-$arch.exe"
    $line = Get-Content -LiteralPath $checksums | Where-Object { $_ -match "\s\*?$([regex]::Escape($asset))$" } | Select-Object -First 1
    $launcherAsset = "csx-launcher-windows-$arch.exe"
    $launcherLine = Get-Content -LiteralPath $checksums | Where-Object { $_ -match "\s\*?$([regex]::Escape($launcherAsset))$" } | Select-Object -First 1
    Remove-Item -LiteralPath $checksums -Force -ErrorAction SilentlyContinue
    if (-not $line -or -not $launcherLine) { throw 'release checksum does not name the Windows payload and launcher' }
    $expected = ($line -split '\s+', 2)[0].ToLowerInvariant()
    $actual = (Get-FileHash -LiteralPath $staged -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { Remove-Item $staged -Force -ErrorAction SilentlyContinue; throw 'downloaded csx checksum mismatch' }
    $launcherExpected = ($launcherLine -split '\s+', 2)[0].ToLowerInvariant()
    $launcherActual = (Get-FileHash -LiteralPath $launcherStaged -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($launcherActual -ne $launcherExpected) { throw 'downloaded launcher checksum mismatch' }
    $stagedVersion = & $staged version
    if ($LASTEXITCODE -ne 0 -or $stagedVersion -notmatch '^csx v\d+\.\d+\.\d+$') { Remove-Item $staged -Force -ErrorAction SilentlyContinue; throw 'staged csx self-test failed' }
    $launcherVersion = & $launcherStaged --launcher-version
    if ($LASTEXITCODE -ne 0 -or $launcherVersion -ne 'csx-launcher v1.0.0') { throw 'staged launcher self-test failed' }
} catch {
    $msg = $_.Exception.Message
    $hresult = $_.Exception.HResult
    $isAv = ($hresult -eq -2147024671) -or ($msg -match 'virus|potentially unwanted software|operation did not complete successfully')
    Remove-Item $staged -Force -ErrorAction SilentlyContinue
    Remove-Item $launcherStaged -Force -ErrorAction SilentlyContinue
    Remove-Item $checksums -Force -ErrorAction SilentlyContinue
    if ($isAv) {
        Write-Host ""
        Write-Host "Security software (such as Microsoft Defender) quarantined or blocked the downloaded binary." -ForegroundColor Red
        Write-Host "This is a known false positive under official vendor review (issue #70)."
        Write-Host "Do NOT disable your antivirus or add unsafe global exclusions."
        Write-Host "See https://github.com/r2cuerdame/CodeSampleX/issues/70 for status and resolution."
        throw "installation blocked by security software: $msg"
    }
    throw
}

$version = ($stagedVersion -split ' ', 2)[1]
$alreadyLauncher = $false
$installedLauncherVersion = ''
if (Test-Path $exe) {
    try {
		$installedLauncherVersion = (& $exe --launcher-version 2>$null)
		$alreadyLauncher = ($installedLauncherVersion -match '^csx-launcher v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$')
	} catch { $alreadyLauncher = $false }
}
if ($alreadyLauncher -and $installedLauncherVersion -ne $launcherVersion) { throw 'launcher protocol transition requires a newer migration installer; no pointer was changed' }
if ((Test-Path $exe) -and -not $alreadyLauncher) { & $staged update bootstrap-launcher $dir $staged $exe }
else { & $staged update bootstrap-launcher $dir $staged }
if ($LASTEXITCODE -ne 0) { throw 'signed launcher payload bootstrap failed' }
Remove-Item $staged -Force -ErrorAction SilentlyContinue
$replaceLauncher = -not $alreadyLauncher
if ($alreadyLauncher) { $replaceLauncher = ((Get-FileHash $exe -Algorithm SHA256).Hash.ToLowerInvariant() -ne $launcherActual) }

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
if ((Test-Path $exe) -and $replaceLauncher) {
    $aside = Join-Path $dir ("csx.exe.legacy-" + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    Write-Durable $journal ((@{ schema = 1; phase = 'old-renamed'; backup = $aside } | ConvertTo-Json) + "`n")
    try {
        Move-Item -Path $exe -Destination $aside -Force
        $replaced = $true
    } catch {
        throw "stable launcher migration failed; previous csx.exe remains installed"
    }
}
try {
    if ($replaceLauncher) { Move-Item -Path $launcherStaged -Destination $exe -Force }
    else { Remove-Item $launcherStaged -Force }
    & $exe update adopt | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "csx update ownership registration failed" }
} catch {
    if ($replaceLauncher) {
        Remove-Item $exe -Force -ErrorAction SilentlyContinue
        if ($replaced -and (Test-Path $aside)) { Move-Item -Path $aside -Destination $exe -Force }
    }
    throw
}
Remove-Item $journal -Force -ErrorAction SilentlyContinue

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
    $normalized = $userPath -split ';' | ForEach-Object { $_.Trim().Trim('"').TrimEnd('\') }
    $want = $dir.Trim().Trim('"').TrimEnd('\')
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
if ($env:CSX_WORKER_ONLY -eq '1') {
    & $exe init --community --yes --no-agents --no-daemon
} else {
    & $exe init
}

# A replaced binary means an MCP server may still be running the old one.
# It keeps answering with the old code until the editor restarts it, and
# nothing else in the flow would ever mention that.
if ($replaced) {
    Write-Host ''
    Write-Host 'You upgraded an existing install. If your editor is open, restart it:'
    Write-Host 'the csx MCP server it started is still running the previous build.'
}
