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
    # Serialization compares scalar and array registry value types by content.
    $beforeRaw = ConvertTo-Json -InputObject $Before.RawValue -Compress
    $afterRaw = ConvertTo-Json -InputObject $After.RawValue -Compress
    return [string]::Equals($beforeRaw, $afterRaw, [StringComparison]::Ordinal)
}

function Get-CSXUserPathState {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $false)
    try { return Get-CSXRegistryValueState -Key $key -Name 'Path' }
    finally { if ($null -ne $key) { $key.Dispose() } }
}
