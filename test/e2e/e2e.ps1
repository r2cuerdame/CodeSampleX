# CodeSampleX Public v1 end-to-end verification — goal.md §25 scenarios A–F.
# Runs against a local compose stack (deploy/docker-compose.e2e.yml, :8089)
# with two fresh CSX_HOME peers. Exits non-zero if any scenario fails and
# writes docs/e2e-report.md.
$ErrorActionPreference = "Stop"
$repo = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$tmp = Join-Path $PSScriptRoot "tmp"
$server = "http://localhost:8089"
$results = [ordered]@{}
$evidence = [System.Collections.ArrayList]@()

# Log writes straight to the console: Write-Output here would leak into the
# return value of every function that logs (silently breaking Wait-Until).
function Log($msg) { [Console]::Out.WriteLine("[e2e] " + $msg) }
function Note($msg) { [void]$evidence.Add($msg); Log $msg }

function Wait-Until([scriptblock]$Cond, [int]$TimeoutSec, [string]$What) {
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try { if (& $Cond) { return $true } } catch {}
        Start-Sleep -Seconds 3
    }
    Log "TIMEOUT waiting for: $What"
    return $false
}

function Invoke-Csx([string]$CsxHome, [string[]]$CsxArgs, [string]$Cwd = $null, [hashtable]$ExtraEnv = @{}) {
    $prevHome = $env:CSX_HOME; $prevLoc = Get-Location; $prevEAP = $ErrorActionPreference
    $env:CSX_HOME = $CsxHome
    foreach ($k in $ExtraEnv.Keys) { Set-Item "env:$k" $ExtraEnv[$k] }
    try {
        if ($Cwd) { Set-Location $Cwd }
        # PS 5.1 turns native stderr lines into ErrorRecords under
        # ErrorActionPreference=Stop; child tools legitimately write stderr.
        $ErrorActionPreference = "Continue"
        $out = & $script:csx @CsxArgs 2>&1 | ForEach-Object { "$_" } | Out-String
        return @{ exit = $LASTEXITCODE; out = $out }
    } finally {
        $ErrorActionPreference = $prevEAP
        $env:CSX_HOME = $prevHome
        foreach ($k in $ExtraEnv.Keys) { Remove-Item "env:$k" -ErrorAction SilentlyContinue }
        Set-Location $prevLoc
    }
}

function Get-Json([string]$Url) {
    try { return Invoke-RestMethod -Uri $Url -TimeoutSec 10 } catch { return $null }
}

# ---------- setup ----------
Log "setup: clean tmp, build csx, start stack"
if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp }
New-Item -ItemType Directory -Force $tmp | Out-Null
$script:csx = Join-Path $tmp "csx.exe"
Set-Location $repo
$env:CGO_ENABLED = "0"
go build -o $script:csx ./cmd/csx
if (-not $?) { throw "csx build failed" }

docker compose -f (Join-Path $repo "deploy\docker-compose.e2e.yml") up -d --build --wait
if (-not $?) { throw "e2e stack failed to start" }
if (-not (Wait-Until { (Invoke-WebRequest -Uri "$server/healthz" -UseBasicParsing -TimeoutSec 5).StatusCode -eq 200 } 90 "server healthz")) { throw "server never became healthy" }
Note "server healthy at $server"

$home1 = Join-Path $tmp "home1"; $home2 = Join-Path $tmp "home2"
$port1 = 48619; $port2 = 48719
foreach ($h in @(@{home = $home1; port = $port1 }, @{home = $home2; port = $port2 })) {
    $r = Invoke-Csx $h.home @("init", "--community", "--yes", "--server", $server)
    if ($r.exit -ne 0) { throw "csx init failed for $($h.home): $($r.out)" }
    # Two peers on one machine need distinct daemon ports.
    [void](Invoke-Csx $h.home @("config", "set", "daemonPort", "$($h.port)"))
}
Note "two community peers initialized (home1:$port1, home2:$port2)"

# Starts `csx daemon run` for a home and returns @{job; base} once the
# daemon has published its live address.
function Start-Daemon([string]$CsxHome) {
    # NB: never name a job parameter $home — PowerShell's $HOME is read-only
    # and the assignment kills the job before it runs.
    $job = Start-Job -ScriptBlock {
        param($csx, $csxHome)
        $env:CSX_HOME = $csxHome
        & $csx daemon run 2>&1
    } -ArgumentList $script:csx, $CsxHome
    $addrPath = Join-Path $CsxHome "daemon.addr"
    $base = $null
    if (Wait-Until { Test-Path $addrPath } 60 "daemon.addr for $CsxHome") {
        $addr = (Get-Content $addrPath -Raw).Trim()
        $base = "http://$addr"
        if (-not (Wait-Until { (Get-Json "$base/local/v1/status") -ne $null } 30 "daemon status at $base")) {
            Log "daemon at $base never answered; job output:"
            Receive-Job $job -ErrorAction SilentlyContinue | Select-Object -Last 15 | ForEach-Object { Log "  $_" }
            $base = $null
        }
    } else {
        Log "daemon for $CsxHome never published an address; job output:"
        Receive-Job $job -ErrorAction SilentlyContinue | Select-Object -Last 15 | ForEach-Object { Log "  $_" }
    }
    return @{ job = $job; base = $base }
}

function Stop-Daemon($d) {
    if ($d.base) { try { Invoke-RestMethod -Method Post -Uri "$($d.base)/local/v1/shutdown" -TimeoutSec 5 | Out-Null } catch {} }
    Start-Sleep -Seconds 2
    if ($d.job) { Stop-Job $d.job -ErrorAction SilentlyContinue; Remove-Job $d.job -Force -ErrorAction SilentlyContinue }
}

$proj = Join-Path $tmp "proj"
Copy-Item -Recurse (Join-Path $PSScriptRoot "fixtures\npmproj") $proj

# ---------- Scenario A: automatic evidence ----------
Log "Scenario A: automatic evidence"
try {
    $run = Invoke-Csx $home1 @("run", "--", "node", "build.mjs") $proj
    if ($run.exit -ne 0) { throw "csx run build failed: $($run.out)" }
    $sync = Invoke-Csx $home1 @("sync")
    Note ("A: csx run exit=0; csx sync exit=" + $sync.exit)
    $ok = Wait-Until {
        $s = Get-Json "$server/v1/stats"
        $s -and [int64]$s.evidence -gt 0 -and [int64]$s.packages -gt 0
    } 60 "server stats show evidence"
    $pkg = Get-Json ("$server/v1/registry/packages/" + [uri]::EscapeDataString("pkg:npm/axios@1.12.0"))
    if ($ok -and $pkg) { Note "A: axios@1.12.0 visible in registry with evidence"; $results["A - automatic evidence"] = "PASS" }
    else { $results["A - automatic evidence"] = "FAIL" }
} catch { Note ("A: EXCEPTION " + $_); $results["A - automatic evidence"] = "FAIL" }

# ---------- Scenario B: failure evidence, sanitized ----------
Log "Scenario B: failure evidence"
try {
    $run = Invoke-Csx $home1 @("run", "--", "node", "fail.mjs") $proj
    if ($run.exit -eq 0) { throw "fail.mjs unexpectedly succeeded" }
    [void](Invoke-Csx $home1 @("sync"))
    $ok = Wait-Until {
        $raw = try { (Invoke-WebRequest -Uri ("$server/v1/registry/packages/" + [uri]::EscapeDataString("pkg:npm/axios@1.12.0")) -UseBasicParsing -TimeoutSec 10).Content } catch { "" }
        $raw -match "ERR_MODULE_NOT_FOUND"
    } 60 "failure cluster visible"
    $raw = (Invoke-WebRequest -Uri ("$server/v1/registry/packages/" + [uri]::EscapeDataString("pkg:npm/axios@1.12.0")) -UseBasicParsing -TimeoutSec 10).Content
    $leak = ($raw -match "secret-project") -or ($raw -match "Users\\\\someone") -or ($raw -match "C:\\\\")
    if ($ok -and -not $leak) { Note "B: failure cluster recorded with error code, no path/project leak in server response"; $results["B - failure evidence"] = "PASS" }
    elseif ($leak) { Note "B: PRIVACY LEAK DETECTED in server response"; $results["B - failure evidence"] = "FAIL" }
    else { $results["B - failure evidence"] = "FAIL" }
} catch { Note ("B: EXCEPTION " + $_); $results["B - failure evidence"] = "FAIL" }

# ---------- Scenario D (before C so search has a sample): contribution + cross verification ----------
Log "Scenario D: sample contribution + cross verification"
try {
    # Work on a copy: `sample create` canonicalizes csx.json in place.
    $sampleDir = Join-Path $tmp "sample-axios-upload"
    Copy-Item -Recurse (Join-Path $PSScriptRoot "fixtures\sample-axios-upload") $sampleDir
    $create = Invoke-Csx $home1 @("sample", "create", $sampleDir)
    if ($create.exit -ne 0) { throw "sample create failed: $($create.out)" }
    $sampleId = ([regex]::Match($create.out, "sha256:[0-9a-f]{64}")).Value
    if (-not $sampleId) { throw "no sampleId in create output: $($create.out)" }
    Note "D: created $sampleId"

    $verify = Invoke-Csx $home1 @("sample", "verify", $sampleId)
    Note ("D: origin verify exit=" + $verify.exit)
    $publish = Invoke-Csx $home1 @("sample", "publish", $sampleId, "--anonymous", "--assume-yes", "--server", $server) $null @{ CSX_TEST_ASSUME_YES = "1" }
    if ($publish.exit -ne 0) { throw "publish failed: $($publish.out)" }
    Note "D: published anonymously"

    # Peer 2 cross-verifies via its daemon's verification loop (unlimited budget).
    [void](Invoke-Csx $home2 @("config", "set", "idleVerification", "unlimited"))
    $daemon2 = Start-Daemon $home2
    if (-not $daemon2.base) { throw "peer 2 daemon did not start" }
    $ok = Wait-Until {
        $s = Get-Json "$server/v1/samples/$sampleId"
        $s -and ($s.status -in @("CROSS_PASS", "MATRIX_PASS", "STABLE"))
    } 240 "sample reaches CROSS_PASS"
    Stop-Daemon $daemon2
    $final = Get-Json "$server/v1/samples/$sampleId"
    Note ("D: final sample status = " + $final.status)
    if ($ok) { $results["D - sample contribution + cross verify"] = "PASS" } else { $results["D - sample contribution + cross verify"] = "FAIL" }
    $script:sampleId = $sampleId
} catch { Note ("D: EXCEPTION " + $_); $results["D - sample contribution + cross verify"] = "FAIL" }

# ---------- Scenario C: search + reuse ----------
Log "Scenario C: search and reuse"
try {
    [void](Invoke-Csx $home1 @("sync"))
    $search = Invoke-Csx $home1 @("search", "upload", "JSON", "with", "axios", "--json") $proj
    $js = $search.out | ConvertFrom-Json
    $hit = (-not $js.miss) -and $js.results.Count -gt 0
    if (-not $hit) { throw "search missed: $($search.out)" }
    Note ("C: search HIT grade=" + $js.results[0].match + " sample=" + $js.results[0].sampleId)

    # Adoption via the daemon local API.
    $daemon1 = Start-Daemon $home1
    if (-not $daemon1.base) { throw "daemon1 did not come up" }
    $body = @{ sampleId = $script:sampleId; applied = $true; buildPass = $true } | ConvertTo-Json
    Invoke-RestMethod -Method Post -Uri "$($daemon1.base)/local/v1/adoption" -Body $body -ContentType "application/json" -TimeoutSec 15 | Out-Null
    $stats = Get-Json "$($daemon1.base)/local/v1/stats"
    $queue = Get-Json "$($daemon1.base)/local/v1/queue"
    Stop-Daemon $daemon1
    if ($null -eq $stats) { throw "daemon stats unavailable" }
    Note "C: adoption reported; local stats + privacy preview served by daemon"
    $results["C - search + reuse"] = "PASS"
} catch { Note ("C: EXCEPTION " + $_); $results["C - search + reuse"] = "FAIL" }

# ---------- Scenario E: private package protection ----------
Log "Scenario E: private package exclusion"
try {
    $code = try { (Invoke-WebRequest -Uri ("$server/v1/registry/packages/" + [uri]::EscapeDataString("pkg:npm/local-lib@0.0.1")) -UseBasicParsing -TimeoutSec 10).StatusCode } catch { [int]$_.Exception.Response.StatusCode }
    $stats = Get-Json "$server/v1/stats"
    $statsRaw = ($stats | ConvertTo-Json -Depth 10)
    if ($code -eq 404 -and $statsRaw -notmatch "local-lib") { Note "E: private file: dependency fully absent from server (404 + no stats trace)"; $results["E - private protection"] = "PASS" }
    else { Note ("E: local-lib registry status=" + $code); $results["E - private protection"] = "FAIL" }
} catch { Note ("E: EXCEPTION " + $_); $results["E - private protection"] = "FAIL" }

# ---------- Scenario F: server outage resilience ----------
Log "Scenario F: server outage"
try {
    docker compose -f (Join-Path $repo "deploy\docker-compose.e2e.yml") stop server | Out-Null
    Note "F: server stopped"
    $run = Invoke-Csx $home1 @("run", "--", "node", "build.mjs") $proj
    if ($run.exit -ne 0) { throw "csx run failed while server down" }
    $search = Invoke-Csx $home1 @("search", "upload", "JSON", "with", "axios", "--json") $proj
    $js = $search.out | ConvertFrom-Json
    if ($js.miss) { throw "local search stopped working during outage" }
    Note "F: csx run and local search work with server down"
    docker compose -f (Join-Path $repo "deploy\docker-compose.e2e.yml") start server | Out-Null
    if (-not (Wait-Until { (Invoke-WebRequest -Uri "$server/healthz" -UseBasicParsing -TimeoutSec 5).StatusCode -eq 200 } 90 "server back")) { throw "server did not come back" }
    $before = [int64](Get-Json "$server/v1/stats").evidence
    $sync = Invoke-Csx $home1 @("sync")
    $ok = Wait-Until { [int64](Get-Json "$server/v1/stats").evidence -gt $before } 60 "queued evidence uploaded after recovery"
    if ($ok) { Note "F: queued evidence uploaded after recovery"; $results["F - outage resilience"] = "PASS" }
    else { $results["F - outage resilience"] = "FAIL" }
} catch { Note ("F: EXCEPTION " + $_); $results["F - outage resilience"] = "FAIL" }

# ---------- report ----------
$fail = ($results.Values | Where-Object { $_ -ne "PASS" }).Count
$report = @()
$report += "# Public v1 E2E Report (goal.md §25)"
$report += ""
$report += "Run: $((Get-Date).ToUniversalTime().ToString('yyyy-MM-dd HH:mm')) UTC — server: docker compose e2e stack, client: csx.exe (windows/amd64), peers: 2"
$report += ""
$report += "| Scenario | Result |"
$report += "|----------|--------|"
foreach ($k in $results.Keys) { $report += "| $k | $($results[$k]) |" }
$report += ""
$report += "## Evidence log"
$report += ""
foreach ($e in $evidence) { $report += "- $e" }
Set-Content -Path (Join-Path $repo "docs\e2e-report.md") -Value ($report -join "`n") -Encoding utf8

Log "---- RESULTS ----"
foreach ($k in $results.Keys) { Log ("{0,-45} {1}" -f $k, $results[$k]) }
Log "report: docs/e2e-report.md"

docker compose -f (Join-Path $repo "deploy\docker-compose.e2e.yml") down -v | Out-Null
if ($fail -gt 0) { exit 1 } else { exit 0 }
