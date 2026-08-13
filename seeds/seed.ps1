# Seeds the public network with real, verified samples and real evidence.
#
# Nothing here is fabricated: each sample is created from the seed directory,
# verified by actually running its contract in a sandbox, and published with
# the resulting signed receipt. Cross-verification is deliberately NOT
# simulated — a second identity on this same machine would inflate the
# independence numbers the trust model depends on (goal.md §16.5), so
# CROSS_PASS is left for real peers to earn.
#
# Usage: .\seed.ps1 [-Server https://codesamplex.dev] [-Seeder r2cuerdame]
param(
    [string]$Server = "https://codesamplex.dev",
    [string]$Seeder = "r2cuerdame",
    [string]$CsxHome = "$env:LOCALAPPDATA\csx-seed"
)
$ErrorActionPreference = "Stop"
$seeds = $PSScriptRoot
$repo = Resolve-Path (Join-Path $seeds "..")
$csx = Join-Path $env:TEMP "csx-seed.exe"

Write-Output "== building csx =="
Push-Location $repo
$env:CGO_ENABLED = "0"
go build -o $csx ./cmd/csx
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "csx build failed" }
Pop-Location

$env:CSX_HOME = $CsxHome
New-Item -ItemType Directory -Force $CsxHome | Out-Null
Write-Output "== init (community, no agent config touched) =="
& $csx init --community --yes --no-agents --server $Server | Select-Object -Last 2

$names = @("axios-post-json", "zod-parse-validate", "express-json-route", "dayjs-utc-format")
$published = @()
foreach ($n in $names) {
    Write-Output ""
    Write-Output "== $n =="
    $dir = Join-Path $seeds $n
    # A published sample carries source + lockfile only; node_modules exists
    # in the seed dir for the evidence phase below, so build from a copy.
    $clean = Join-Path ([System.IO.Path]::GetTempPath()) ("csx-seed-" + $n)
    if (Test-Path $clean) { Remove-Item -Recurse -Force $clean }
    New-Item -ItemType Directory -Force $clean | Out-Null
    Get-ChildItem $dir -Force | Where-Object { $_.Name -ne "node_modules" } |
        ForEach-Object { Copy-Item $_.FullName -Destination $clean -Recurse -Force }

    $prevEAP = $ErrorActionPreference; $ErrorActionPreference = "Continue"
    $out = & $csx sample create $clean 2>&1 | ForEach-Object { "$_" }
    $ErrorActionPreference = $prevEAP
    $id = ([regex]::Match(($out -join "`n"), "sha256:[0-9a-f]{64}")).Value
    if (-not $id) { throw "create failed for ${n}:`n$($out -join "`n")" }
    Write-Output "  id: $id"

    $ErrorActionPreference = "Continue"
    $v = & $csx sample verify $id 2>&1 | ForEach-Object { "$_" }
    $ErrorActionPreference = $prevEAP
    $v | Where-Object { $_ -match "^(Sandbox|resolve|compile|load|contract)" } | ForEach-Object { Write-Output "  $_" }
    if (($v -join "`n") -notmatch "contract\s+PASS") {
        Write-Output "  SKIPPED publish: contract did not pass"
        continue
    }

    $env:CSX_TEST_ASSUME_YES = "1"
    $ErrorActionPreference = "Continue"
    $p = & $csx sample publish $id --seeder $Seeder --assume-yes --server $Server 2>&1 | ForEach-Object { "$_" }
    $ErrorActionPreference = $prevEAP
    Remove-Item Env:CSX_TEST_ASSUME_YES -ErrorAction SilentlyContinue
    if (($p -join "`n") -match "Published") {
        Write-Output "  published: $Server/samples/$id"
        $published += $id
    } else {
        Write-Output "  publish failed:`n    $($p -join "`n    ")"
    }
}

Write-Output ""
Write-Output "== evidence from the seed projects =="
# Real observations: each seed project is scanned and its contract run
# through csx, so the packages above get PROJECT_TEST evidence too.
foreach ($n in $names) {
    $dir = Join-Path $seeds $n
    Push-Location $dir
    $ErrorActionPreference = "Continue"
    & $csx run -- node test/contract.mjs 2>&1 | Out-Null
    $code = $LASTEXITCODE
    $ErrorActionPreference = $prevEAP
    Pop-Location
    Write-Output ("  {0,-22} csx run exit={1}" -f $n, $code)
}
& $csx sync | Select-Object -Last 1

Write-Output ""
Write-Output "published $($published.Count)/$($names.Count) samples"
Write-Output "check: $Server/stats"
