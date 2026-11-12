$ErrorActionPreference = 'Stop'
# A failing native-command stand-in lets us verify the wrapper's exit and
# restoration behavior without compiling another test suite.
function go {
    if ($args[0] -ne 'test' -or $args[1] -ne '-count=9' -or $args[2] -ne './scripts') {
        throw 'Go arguments changed'
    }
    foreach ($key in @('GOTMPDIR', 'TEMP', 'TMP')) {
        if ([Environment]::GetEnvironmentVariable($key, 'Process') -ne (Join-Path $env:LOCALAPPDATA 'csx\gotmp')) {
            throw "$key was not redirected before Go started"
        }
    }
    $global:LASTEXITCODE = 23
}

foreach ($original in @($null, 'inherited-path')) {
    Remove-Item Env:GOTMPDIR -ErrorAction SilentlyContinue
    if ($null -ne $original) { $env:GOTMPDIR = $original }
    $savedTemp = $env:TEMP
    $savedTmp = $env:TMP
    $savedPriority = [Diagnostics.Process]::GetCurrentProcess().PriorityClass
    & "$PSScriptRoot/go-test.ps1" -count=9 ./scripts
    if ($LASTEXITCODE -ne 23) { throw 'Go exit code was not preserved' }
    if ([Environment]::GetEnvironmentVariable('GOTMPDIR', 'Process') -cne $original) {
        throw 'GOTMPDIR was not restored (including absent versus empty)'
    }
    if ($env:TEMP -cne $savedTemp -or $env:TMP -cne $savedTmp) { throw 'TEMP/TMP leaked' }
    if ([Diagnostics.Process]::GetCurrentProcess().PriorityClass -ne $savedPriority) { throw 'Priority leaked' }
}
Write-Output 'PASS: arguments, failure exit code, absent/present environment and priority restoration'
exit 0
