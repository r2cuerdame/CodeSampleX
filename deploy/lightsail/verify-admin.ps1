# Re-run the authenticated production admin smoke without exposing the Basic
# credential. Only the numeric HTTP status is printed.
param()

$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "admin-credential.ps1")

$paths = Get-CSXAdminCredentialPaths
$secret = Read-CSXAdminCredential $paths.Active
try {
    try { $status = Invoke-CSXAdminAuthenticatedProbe $secret }
    catch { throw "admin authenticated smoke failed" }
} finally {
    $secret.Dispose()
}

if ($status -ne 200) { throw "admin authenticated smoke failed (HTTP $status)" }
Write-Output "admin authenticated smoke: 200"
