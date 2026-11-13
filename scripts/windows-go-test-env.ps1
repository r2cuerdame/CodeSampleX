# Dot-source before invoking Go, not from TestMain: the outer Go process has
# already built its test .exe by the time TestMain can change the environment.
param([switch]$ExportGitHubEnvironment)

$ErrorActionPreference = 'Stop'
if ([Environment]::OSVersion.Platform -ne 'Win32NT') { throw 'Windows Go test environment requires Windows' }
$local = $env:LOCALAPPDATA
if (-not $local) { $local = [Environment]::GetFolderPath('LocalApplicationData') }
if (-not $local -or -not [IO.Path]::IsPathRooted($local)) {
    throw 'A rooted LocalApplicationData path is required for Windows Go tests'
}
$goTemp = Join-Path $local 'csx\gotmp'
[IO.Directory]::CreateDirectory($goTemp) | Out-Null
foreach ($key in @('GOTMPDIR', 'TEMP', 'TMP')) {
    [Environment]::SetEnvironmentVariable($key, $goTemp, 'Process')
    if ($ExportGitHubEnvironment) {
        if (-not $env:GITHUB_ENV) { throw 'GITHUB_ENV is required to export test paths' }
        Add-Content -LiteralPath $env:GITHUB_ENV -Value "$key=$goTemp" -Encoding utf8
    }
}
# Children inherit this priority. GOCACHE and all persistent settings stay as-is.
[Diagnostics.Process]::GetCurrentProcess().PriorityClass = 'BelowNormal'
