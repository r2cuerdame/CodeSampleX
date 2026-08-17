# Explicitly copy the DPAPI-protected production admin password to the Windows
# clipboard. The password is never printed or placed in a process argument.
param()

$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "admin-credential.ps1")

$paths = Get-CSXAdminCredentialPaths
$secret = Read-CSXAdminCredential $paths.Active
try {
    Copy-CSXSecureStringToClipboard $secret
} finally {
    $secret.Dispose()
}

Write-Output "Admin password copied to the clipboard by explicit request."
Write-Output "Paste it into https://codesamplex.dev/admin, then clear the clipboard."
