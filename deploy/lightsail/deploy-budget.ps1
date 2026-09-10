# Each phase owns a wall-clock deadline shared by all its commands and sleeps.
# The rollback and cleanup reserves are independent of an exhausted success phase.
$script:deployPhaseWatch = $null
$script:deployPhaseTimings = [ordered]@{}
function Set-DeployPhase([string]$Name, [int]$Seconds) {
    Complete-DeployPhase
    $script:deployPhaseName = $Name
    $script:deployPhaseSeconds = $Seconds
    $script:deployPhaseWatch = [Diagnostics.Stopwatch]::StartNew()
    Write-Host "phase=$Name started ceiling_seconds=$Seconds"
}
function Complete-DeployPhase {
    if ($null -ne $script:deployPhaseWatch) {
        $elapsed = [Math]::Round($script:deployPhaseWatch.Elapsed.TotalSeconds, 3)
        $script:deployPhaseTimings[$script:deployPhaseName] = $elapsed
        Write-Host "phase=$($script:deployPhaseName) elapsed_seconds=$elapsed ceiling_seconds=$($script:deployPhaseSeconds)"
    }
    $script:deployPhaseWatch = $null
}
function Get-DeployBudgetSeconds([int]$CommandSeconds = 60) {
    $remaining = [Math]::Floor($script:deployPhaseSeconds - $script:deployPhaseWatch.Elapsed.TotalSeconds)
    if ($remaining -le 6) { throw "deployment phase $($script:deployPhaseName) exhausted its wall-clock budget" }
    # Five seconds for termination/reaping plus one for output draining.
    return [int][Math]::Min($CommandSeconds, $remaining - 6)
}
function Wait-DeployProcess([Diagnostics.Process]$Process, [int]$Seconds) {
    if (-not $Process.WaitForExit($Seconds * 1000)) {
        try { $Process.Kill($true) } catch { try { $Process.Kill() } catch { } }
        [void]$Process.WaitForExit(5000)
        throw "deployment phase $($script:deployPhaseName): command exceeded ${Seconds}s"
    }
}
function Invoke-DeployProcess([string]$File, [string[]]$Arguments, [int]$Seconds = 60) {
    $limit = Get-DeployBudgetSeconds $Seconds
    $psi = [Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = $File
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    foreach ($arg in $Arguments) { [void]$psi.ArgumentList.Add($arg) }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $psi
    $watch = [Diagnostics.Stopwatch]::StartNew()
    try {
        if (-not $process.Start()) { throw "could not start $File" }
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        Wait-DeployProcess $process $limit
        if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdout, $stderr), 1000)) {
            throw "command output did not close within its bound"
        }
        if ($process.ExitCode -ne 0) {
            $detail = ($stdout.Result + "`n" + $stderr.Result).Trim()
            if ($detail.Length -gt 4096) { $detail = $detail.Substring($detail.Length - 4096) }
            throw "command failed ($($process.ExitCode)): $File`n$detail"
        }
        if ($stdout.Result) { return @($stdout.Result.TrimEnd() -split "`r?`n") }
    } finally {
        Write-Host "phase=$($script:deployPhaseName) command=$([IO.Path]::GetFileName($File)) elapsed_seconds=$([Math]::Round($watch.Elapsed.TotalSeconds, 3)) ceiling_seconds=$limit"
        $process.Dispose()
    }
}
