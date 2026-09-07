# Exercise the same comparator as the installer smoke in an owned test key.
# Never write to HKCU\Environment or its real Path value.
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'windows-registry-state.ps1')
$realPathBefore = Get-CSXUserPathState
$profileBefore = $env:USERPROFILE
$localBefore = $env:LOCALAPPDATA
$testName = 'Software\CSXBootstrapSmokeTest-' + [Guid]::NewGuid().ToString('N')
$key = $null
$created = $false
try {
    $existing = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($testName, $false)
    if ($null -ne $existing) { $existing.Dispose(); throw 'test registry key already exists' }
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey($testName)
    $created = $true
    $raw = '%USERPROFILE%\tools;%LOCALAPPDATA%\bin'
    $key.SetValue('Path', $raw, [Microsoft.Win32.RegistryValueKind]::ExpandString)
    $env:USERPROFILE = 'C:\csx-test-original-profile'
    $env:LOCALAPPDATA = 'C:\csx-test-original-local'
    $before = Get-CSXRegistryValueState $key 'Path'
    $expandedBefore = [string]$key.GetValue('Path')
    $env:USERPROFILE = 'C:\csx-test-isolated-profile'
    $env:LOCALAPPDATA = 'C:\csx-test-isolated-local'
    $after = Get-CSXRegistryValueState $key 'Path'
    $expandedAfter = [string]$key.GetValue('Path')
    if ($expandedBefore -ceq $expandedAfter) { throw 'fixture did not reproduce the expanded-value false positive' }
    if ($before.RawValue -cne $raw -or $after.RawValue -cne $raw) { throw 'snapshot expanded the stored registry value' }
    if (-not (Test-CSXRegistryValueStateEqual $before $after)) { throw 'expansion-only profile change rejected despite unchanged registry' }

    $key.SetValue('Path', $raw + ';C:\unexpected', [Microsoft.Win32.RegistryValueKind]::ExpandString)
    if (Test-CSXRegistryValueStateEqual $before (Get-CSXRegistryValueState $key 'Path')) { throw 'raw PATH mutation was accepted' }
    $key.SetValue('Path', $raw, [Microsoft.Win32.RegistryValueKind]::String)
    if (Test-CSXRegistryValueStateEqual $before (Get-CSXRegistryValueState $key 'Path')) { throw 'registry value-kind mutation was accepted' }
    $key.DeleteValue('Path')
    $missing = Get-CSXRegistryValueState $key 'Path'
    if (Test-CSXRegistryValueStateEqual $before $missing) { throw 'PATH deletion was accepted' }
    if (-not (Test-CSXRegistryValueStateEqual $missing (Get-CSXRegistryValueState $key 'Path'))) { throw 'unchanged absent value rejected' }
    $key.SetValue('Path', '', [Microsoft.Win32.RegistryValueKind]::String)
    if (Test-CSXRegistryValueStateEqual $missing (Get-CSXRegistryValueState $key 'Path')) { throw 'absent and empty PATH values were treated as equal' }
    Write-Output 'PASS: expansion-only change preserves raw state; raw, kind, deletion, and insertion mutations are rejected.'
} finally {
    $env:USERPROFILE = $profileBefore
    $env:LOCALAPPDATA = $localBefore
    if ($null -ne $key) { $key.Dispose() }
    if ($created) {
        if ($testName -cnotmatch '^Software\\CSXBootstrapSmokeTest-[0-9a-f]{32}$') { throw 'refusing unexpected test registry cleanup target' }
        [Microsoft.Win32.Registry]::CurrentUser.DeleteSubKeyTree($testName)
    }
    if (-not (Test-CSXRegistryValueStateEqual $realPathBefore (Get-CSXUserPathState))) { throw 'test changed the real user PATH' }
}
