<#
.SYNOPSIS
Ask Microsoft Defender what it thinks of a CodeSampleX Windows release
artifact, without letting the question quarantine the answer.

.DESCRIPTION
Between 2026-08-24 and 2026-08-26 this project's own Windows install lost its
payload to Defender five times (Trojan:Win32/Bearfoos.A!ml, ThreatID
2147731250, and Bearfoos.B!ml for v0.1.39). The launcher recovers from that
(R2C-181), but recovering is not the same as knowing, and "does Defender object
to what we are about to ship" had no repeatable answer.

This script is that answer, and it is careful about three things:

1. It verifies every downloaded asset against the release's own SHA256SUMS.txt
   before scanning. Scanning a truncated download and reporting it clean would
   be worse than not scanning at all.
2. It scans with -DisableRemediation, so asking cannot quarantine the artifact
   being asked about.
3. It never reports "could not measure" as "clean". No Defender, no scanner, a
   failed download and a mismatched checksum all exit 2.

READ THE VERDICT NARROWLY. A clean result is scoped to the security
intelligence version printed beside it and to a static scan. It is evidence
that this build is not flagged today; it is not a promise about tomorrow, and
it does not cover execution. Both were measured:

  - v0.1.39's payload was quarantined as Bearfoos.B!ml on 2026-08-25 and
    scanned clean with the same engine on 2026-08-26. The bytes did not move;
    the definitions did.
  - Every install-root quarantine so far was raised by real-time protection
    with the launcher (csx.exe) as the acting process -- that is, at execution,
    not on download.

So a green run here does not close R2C-189. It is the pre-release half; the
post-install half is `csx update status` on a real machine, which reports any
payload recovery the launcher had to perform.

.PARAMETER Tag
Release tag to check, e.g. v0.1.46. Defaults to the repository's latest
release.

.PARAMETER Path
Scan local files instead of a release. Repeatable.

.PARAMETER Arch
Which Windows architectures to fetch. Defaults to amd64 and arm64.

.EXAMPLE
powershell -NoProfile -File scripts/defender-release-check.ps1 -Tag v0.1.46

.EXAMPLE
powershell -NoProfile -File scripts/defender-release-check.ps1 -Path dist/csx-windows-amd64.exe

.EXAMPLE
./scripts/defender-release-check.ps1 -Path dist/csx-windows-amd64.exe,dist/csx-launcher-windows-amd64.exe
#>
[CmdletBinding()]
param(
    [string] $Tag,
    # Several files are one comma-separated value -- `-Path a.exe,b.exe`. A
    # space-separated second file would bind to -Tag and be looked up as a
    # release, so the list has to be a list.
    [string[]] $Path,
    [string[]] $Arch = @('amd64', 'arm64'),
    [string] $Repository = 'r2cuerdame/CodeSampleX'
)

$ErrorActionPreference = 'Stop'

$EXIT_CLEAN = 0
$EXIT_FLAGGED = 1
$EXIT_UNMEASURED = 2

function Fail-Unmeasured([string] $Message) {
    Write-Host "UNMEASURED: $Message"
    Write-Host 'Nothing was measured, so nothing is known. This is not a clean result.'
    exit $EXIT_UNMEASURED
}

function Get-Scanner {
    if ($env:CSX_DEFENDER_MPCMDRUN) {
        if (Test-Path -LiteralPath $env:CSX_DEFENDER_MPCMDRUN) { return $env:CSX_DEFENDER_MPCMDRUN }
        Fail-Unmeasured "CSX_DEFENDER_MPCMDRUN=$($env:CSX_DEFENDER_MPCMDRUN) is not a file"
    }
    # Defender updates itself into a versioned platform directory and leaves
    # the Program Files copy behind as an older stub, so the newest platform
    # build is the one whose verdict real-time protection would give.
    $root = Join-Path $env:ProgramData 'Microsoft\Windows Defender\Platform'
    if (Test-Path -LiteralPath $root) {
        $candidate = Get-ChildItem -LiteralPath $root -Directory -ErrorAction SilentlyContinue |
            Sort-Object Name -Descending |
            ForEach-Object { Join-Path $_.FullName 'MpCmdRun.exe' } |
            Where-Object { Test-Path -LiteralPath $_ } |
            Select-Object -First 1
        if ($candidate) { return $candidate }
    }
    $fallback = Join-Path $env:ProgramFiles 'Windows Defender\MpCmdRun.exe'
    if (Test-Path -LiteralPath $fallback) { return $fallback }
    Fail-Unmeasured 'MpCmdRun.exe was not found; this machine has no Defender scanner to ask'
}

function Get-DefinitionVersion {
    try {
        $k = 'HKLM:\SOFTWARE\Microsoft\Windows Defender\Signature Updates'
        return (Get-ItemProperty -LiteralPath $k -ErrorAction Stop).AVSignatureVersion
    } catch {
        return 'unknown'
    }
}

# Returns 'CLEAN', 'FLAGGED <names>', or throws. MpCmdRun answers a missing
# file with the same exit code it uses for "threats found", so the caller must
# have confirmed the file exists first -- otherwise a typo reads as malware.
function Invoke-Scan([string] $Scanner, [string] $File) {
    $raw = & $Scanner -Scan -ScanType 3 -File $File -DisableRemediation 2>&1 | Out-String
    if ($raw -match 'found no threats') { return 'CLEAN' }
    if (($raw -match 'found\s+\d+\s+threats?') -or ($raw -match 'LIST OF DETECTED THREATS')) {
        $names = @()
        foreach ($m in [regex]::Matches($raw, '(?im)^\s*Threat\s*:\s*(\S.*?)\s*$')) {
            $names += $m.Groups[1].Value.Trim()
        }
        if ($names.Count -eq 0) { $names = @('unnamed-detection') }
        return 'FLAGGED ' + (($names | Select-Object -Unique) -join ', ')
    }
    throw "Defender produced no verdict for ${File}: $($raw.Trim())"
}

function Get-ReleaseTargets([string] $ReleaseTag, [string[]] $Architectures, [string] $Repo, [string] $WorkDir) {
    if (-not $ReleaseTag) {
        $latest = Invoke-RestMethod -UseBasicParsing -Uri "https://api.github.com/repos/$Repo/releases/latest"
        $ReleaseTag = $latest.tag_name
    }
    Write-Host "Release: $ReleaseTag  (repository $Repo)"
    $base = "https://github.com/$Repo/releases/download/$ReleaseTag"

    $sums = Join-Path $WorkDir 'SHA256SUMS.txt'
    & curl.exe -sSL --retry 3 --fail -o $sums "$base/SHA256SUMS.txt"
    if ($LASTEXITCODE -ne 0) { Fail-Unmeasured "could not download $base/SHA256SUMS.txt" }

    $expected = @{}
    foreach ($line in (Get-Content -LiteralPath $sums)) {
        $parts = $line -split '\s+', 2
        if ($parts.Count -eq 2) { $expected[$parts[1].Trim().TrimStart('*')] = $parts[0].Trim().ToLowerInvariant() }
    }

    $names = @()
    foreach ($a in $Architectures) {
        $names += "csx-windows-$a.exe"
        $names += "csx-launcher-windows-$a.exe"
    }

    $targets = @()
    foreach ($n in $names) {
        if (-not $expected.ContainsKey($n)) { Fail-Unmeasured "$ReleaseTag/SHA256SUMS.txt does not name $n" }
        $dest = Join-Path $WorkDir $n
        & curl.exe -sSL --retry 3 --fail -o $dest "$base/$n"
        if ($LASTEXITCODE -ne 0) { Fail-Unmeasured "could not download $base/$n" }
        $actual = (Get-FileHash -LiteralPath $dest -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expected[$n]) {
            Fail-Unmeasured "$n does not match the release checksum (got $actual, want $($expected[$n]))"
        }
        Write-Host "  verified $n  sha256=$actual"
        $targets += $dest
    }
    return $targets
}

# ---------------------------------------------------------------- main ------

if ($env:OS -ne 'Windows_NT') { Fail-Unmeasured 'this check only exists where Defender does' }

$scanner = Get-Scanner
$definitions = Get-DefinitionVersion
Write-Host "Scanner: $scanner"
Write-Host "Security intelligence: $definitions"
Write-Host ''

$work = Join-Path ([IO.Path]::GetTempPath()) ('csx-defender-check-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Force -Path $work | Out-Null

$targets = @()
try {
    if ($Path) {
        foreach ($p in $Path) {
            if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { Fail-Unmeasured "$p is not a file" }
            $targets += (Resolve-Path -LiteralPath $p).Path
        }
    } else {
        $targets = Get-ReleaseTargets -ReleaseTag $Tag -Architectures $Arch -Repo $Repository -WorkDir $work
    }

    Write-Host ''
    $flagged = @()
    foreach ($t in $targets) {
        try {
            $verdict = Invoke-Scan -Scanner $scanner -File $t
        } catch {
            Fail-Unmeasured $_.Exception.Message
        }
        Write-Host ("{0,-9} {1}" -f $verdict.Split(' ')[0], (Split-Path $t -Leaf))
        if ($verdict.StartsWith('FLAGGED')) {
            $flagged += ((Split-Path $t -Leaf) + ' -> ' + $verdict.Substring(8))
        }
    }

    Write-Host ''
    if ($flagged.Count -gt 0) {
        Write-Host 'FLAGGED by Microsoft Defender:'
        foreach ($f in $flagged) { Write-Host "  $f" }
        Write-Host ''
        Write-Host "Security intelligence version: $definitions"
        Write-Host 'This is a false positive on an unsigned Go binary until proven otherwise.'
        Write-Host 'See docs/operations.md, "Defender and the Windows release payload", for the'
        Write-Host 'mitigation ladder and which rungs need a human decision.'
        exit $EXIT_FLAGGED
    }

    Write-Host "No Defender objection today, at security intelligence $definitions."
    Write-Host 'That is a static verdict from one definition build. It is not a promise:'
    Write-Host 'v0.1.39 scanned clean the day after the same engine quarantined it, and every'
    Write-Host 'install-root quarantine so far was raised at execution, not on download.'
    Write-Host 'The other half of this check is `csx update status` on a real install.'
    exit $EXIT_CLEAN
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
