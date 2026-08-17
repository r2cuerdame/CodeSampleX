param(
    [switch]$ReplacePending
)

$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "admin-credential.ps1")

$paths = Get-CSXAdminCredentialPaths
if ((Test-Path -LiteralPath $paths.Pending -PathType Leaf) -and -not $ReplacePending) {
    throw "a pending admin credential already exists; finish or explicitly replace it first"
}

$plaintext = $null
$secret = New-Object Security.SecureString
try {
    $plaintext = [Console]::In.ReadToEnd().TrimEnd("`r", "`n")
    if ($plaintext.Length -lt 10 -or $plaintext.Length -gt 128) {
        throw "admin password must be between 10 and 128 characters"
    }
    foreach ($character in $plaintext.ToCharArray()) {
        $code = [int]$character
        if ($code -lt 33 -or $code -gt 126) {
            throw "admin password must contain printable non-space ASCII characters only"
        }
        $secret.AppendChar($character)
    }
    $secret.MakeReadOnly()
    Save-CSXAdminCredential $secret $paths.Pending
    Write-Output "staged the requested admin credential in the current-user DPAPI store"
} finally {
    $plaintext = $null
    $secret.Dispose()
}
