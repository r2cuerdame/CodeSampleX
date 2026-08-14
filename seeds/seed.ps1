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
#        .\seed.ps1 -Only php-parser-v5-factory,dart-crypto-digest
#
# -Only exists because publishing is rate limited to 10/hour per address:
# a full re-run of every seed spends the budget re-publishing samples the
# network already has, and the new ones fail at the end.
param(
    [string]$Server = "https://codesamplex.dev",
    [string]$Seeder = "r2cuerdame",
    [string]$CsxHome = "$env:LOCALAPPDATA\csx-seed",
    [string[]]$Only = @()
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

# Seeds are discovered, never listed: a hard-coded array silently skipped
# every sample added after it was written.
$names = Get-ChildItem $seeds -Directory |
    Where-Object { Test-Path (Join-Path $_.FullName "csx.json") } |
    ForEach-Object { $_.Name } | Sort-Object
if ($Only.Count -gt 0) {
    $missing = $Only | Where-Object { $names -notcontains $_ }
    if ($missing) { throw "no such seed: $($missing -join ', ')" }
    $names = $names | Where-Object { $Only -contains $_ }
}
Write-Output "seeds found: $($names -join ', ')"
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
    $ok = ($LASTEXITCODE -eq 0)
    $ErrorActionPreference = $prevEAP
    Remove-Item Env:CSX_TEST_ASSUME_YES -ErrorAction SilentlyContinue
    # Exit code, not a substring. Matching "Published" in the output read a
    # REFUSED report as a success for two runs straight, because one of the
    # maintainer addresses the refusal listed was bschussek@2bepublished.at.
    if ($ok) {
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
# The command comes from each manifest, not from a hard-coded `node
# test/contract.mjs`: that assumption ran a Node command inside the python,
# go and rust seed dirs, which recorded nothing and left stray lockfiles
# behind. And csx run observes on the HOST, so a seed whose toolchain is
# not installed here is reported as skipped rather than failed.
foreach ($n in $names) {
    $dir = Join-Path $seeds $n
    $m = Get-Content (Join-Path $dir "csx.json") -Raw | ConvertFrom-Json
    $argv = @($m.contractCommand)
    if (-not $argv -or $argv.Count -eq 0) {
        Write-Output ("  {0,-30} no contractCommand" -f $n); continue
    }
    if (-not (Get-Command $argv[0] -ErrorAction SilentlyContinue)) {
        Write-Output ("  {0,-30} skipped ({1} not on this host)" -f $n, $argv[0]); continue
    }
    Push-Location $dir
    $ErrorActionPreference = "Continue"
    & $csx run -- @argv 2>&1 | Out-Null
    $code = $LASTEXITCODE
    $ErrorActionPreference = $prevEAP
    Pop-Location
    Write-Output ("  {0,-30} csx run exit={1}" -f $n, $code)
}
& $csx sync | Select-Object -Last 1

Write-Output ""
Write-Output "published $($published.Count)/$($names.Count) samples"
Write-Output "check: $Server/stats"
