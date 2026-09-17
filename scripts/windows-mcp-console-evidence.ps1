# Released-artifact evidence for R2C-103 / GitHub #90: an MCP host that owns
# no console must not get a Terminal window from `csx mcp`, and a person in
# a terminal must keep theirs.
#
# This drives an INSTALLED launcher (the csx.exe that install.ps1 wrote, plus
# the payload it points at) through the exact process shapes the fix is
# about, and records what Windows actually did: every visible top-level
# window that appeared while the session ran, which process owned it, and
# whether the payload was attached to the host's console or to one of its
# own. Nothing here is a unit test; it needs a desktop session and it runs
# release bytes, not source.
#
# Modes (one per invocation, JSON on stdout):
#   host-consoleless  The Claude Desktop / VS Code / spawned-agent shape: this
#                     process drops its console (FreeConsole) and spawns
#                     `csx mcp` over pipes, the way a host that owns no
#                     console does. A console-subsystem child of such a
#                     parent gets a brand-new console and a window with it
#                     unless someone hides it. Expected: no new window.
#   host-console      The Codex / claude-in-a-terminal shape: this process
#                     owns a visible console and spawns `csx mcp` over pipes.
#                     Expected: no new window, and the launcher and payload
#                     both attached to THIS console (GetConsoleProcessList).
#                     Also proves the direct-CLI contract: `csx daemon run`
#                     started unredirected joins this console, and killing the
#                     launcher takes the payload with it (kill-on-close job).
#   control           host-consoleless, but spawning the PAYLOAD directly
#                     instead of the launcher. That is the pre-fix shape and
#                     it is expected to open a window; if it does not, the
#                     detector proves nothing and the run says so.
#   watch             Run a real client command (-Command 'claude mcp get
#                     csx-release', space-separated) from this console and
#                     report every window that appeared until it exited.
#                     -Launcher/-CsxHome only identify the install under test.
#
# -Launcher is the installed csx.exe; -CsxHome is the CSX_HOME it should use.
# Run host-console from a real terminal, or open one for it:
#   Start-Process pwsh -ArgumentList '-NoProfile','-File',<this>,'-Mode','host-console',...
param(
    [Parameter(Mandatory)][ValidateSet('host-consoleless', 'host-console', 'control', 'watch')][string]$Mode,
    [Parameter(Mandatory)][string]$Launcher,
    [Parameter(Mandatory)][string]$CsxHome,
    [string]$OutFile = '',
    [int]$WatchSeconds = 6,
    [string]$Command = ''
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public static class CsxWinProbe {
    delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);
    [DllImport("user32.dll")] static extern bool EnumWindows(EnumWindowsProc cb, IntPtr lParam);
    [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr h);
    [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern int GetClassName(IntPtr h, StringBuilder s, int n);
    [DllImport("kernel32.dll")] public static extern IntPtr GetConsoleWindow();
    [DllImport("kernel32.dll")] public static extern bool FreeConsole();
    [DllImport("kernel32.dll")] static extern uint GetConsoleProcessList(uint[] list, uint count);
    public static bool WindowVisible(IntPtr h) { return IsWindowVisible(h); }
    // Every visible top-level window as "hwnd|pid|class|title".
    public static string[] VisibleWindows() {
        var found = new List<string>();
        EnumWindows((h, l) => {
            if (!IsWindowVisible(h)) return true;
            uint pid; GetWindowThreadProcessId(h, out pid);
            var cls = new StringBuilder(256); GetClassName(h, cls, 256);
            var title = new StringBuilder(512); GetWindowText(h, title, 512);
            found.Add(h.ToInt64() + "|" + pid + "|" + cls + "|" + title.ToString().Replace("|", "/"));
            return true;
        }, IntPtr.Zero);
        return found.ToArray();
    }
    // Pids attached to this process's console (0 entries when it has none).
    public static uint[] ConsolePids() {
        var buf = new uint[256];
        uint n = GetConsoleProcessList(buf, 256);
        if (n > 256) n = 256;
        var outp = new uint[n]; Array.Copy(buf, outp, n); return outp;
    }
}
'@

function Get-ProcTable {
    $t = @{}
    foreach ($p in Get-CimInstance Win32_Process) { $t[[uint32]$p.ProcessId] = $p }
    return $t
}
function Get-Descendants([hashtable]$table, [uint32]$root) {
    $out = @(); $queue = @($root)
    while ($queue.Count) {
        $cur = $queue[0]; $queue = @($queue | Select-Object -Skip 1)
        foreach ($p in $table.Values) {
            if ([uint32]$p.ParentProcessId -eq $cur -and [uint32]$p.ProcessId -ne $cur -and $p.ProcessId -notin $out) {
                $out += [uint32]$p.ProcessId; $queue += [uint32]$p.ProcessId
            }
        }
    }
    return $out
}
function Describe-Window([string]$w, [hashtable]$table) {
    $retitled = $w.StartsWith('retitled:')
    if ($retitled) { $w = $w.Substring(9) }
    $parts = $w -split '\|', 4
    $pid_ = [uint32]$parts[1]
    $proc = $table[$pid_]
    $name = if ($proc) { $proc.Name } else { (Get-Process -Id $pid_ -ErrorAction SilentlyContinue).ProcessName }
    $parent = if ($proc) { $table[[uint32]$proc.ParentProcessId] } else { $null }
    [pscustomobject]@{
        hwnd = $parts[0]; pid = $pid_; process = $name
        parent = $(if ($parent) { "$($parent.Name) $($parent.ProcessId)" } else { '' })
        class = $parts[2]; title = $parts[3]
        kind = $(if ($retitled) { 'baseline window retitled' } else { 'NEW window' })
    }
}
function Watch-Windows([string[]]$baseline, [hashtable]$seen, [double]$seconds) {
    # Poll fast: the launcher hides its own window with ShowWindow(SW_HIDE),
    # so a window that existed for one frame is still a finding.
    $baselineHwnds = @($baseline | ForEach-Object { ($_ -split '\|', 2)[0] })
    $deadline = [DateTime]::UtcNow.AddSeconds($seconds)
    $polls = 0
    while ([DateTime]::UtcNow -lt $deadline) {
        $now = [DateTime]::UtcNow
        foreach ($w in [CsxWinProbe]::VisibleWindows()) {
            if ($baseline -contains $w) { continue }
            # A window that was already there and merely changed its title
            # (a client naming its console "claude") is not a new window.
            $hwnd = ($w -split '\|', 2)[0]
            $key = if ($baselineHwnds -contains $hwnd) { "retitled:$w" } else { $w }
            if (-not $seen.ContainsKey($key)) { $seen[$key] = @{ first = $now; last = $now; polls = 0 } }
            $seen[$key].last = $now; $seen[$key].polls++
        }
        $polls++
        Start-Sleep -Milliseconds 40
    }
    return $polls
}
function Start-Piped([string]$exe, [string[]]$argv, [string]$csxHome) {
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $exe
    foreach ($a in $argv) { $psi.ArgumentList.Add($a) }
    $psi.UseShellExecute = $false
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    # Deliberately NOT CreateNoWindow: an MCP host does not set it either.
    $psi.CreateNoWindow = $false
    $psi.Environment['CSX_HOME'] = $csxHome
    $p = New-Object System.Diagnostics.Process
    $p.StartInfo = $psi
    [void]$p.Start()
    return $p
}
function Read-Line-Timeout([System.Diagnostics.Process]$p, [int]$ms) {
    $task = $p.StandardOutput.ReadLineAsync()
    if ($task.Wait($ms)) { return $task.Result }
    return $null
}
function Invoke-McpSession([System.Diagnostics.Process]$p) {
    $r = [ordered]@{}
    $init = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"issue-90-evidence","version":"0"}}}'
    $p.StandardInput.WriteLine($init); $p.StandardInput.Flush()
    $line = Read-Line-Timeout $p 20000
    $r.initializeResponse = $line
    $r.initializeOk = [bool]($line -and $line -match '"serverInfo"')
    $p.StandardInput.WriteLine('{"jsonrpc":"2.0","method":"notifications/initialized"}'); $p.StandardInput.Flush()
    $p.StandardInput.WriteLine('{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'); $p.StandardInput.Flush()
    $line = Read-Line-Timeout $p 20000
    $r.toolsListOk = [bool]($line -and $line -match '"tools"')
    $r.toolCount = if ($line) { ([regex]::Matches($line, '"inputSchema"')).Count } else { 0 }
    return $r
}

$result = [ordered]@{
    mode = $Mode
    launcher = $Launcher
    launcherSHA256 = (Get-FileHash -LiteralPath $Launcher -Algorithm SHA256).Hash.ToLowerInvariant()
    home = $CsxHome
    startedAt = [DateTime]::UtcNow.ToString('o')
    hostPid = $PID
    hostConsoleWindowBefore = [CsxWinProbe]::GetConsoleWindow().ToInt64()
}
$active = Get-Content -LiteralPath (Join-Path (Split-Path $Launcher) 'active.json') -Raw | ConvertFrom-Json
$payload = Join-Path (Split-Path $Launcher) "payloads/$($active.current.version)/csx-payload.exe"
$result.payloadVersion = $active.current.version
$result.payload = $payload
$result.payloadSHA256 = (Get-FileHash -LiteralPath $payload -Algorithm SHA256).Hash.ToLowerInvariant()

if ($Mode -eq 'watch') {
    if (-not $Command) { throw 'watch needs -Command' }
    $words = @($Command -split ' +')
    $baseline = [CsxWinProbe]::VisibleWindows()
    $result.hostConsolePids = @([CsxWinProbe]::ConsolePids()).Count
    $result.command = $Command
    $exe = (Get-Command $words[0]).Source
    $proc = Start-Piped $exe @($words | Select-Object -Skip 1) $CsxHome
    $proc.StandardInput.Close()
    $outTask = $proc.StandardOutput.ReadToEndAsync(); $errTask = $proc.StandardError.ReadToEndAsync()
    $seen = @{}; $polls = 0
    while (-not $proc.HasExited) { $polls += Watch-Windows $baseline $seen 0.2 }
    $table = Get-ProcTable
    $result.exitCode = $proc.ExitCode
    $result.stdout = $outTask.Result.Trim(); $result.stderr = $errTask.Result.Trim()
    $result.windowWatch = [ordered]@{
        polls = $polls
        newVisibleWindows = @($seen.Keys | ForEach-Object {
            $d = Describe-Window $_ $table
            $d | Add-Member -NotePropertyName firstSeen -NotePropertyValue $seen[$_].first.ToString('o')
            $d | Add-Member -NotePropertyName visibleMs -NotePropertyValue ([int]($seen[$_].last - $seen[$_].first).TotalMilliseconds) -PassThru
        })
    }
    $result.finishedAt = [DateTime]::UtcNow.ToString('o')
    $json = $result | ConvertTo-Json -Depth 6
    if ($OutFile) { [IO.File]::WriteAllText($OutFile, $json, (New-Object Text.UTF8Encoding($false))) }
    $json
    return
}

if ($Mode -ne 'host-console') {
    # Become the host that owns no console. Output already goes to a pipe or
    # file, so nothing is lost; from here on this process looks like a GUI
    # app to every child it creates.
    [void][CsxWinProbe]::FreeConsole()
    $result.hostConsoleWindowAfterFree = [CsxWinProbe]::GetConsoleWindow().ToInt64()
    $result.hostConsolePidsAfterFree = @([CsxWinProbe]::ConsolePids()).Count
    if ($result.hostConsoleWindowAfterFree -ne 0 -or $result.hostConsolePidsAfterFree -ne 0) {
        throw "host still owns a console after FreeConsole; the consoleless shape was not reproduced"
    }
} else {
    $own = [CsxWinProbe]::GetConsoleWindow()
    $result.hostConsoleVisible = [CsxWinProbe]::WindowVisible($own)
    if ($own -eq [IntPtr]::Zero -or -not $result.hostConsoleVisible) {
        throw "host-console needs a visible console; start this from a terminal or via Start-Process pwsh"
    }
}

$exe = if ($Mode -eq 'control') { $payload } else { $Launcher }
$baseline = [CsxWinProbe]::VisibleWindows()
$result.baselineVisibleWindows = $baseline.Count

$proc = Start-Piped $exe @('mcp') $CsxHome
$result.spawnedExe = $exe
$result.spawnedPid = $proc.Id
$stderrTask = $proc.StandardError.ReadToEndAsync()

# Watch first, talk second: the window, if any, is created while the child
# initialises, before it has read a byte of protocol.
$seen = @{}
$polls = Watch-Windows $baseline $seen 1.5
$session = Invoke-McpSession $proc
$polls += Watch-Windows $baseline $seen ($WatchSeconds - 1.5)
$result.mcp = $session
$table = Get-ProcTable
$desc = Get-Descendants $table ([uint32]$proc.Id)
$result.descendantsDuringSession = @($desc | ForEach-Object { $p = $table[$_]; "$($p.Name) $($p.ProcessId) parent=$($p.ParentProcessId)" })
$daemonProcs = @($table.Values | Where-Object { $_.CommandLine -match 'daemon run' -and $_.CommandLine -match [regex]::Escape((Split-Path $Launcher)) })
$result.daemonProcessesForThisInstall = @($daemonProcs | ForEach-Object { "$($_.Name) $($_.ProcessId) parent=$($_.ParentProcessId)" })
$result.windowWatch = [ordered]@{
    polls = $polls
    newVisibleWindows = @($seen.Keys | ForEach-Object {
        $d = Describe-Window $_ $table
        $d | Add-Member -NotePropertyName firstSeen -NotePropertyValue $seen[$_].first.ToString('o')
        $d | Add-Member -NotePropertyName lastSeen -NotePropertyValue $seen[$_].last.ToString('o')
        $d | Add-Member -NotePropertyName visibleMs -NotePropertyValue ([int]($seen[$_].last - $seen[$_].first).TotalMilliseconds)
        $d | Add-Member -NotePropertyName visiblePolls -NotePropertyValue $seen[$_].polls -PassThru
    })
}
if ($Mode -eq 'host-console') {
    $attached = [CsxWinProbe]::ConsolePids()
    $result.hostConsolePidsDuringSession = @($attached)
    $result.launcherAttachedToHostConsole = $attached -contains [uint32]$proc.Id
    $result.payloadAttachedToHostConsole = [bool](@($desc | Where-Object { $attached -contains $_ -and $table[$_].Name -eq 'csx-payload.exe' }).Count)
}

# The host closes stdin; the server must exit on its own and take the
# payload with it.
$proc.StandardInput.Close()
$exited = $proc.WaitForExit(15000)
$result.exitedAfterStdinClose = $exited
if (-not $exited) { $proc.Kill($true) }
$result.exitCode = $proc.ExitCode
$result.stderr = $stderrTask.Result
Start-Sleep -Milliseconds 700
$after = Get-ProcTable
$result.descendantsAliveAfterExit = @($desc | Where-Object { $after.ContainsKey($_) -and $after[$_].CommandLine -notmatch 'daemon run' } | ForEach-Object { "$($after[$_].Name) $_" })
$baselineHwnds = @($baseline | ForEach-Object { ($_ -split '\|', 2)[0] })
$result.visibleWindowsAfterExit = @([CsxWinProbe]::VisibleWindows() | Where-Object { $baselineHwnds -notcontains ($_ -split '\|', 2)[0] } | ForEach-Object { Describe-Window $_ $after })

if ($Mode -eq 'host-console') {
    # Direct CLI contract: an interactive command joins this console and its
    # payload dies with the launcher.
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $Launcher; $psi.ArgumentList.Add('daemon'); $psi.ArgumentList.Add('run')
    $psi.UseShellExecute = $false; $psi.Environment['CSX_HOME'] = (Join-Path $CsxHome 'cli-console-home')
    New-Item -ItemType Directory -Force $psi.Environment['CSX_HOME'] | Out-Null
    $cfgSrc = Join-Path $CsxHome 'config.json'
    if (Test-Path $cfgSrc) { Copy-Item $cfgSrc (Join-Path $psi.Environment['CSX_HOME'] 'config.json') }
    $cli = [System.Diagnostics.Process]::Start($psi)
    Start-Sleep -Seconds 3
    $t2 = Get-ProcTable
    $d2 = Get-Descendants $t2 ([uint32]$cli.Id)
    $attached2 = [CsxWinProbe]::ConsolePids()
    $result.cli = [ordered]@{
        command = 'csx daemon run (unredirected, interactive)'
        launcherPid = $cli.Id
        launcherAttachedToHostConsole = $attached2 -contains [uint32]$cli.Id
        payloadPids = @($d2 | Where-Object { $t2[$_].Name -eq 'csx-payload.exe' })
        payloadAttachedToHostConsole = [bool](@($d2 | Where-Object { $attached2 -contains $_ -and $t2[$_].Name -eq 'csx-payload.exe' }).Count)
        newVisibleWindows = @([CsxWinProbe]::VisibleWindows() | Where-Object { $baselineHwnds -notcontains ($_ -split '\|', 2)[0] } | ForEach-Object { Describe-Window $_ $t2 })
    }
    $cli.Kill()
    [void]$cli.WaitForExit(5000)
    Start-Sleep -Milliseconds 1500
    $t3 = Get-ProcTable
    $result.cli.payloadAliveAfterLauncherKilled = @($d2 | Where-Object { $t3.ContainsKey($_) } | ForEach-Object { "$($t3[$_].Name) $_" })
    # The PowerShell 5.1 regression that the windowsgui build caused: a
    # captured `csx version` must not come back empty.
    $ps51 = & powershell.exe -NoProfile -NonInteractive -Command "`$env:CSX_HOME='$CsxHome'; `$v = & '$Launcher' version; `$v; exit `$LASTEXITCODE"
    $result.cli.powershell51CapturedVersion = "$ps51"
    $result.cli.powershell51ExitCode = $LASTEXITCODE
}

# Exit codes travel through the launcher unchanged. Spawned the same way a
# host would (pipes), because after FreeConsole this shell has no console
# for `&` to hand a native command.
function Run-Captured([string]$exe, [string[]]$argv) {
    $p = Start-Piped $exe $argv $CsxHome
    $p.StandardInput.Close()
    $out = $p.StandardOutput.ReadToEnd(); $err = $p.StandardError.ReadToEnd()
    $p.WaitForExit()
    return [pscustomobject]@{ stdout = $out.Trim(); stderr = $err.Trim(); exitCode = $p.ExitCode }
}
$v = Run-Captured $Launcher @('version')
$result.versionOutput = $v.stdout; $result.versionExit = $v.exitCode
$result.unknownCommandExitViaLauncher = (Run-Captured $Launcher @('definitely-not-a-command')).exitCode
$result.unknownCommandExitViaPayload = (Run-Captured $payload @('definitely-not-a-command')).exitCode
$e = Run-Captured $Launcher @('mcp')
$result.mcpWithStdinClosedImmediately = [ordered]@{ exitCode = $e.exitCode; stdoutBytes = $e.stdout.Length }
$result.finishedAt = [DateTime]::UtcNow.ToString('o')

$json = $result | ConvertTo-Json -Depth 6
if ($OutFile) { [IO.File]::WriteAllText($OutFile, $json, (New-Object Text.UTF8Encoding($false))) }
$json
