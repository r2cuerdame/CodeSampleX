# Run the actual installer and native release binaries in an isolated profile.
# -DistDir tests unpublished signed release artifacts; omit it for production.
param(
    [string]$BaseUrl = 'https://codesamplex.dev',
    [string]$DistDir = ''
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
. (Join-Path $PSScriptRoot 'windows-registry-state.ps1')
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('csx-bootstrap-smoke-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $scratch | Out-Null
$saved = @{}
foreach ($name in @('LOCALAPPDATA', 'APPDATA', 'USERPROFILE', 'CSX_HOME', 'CSX_INSTALL_ONLY', 'PATH')) {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$userPathBefore = Get-CSXUserPathState
try {
    $env:LOCALAPPDATA = Join-Path $scratch 'local'
    $env:APPDATA = Join-Path $scratch 'roaming'
    $env:USERPROFILE = Join-Path $scratch 'profile'
    $env:CSX_HOME = Join-Path $scratch 'home'
    $env:CSX_INSTALL_ONLY = '1'
    $script:observedStable = $null
    $script:bootstrapDist = if ($DistDir) { (Resolve-Path -LiteralPath $DistDir).Path } else { '' }
    # The installer still executes unchanged. Only HTTP transport is mapped
    # to unpublished artifacts for the release gate; no verification is mocked.
    function Invoke-WebRequest {
        param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile)
        if ($script:bootstrapDist) {
            if ($Uri -eq "$BaseUrl/dl/csx-update-stable.json") {
                $name = 'csx-update-stable.json'
            } else {
                $prefix = "https://github.com/r2cuerdame/CodeSampleX/releases/download/$($script:observedStable.version)/"
                if (-not $Uri.StartsWith($prefix, [StringComparison]::Ordinal)) { throw "unversioned or mixed installer URL: $Uri" }
                $name = $Uri.Substring($prefix.Length)
                if ($name -notmatch '^(csx-bootstrap-stable\.json|SHA256SUMS\.txt|csx-(launcher-)?windows-(amd64|arm64)\.exe)$') { throw 'unexpected installer asset' }
            }
            Copy-Item -LiteralPath (Join-Path $script:bootstrapDist $name) -Destination $OutFile
        } else {
            Microsoft.PowerShell.Utility\Invoke-WebRequest -UseBasicParsing -Uri $Uri -OutFile $OutFile
        }
        if ($Uri -eq "$BaseUrl/dl/csx-update-stable.json") {
            $envelope = Get-Content -LiteralPath $OutFile -Raw | ConvertFrom-Json
            $script:observedStable = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($envelope.payload)) | ConvertFrom-Json
        }
    }
    if ($DistDir) {
        $installer = (Get-Content -LiteralPath (Join-Path $PSScriptRoot '../internal/web/install/install.ps1') -Raw).Replace('__CSX_BASE_URL__', $BaseUrl)
    } else {
        $installer = Microsoft.PowerShell.Utility\Invoke-RestMethod -Uri "$BaseUrl/install.ps1"
    }
    # Scriptblock scope keeps the install-only return inside the installer.
    & ([scriptblock]::Create($installer))
    if (-not $script:observedStable) { throw 'installer never captured a stable manifest' }
    $root = Join-Path $env:LOCALAPPDATA 'csx'
    $exe = Join-Path $root 'csx.exe'
    $version = & $exe version
    if ($LASTEXITCODE -ne 0 -or $version -cne "csx $($script:observedStable.version)") { throw 'installed payload identity mismatch' }
    $active = Get-Content -LiteralPath (Join-Path $root 'active.json') -Raw | ConvertFrom-Json
    if ($active.current.version -cne $script:observedStable.version -or $active.current.sequence -ne $script:observedStable.sequence) { throw 'active pointer identity mismatch' }
    $asset = $script:observedStable.assets | Where-Object { $_.os -eq 'windows' -and $_.arch -eq $(if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }) }
    $payload = Join-Path $root "payloads/$($active.current.version)/csx-payload.exe"
    $payloadHash = (Get-FileHash -LiteralPath $payload -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($payloadHash -cne $asset.sha256 -or $active.current.sha256 -cne $asset.sha256) { throw 'installed payload signed hash mismatch' }
    if (-not (Test-CSXRegistryValueStateEqual $userPathBefore (Get-CSXUserPathState))) { throw 'installer modified real user PATH during isolated smoke' }
    [pscustomobject]@{
        version = $active.current.version
        sequence = $active.current.sequence
        payloadSHA256 = $payloadHash
        launcherSHA256 = (Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant()
        source = $(if ($DistDir) { 'unpublished signed artifacts' } else { $BaseUrl })
        result = 'clean Windows bootstrap passed'
    } | ConvertTo-Json
} finally {
    foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
    # Retain the isolated files as evidence. Nothing in the actual user's
    # install, PATH, daemon state, or agent configuration is touched.
    Write-Output "Isolated bootstrap evidence: $scratch"
}
