# Exercise the same comparator as the installer smoke in an owned test key.
# Never write to HKCU\Environment or its real Path value.
$ErrorActionPreference = 'Stop'
$timer = [Diagnostics.Stopwatch]::StartNew()
function Write-TestPhase([string]$Name) {
    [Console]::Out.WriteLine(('registry test: {0} ({1}ms)' -f $Name, $timer.ElapsedMilliseconds))
    [Console]::Out.Flush()
}
function Assert-RegistryValueStateEqual($Before, $After, [bool]$Expected, [string]$Case) {
    # Use the comparator's defining scope, then restore it after each assertion.
    $autoloadBefore = $script:PSModuleAutoloadingPreference
    try {
        $script:PSModuleAutoloadingPreference = 'None'
        if ((Test-CSXRegistryValueStateEqual $Before $After) -ne $Expected) { throw $Case }
    } finally { $script:PSModuleAutoloadingPreference = $autoloadBefore }
}
Write-TestPhase 'script entered'
. (Join-Path $PSScriptRoot 'windows-registry-state.ps1')
Write-TestPhase 'helper loaded'
$realPathBefore = Get-CSXUserPathState
Write-TestPhase 'real PATH captured'
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
    Write-TestPhase 'owned registry fixture created'
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
    Write-TestPhase 'profile expansion reproduced; comparing raw values'
    Assert-RegistryValueStateEqual $before $after $true 'expansion-only profile change rejected despite unchanged registry'
    Write-TestPhase 'raw comparison completed'

    $key.SetValue('Path', $raw + ';C:\unexpected', [Microsoft.Win32.RegistryValueKind]::ExpandString)
    Assert-RegistryValueStateEqual $before (Get-CSXRegistryValueState $key 'Path') $false 'raw PATH mutation was accepted'
    $key.SetValue('Path', $raw, [Microsoft.Win32.RegistryValueKind]::String)
    Assert-RegistryValueStateEqual $before (Get-CSXRegistryValueState $key 'Path') $false 'registry value-kind mutation was accepted'
    $key.DeleteValue('Path')
    $missing = Get-CSXRegistryValueState $key 'Path'
    Assert-RegistryValueStateEqual $before $missing $false 'PATH deletion was accepted'
    Assert-RegistryValueStateEqual $missing (Get-CSXRegistryValueState $key 'Path') $true 'unchanged absent value rejected'
    $key.SetValue('Path', '', [Microsoft.Win32.RegistryValueKind]::String)
    Assert-RegistryValueStateEqual $missing (Get-CSXRegistryValueState $key 'Path') $false 'absent and empty PATH values were treated as equal'

    $cultureBefore = [Threading.Thread]::CurrentThread.CurrentCulture
    try {
        foreach ($cultureName in @('en-US', 'tr-TR')) {
            [Threading.Thread]::CurrentThread.CurrentCulture = [Globalization.CultureInfo]::GetCultureInfo($cultureName)
            foreach ($case in @(
                @{ Kind='String'; Value='I'; Changed='i' },
                @{ Kind='ExpandString'; Value='%USERPROFILE%\I'; Changed='%USERPROFILE%\i' },
                @{ Kind='Binary'; Value=[byte[]](0,17,255); Changed=[byte[]](0,18,255) },
                @{ Kind='None'; Value=[byte[]](0,17,255); Changed=[byte[]](0,18,255) },
                @{ Kind='DWord'; Value=[int32]-1; Changed=[int32]2147483647 },
                @{ Kind='QWord'; Value=[int64]::MaxValue; Changed=[int64]::MinValue },
                @{ Kind='MultiString'; Value=[string[]]('I','two'); Changed=[string[]]('i','two') }
            )) {
                $kind = [Microsoft.Win32.RegistryValueKind]$case.Kind
                $key.SetValue('Typed', $case.Value, $kind)
                $before = Get-CSXRegistryValueState $key 'Typed'
                Assert-RegistryValueStateEqual $before (Get-CSXRegistryValueState $key 'Typed') $true "$cultureName/$kind equal typed values rejected"
                $key.SetValue('Typed', $case.Changed, $kind)
                Assert-RegistryValueStateEqual $before (Get-CSXRegistryValueState $key 'Typed') $false "$cultureName/$kind typed mutation accepted"
            }
        }
        $key.SetValue('Typed', [string[]]('first','second'), [Microsoft.Win32.RegistryValueKind]::MultiString)
        $before = Get-CSXRegistryValueState $key 'Typed'
        foreach ($changed in @(@('second','first'), @('first'))) {
            $key.SetValue('Typed', [string[]]$changed, [Microsoft.Win32.RegistryValueKind]::MultiString)
            Assert-RegistryValueStateEqual $before (Get-CSXRegistryValueState $key 'Typed') $false 'array order or length mutation accepted'
        }
        $key.SetValue('Typed', [string][char]0x00e9, [Microsoft.Win32.RegistryValueKind]::String)
        $before = Get-CSXRegistryValueState $key 'Typed'
        $key.SetValue('Typed', ('e' + [char]0x0301), [Microsoft.Win32.RegistryValueKind]::String)
        Assert-RegistryValueStateEqual $before (Get-CSXRegistryValueState $key 'Typed') $false 'non-ordinal Unicode string comparison'
    } finally { [Threading.Thread]::CurrentThread.CurrentCulture = $cultureBefore }
    Write-TestPhase 'typed registry comparisons completed without module autoload'
    Write-TestPhase 'PASS: expansion-only change preserves raw state; raw, kind, deletion, and insertion mutations are rejected'
} finally {
    Write-TestPhase 'cleanup entered'
    $env:USERPROFILE = $profileBefore
    $env:LOCALAPPDATA = $localBefore
    if ($null -ne $key) { $key.Dispose() }
    if ($created) {
        if ($testName -cnotmatch '^Software\\CSXBootstrapSmokeTest-[0-9a-f]{32}$') { throw 'refusing unexpected test registry cleanup target' }
        [Microsoft.Win32.Registry]::CurrentUser.DeleteSubKeyTree($testName)
    }
    Assert-RegistryValueStateEqual $realPathBefore (Get-CSXUserPathState) $true 'test changed the real user PATH'
    Write-TestPhase 'cleanup completed; real PATH preserved'
}
