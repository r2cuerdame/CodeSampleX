# Read stored registry state without expanding values against the current
# process environment. Isolation deliberately changes USERPROFILE/LOCALAPPDATA.
function Get-CSXRegistryValueState {
    param([AllowNull()][Microsoft.Win32.RegistryKey]$Key, [string]$Name)
    $exists = $null -ne $Key -and $Key.GetValueNames() -contains $Name
    if (-not $exists) {
        return [pscustomobject]@{ Exists = $false; Kind = $null; RawValue = $null }
    }
    return [pscustomobject]@{
        Exists = $true
        Kind = $Key.GetValueKind($Name)
        RawValue = $Key.GetValue($Name, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    }
}

function Test-CSXRegistryValueStateEqual {
    param($Before, $After)
    if ($Before.Exists -ne $After.Exists -or $Before.Kind -ne $After.Kind) { return $false }
    # Registry values are .NET primitives or arrays. Compare their contents
    # directly, with ordinal strings, without importing modules after isolation.
    return [Collections.StructuralComparisons]::StructuralEqualityComparer.Equals($Before.RawValue, $After.RawValue)
}

function Get-CSXUserPathState {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $false)
    try { return Get-CSXRegistryValueState -Key $key -Name 'Path' }
    finally { if ($null -ne $key) { $key.Dispose() } }
}
