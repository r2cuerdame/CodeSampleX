# Windows test entry point. Set paths before Go builds the test executables.
# All arguments are passed unchanged to go test; no arguments means ./... .
$ErrorActionPreference = 'Stop'
$saved = @{}
foreach ($key in @('GOTMPDIR', 'TEMP', 'TMP')) {
    $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
$process = [Diagnostics.Process]::GetCurrentProcess()
$priority = $process.PriorityClass
try {
    . "$PSScriptRoot/windows-go-test-env.ps1"
    if ($args.Count -eq 0) { $args = @('./...') }
    & go test @args
    $result = $LASTEXITCODE
} finally {
    foreach ($key in $saved.Keys) {
        if ($null -eq $saved[$key]) {
            # PowerShell 7 can distinguish an empty variable from an absent one.
            Remove-Item -LiteralPath "Env:$key" -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process')
        }
    }
    $process.PriorityClass = $priority
}
exit $result
