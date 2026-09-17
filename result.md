# Issue #90 Result: R2C-103 re-verified on released artifacts, clean Windows

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/90
- Branch: `issue/90-r2c-107-r2c-103-release-artifact`
- Related: R2C-103 commits `7fb1705` (v0.1.45, payload gets
  `CREATE_NO_WINDOW`), `9dcf378` (v0.1.88, pipe-on-both-ends test),
  `d61ea24` (v0.1.93, launcher hides its own console window); #70 (Defender
  false positive tracking)

## Verdict

The released launcher **does not meet the headline acceptance criterion on a
current Windows 11 desktop**. When the MCP host owns no console -- the
Claude Desktop / VS Code / spawned-agent shape, which is the shape R2C-103
was opened for -- `csx mcp` from the installed v0.1.197 *and* v0.1.198
artifacts opens a **Windows Terminal window titled with the launcher's own
path and keeps it open for the whole MCP session** (7.5 s of a 7.5 s watch,
159-161 of 170 polls, four runs out of four). The window closes when the
session ends. Everything else holds: no window from a console-owning host
(Codex / Claude Code in a terminal), the direct CLI inherits the terminal,
stdio and exit codes pass through unchanged, cleanup is correct, and the
PowerShell 5.1 capture regression that motivated `d61ea24` is not back.

The mechanism is visible in the measurements. Under Windows Terminal
delegation (the inbox default on this build; `HKCU\Console\%%Startup` has no
`Delegation*` values), `GetConsoleWindow()` returns a
`PseudoConsoleWindow`-class window owned by csx.exe. `d61ea24`'s
`ShowWindow(SW_HIDE)` does hide that -- the pseudo window is seen for one
poll and gone -- but the Terminal window (`CASCADIA_HOSTING_WINDOW_CLASS`,
owned by WindowsTerminal.exe) is a different window and stays. The fix was
measured against conhost, where the two are the same window. Fixing this is
launcher work plus a release and is outside this issue; see "What is left".

| Acceptance | Result |
| --- | --- |
| Install a current signed release artifact on a clean/representative Windows environment | PASS. `scripts/windows-bootstrap-smoke.ps1` against production installed the stable channel (v0.1.197) into an isolated profile (`LOCALAPPDATA`/`APPDATA`/`USERPROFILE`/`CSX_HOME` all scratch); the same script with `-DistDir` installed the v0.1.198 assets downloaded from the GitHub release, hashes matching `SHA256SUMS.txt`. Both printed `clean Windows bootstrap passed`. The Release runs' own `Clean Windows signed bootstrap` jobs on `windows-latest` also passed (below). |
| Start CSX MCP from multiple supported clients/sessions | PASS for the shapes that could be run: consoleless host over pipes (harness), console-owning host over pipes (harness, the Codex shape), Claude Code 2.1.274 health check of the release launcher (`claude mcp get`, `Status: Connected`), interactive `csx daemon run` from a terminal. Every session -- nine harness runs and two Claude Code health checks -- completed `initialize` + `tools/list` (10 tools) against the release payloads. Codex/agy were not driven live: Codex's spawn shape is the console-owning one and is covered; agy's registration on this machine points elsewhere. |
| No visible Terminal/console window for background MCP stdio | **FAIL** for a consoleless host (4/4 runs, both versions). PASS for a console-owning host (0 new windows, 2 runs) and for Claude Code (0 new windows; the only diff was the host's own Terminal tab retitled to `claude`). |
| Direct CLI invocation still inherits/uses an interactive console normally | PASS. From a visible terminal, `csx daemon run` put both the launcher and the payload in the terminal's console (`GetConsoleProcessList` lists both pids), no new window; `$v = & csx version` under Windows PowerShell 5.1 returned `csx v0.1.197`, exit 0. |
| stdio, exit codes and cleanup remain correct | PASS. `initialize`/`tools/list` answered over the pipes with nothing on stderr; closing stdin ended the session with exit 0 in every run and no csx process survived it; `csx mcp` with stdin closed immediately exits 0 with 0 bytes on stdout; an unknown subcommand exits 2 through the launcher and 2 from the payload directly; killing the launcher of an interactive `csx daemon run` took its payload with it (kill-on-close job). |
| Record release version/artifact/build identity and Defender result | Recorded below. |

## Environment

| Fact | Value |
| --- | --- |
| Workstation | Windows 11 Pro 25H2, build 10.0.26200, interactive session 1 |
| Default terminal | Windows Terminal 1.24.11911.0 (`HKCU\Console\%%Startup` present with no `DelegationConsole`/`DelegationTerminal` values = Windows default) |
| Defender | platform 4.18.26080.3, engine 1.1.26080.3, security intelligence **1.459.252.0** (updated 2026-09-17 05:07Z), real-time protection on |
| Clients | Claude Code 2.1.274 (`claude.exe`, console subsystem), Codex CLI 0.154.0, agy 1.2.5, PowerShell 7.6.6, Windows PowerShell 5.1 |
| Date | 2026-09-17 16:58Z - 17:10Z |

## Artifacts under test

| | v0.1.197 (stable channel, what `install.ps1` delivers today) | v0.1.198 (latest GitHub release) |
| --- | --- | --- |
| Tag commit | `ca6480e9b706c47f756d8916e61d38a580261493` | `2fcce190c34716974f16a483a9e970397d2f3883` |
| Release run | [35132672753](https://github.com/r2cuerdame/CodeSampleX/actions/runs/35132672753): Validate, windows-test, build, defender-scan, sign, Clean Windows signed bootstrap, publish, Roll the farm -- all `success` | [35212038454](https://github.com/r2cuerdame/CodeSampleX/actions/runs/35212038454): same eight jobs, all `success`; defender-scan printed `CLEAN` for all four Windows binaries at 2026-09-17T11:05Z |
| Stable manifest sequence | 35132672753 (served by production `/dl/csx-update-stable.json`, production `/version` = v0.1.197) | 35212038454 (from the release's `csx-update-stable.json`) |
| Launcher `csx.exe` sha256 | `c7755b888add9be595bf4b8a12b97409ec3a881c27d6cb2f21d24abe068a2d30` | `a9e740c4b55d8079d83b1b6f24c684c1c1de9a0e488ddb25a36c35d26c49607a` (= `csx-launcher-windows-amd64.exe` in `SHA256SUMS.txt`) |
| Payload `csx-payload.exe` sha256 | `d7a35caddc1f07da8ead38f5b06ff3e028a00785ca7d22d5298297e1d334ab20` | `e73082b431357e5dc58dfa72ef0368384535e69e383f5e818940f7a220a2536f` (= `csx-windows-amd64.exe`) |
| `csx version` through the launcher | `csx v0.1.197`, exit 0 | `csx v0.1.198`, exit 0 |
| MCP `serverInfo.version` | `v0.1.197` | `v0.1.198` |
| Launcher source | both contain `d61ea24` (first tag v0.1.93) and the R2C-103 payload fix `7fb1705` (v0.1.45); `cmd/csx-launcher/run_windows.go` last changed by `facef1b` (#186) | |

Isolated installs were configured `mode: community`, `autoUpdate: off`,
`clientClass: operator` (excluded from public install counts), daemon ports
48711/48712 so nothing reached the workstation's real install (daemon on
48619, still v0.1.179, untouched). Each install's own daemon was spawned by
the MCP session, ran without a window, and was stopped with `csx daemon
stop` (exit 0) afterwards.

## Method

`scripts/windows-mcp-console-evidence.ps1` (new, in this PR) drives an
installed launcher through the process shapes the fix is about and records
what Windows did, as JSON:

- `host-consoleless`: the script calls `FreeConsole()` on itself (verified:
  `GetConsoleWindow()==0`, `GetConsoleProcessList` empty) and then starts
  `csx mcp` with stdin/stdout/stderr redirected to pipes and **without**
  `CREATE_NO_WINDOW`, which is what a host that owns no console does. It
  polls `EnumWindows` every ~40 ms for 8 s (1.5 s before the first protocol
  byte, then `initialize`, `notifications/initialized`, `tools/list`, then
  the rest), keyed by hwnd so a baseline window that merely changes title is
  reported separately from a genuinely new window. Then it closes stdin,
  waits for exit, and checks for surviving descendants and windows.
- `control`: identical, but spawning `csx-payload.exe mcp` directly -- the
  pre-fix shape. It must produce a window, or the detector proves nothing.
- `host-console`: run inside a visible terminal (`Start-Process pwsh`); the
  same MCP session, plus `GetConsoleProcessList` membership of the launcher
  and payload, then the interactive `csx daemon run` / kill / PowerShell 5.1
  capture checks.
- `watch`: run a real client command (`claude mcp get csx-release-198`,
  registered with `claude mcp add -s local` in a scratch project, removed
  afterwards) under the same detector.

Raw outputs are in `docs/evidence/90/` with the user profile path scrubbed
to `%USERPROFILE%`.

## The failure in detail

`docs/evidence/90/v0.1.197-host-consoleless.json` (17:04:03Z) -- new windows
while the session ran:

| Window | Owner | First seen | Visible |
| --- | --- | --- | --- |
| `CASCADIA_HOSTING_WINDOW_CLASS` "Terminal" | WindowsTerminal.exe | +0.10 s after spawn | 108 ms, then retitled to |
| `CASCADIA_HOSTING_WINDOW_CLASS` `…\local\csx\csx.exe` (same hwnd) | WindowsTerminal.exe | +0.25 s | **7456 ms, 159/170 polls, until the launcher exited** |
| `PseudoConsoleWindow` "" | csx.exe (the launcher, pid 39996) | +3.7 s | 4024 ms in this run; one poll in the others |

`v0.1.198-host-consoleless.json` (17:01:39Z): same Terminal window, titled
with the v0.1.198 launcher path, 7504 ms / 160 polls; the launcher's
`PseudoConsoleWindow` seen for exactly one poll (the `SW_HIDE` landing).
This run used the earlier detector keyed by the whole window string rather
than by hwnd; the window still counts as new because it first appeared as
"Terminal" and no baseline window ever carried that title. A re-run with
the final detector was not possible: Defender took the v0.1.198 payload at
17:08Z (below). The earlier v0.1.197 runs at 16:58Z and 16:59Z showed the
same window for 7551 ms.

`v0.1.197-control-payload-direct.json` (17:04:15Z): the payload spawned
directly opens the same kind of Terminal window titled with the payload
path (7495 ms / 159 polls) and its `PseudoConsoleWindow` stays visible the
whole time -- the R2C-103 symptom as originally reported, so the detector
sees what a user sees. The launcher-spawned payload never showed a window of
its own in any run (`CREATE_NO_WINDOW` from `7fb1705` holds; its conhost is
windowless).

Why the release does this: `hideOwnConsoleWindow` (`d61ea24`) asks
`GetConsoleWindow()` for the launcher's console window and hides it. With
conhost hosting the console that is the on-screen window. With Windows
Terminal as the default terminal, conhost runs headless behind a
pseudoconsole and the on-screen window belongs to WindowsTerminal.exe;
`GetConsoleWindow()` returns the `PseudoConsoleWindow` stub, `SW_HIDE` hides
the stub (measured: it disappears after one poll) and the Terminal window is
untouched. The comment in `run_windows.go` records measurements of
`GetConsoleProcessList` and a cmd window, i.e. a conhost desktop; Windows 11
has shipped Windows Terminal as the default since 22H2.

Hypothesis for the follow-up, **not verified**: a launcher that owns its
console alone (`GetConsoleProcessList == 1`, the existing test) should
`FreeConsole()` instead of `ShowWindow(SW_HIDE)`. A console whose last
process detaches is destroyed and Windows Terminal closes its window; the
launcher's stdio are the host's pipes and are unaffected, and the payload
already gets `CREATE_NO_WINDOW` when the launcher has no console. Whether
the Terminal window still flashes for the ~100 ms it takes to appear is the
open question. An experimental launcher with exactly that change was built
from HEAD via `go build -overlay` to measure it, and Defender quarantined
it (and the payload copy it executed) before the first session -- see the
Defender table. The measurement was therefore **not run**; no Defender
setting was changed.

## Defender result

All scans with `MpCmdRun.exe -Scan -ScanType 3 -File <path>
-DisableRemediation`, security intelligence 1.459.252.0 throughout.

| Time (UTC) | File | Verdict |
| --- | --- | --- |
| 2026-09-17 ~16:56 | v0.1.197 launcher `c7755b88…`, v0.1.197 payload `d7a35cad…`, v0.1.198 launcher `a9e740c4…`, v0.1.198 payload `e73082b4…`, v0.1.198 arm64 launcher `6372cf2b…`, v0.1.198 arm64 payload `a63c08e4…` | `found no threats`, exit 0, all six |
| 16:58 - 17:05 | v0.1.198 payload executed by its release launcher | ran three full MCP sessions and its daemon; no detection |
| 17:06:32 | **self-built experimental launcher** (plain `go build`, not the release) executes a **copy** of the v0.1.198 payload | `Trojan:Win32/Bearfoos.A!ml` (ThreatID 2147731250) on the payload copy and on the launcher's `.csx-rehydrate-*.exe` re-download; process named in the detection = the experimental launcher |
| 17:06:39 | the experimental launcher itself | `Trojan:Win32/Bearfoos.B!ml` (2147731849), blocked |
| 17:08:06 | the **release** v0.1.198 launcher (`a9e740c4…`, untouched scratch install) tries to run its **release** payload `e73082b4…` for a fifth session | payload now `Bearfoos.A!ml`, quarantined; launcher exited 126 with `payload-unreadable`, its automatic repair from the official release was blocked at the staged-binary self-test, and it printed the #70 false-positive notes |
| 17:09 | the downloaded `csx-windows-amd64.exe` (v0.1.198, same bytes) rescanned | `found 1 threats`, exit 2 -- the same bytes that scanned clean 13 minutes earlier |
| 17:09 | v0.1.197 payload `d7a35cad…` rescanned | `found no threats`; the v0.1.197 install kept working through the last host-console run at 17:07:50Z-17:08:05Z |

So: the published binaries were clean under the definitions of the day when
this started, matching the release run's `defender-scan`; the v0.1.198
payload's verdict flipped mid-session at the moment an unsigned self-built
launcher executed a copy of it, and stayed flipped for the same bytes at
every path afterwards. Whether the self-built launcher caused the cloud
verdict on the payload or only coincided with it cannot be decided from
here; what is certain is the sequence and that v0.1.198's payload bytes are
now blocked on this machine while production still serves v0.1.197. That is
worth knowing before the stable channel is promoted to v0.1.198 (#70).

## Cleanup and isolation

- The workstation's real install (`%LOCALAPPDATA%\csx`, v0.1.179, daemon
  48619), user PATH, and agent registrations were not touched;
  `windows-bootstrap-smoke.ps1` asserts the PATH part itself.
- Scratch daemons on 48711 and 48712 were stopped (`csx daemon stop`, exit
  0); no process from the scratch installs remains.
- The Claude Code local-scope registration `csx-release-198` in the scratch
  project was removed with `claude mcp remove -s local`.
- Scratch profiles stay under `%TEMP%\csx-bootstrap-smoke-*` and
  `%TEMP%\csx-issue90-*` as evidence, as the smoke script intends.

## Validation

On this branch, Windows workstation, 2026-09-17:

- `go test ./scripts/ -run TestWindowsMCPConsoleEvidenceScriptParses -v`:
  PASS (the new harness parses under Windows PowerShell and documents every
  mode it accepts).
- `go vet ./scripts/`: clean. `gofmt -l` does not list the new test.
- The harness ran end to end in all four modes (9 runs) as recorded above.

## What is left

Not in this issue's scope and needs a Chief split:

1. **Launcher**: hide (or never create) the Windows Terminal window when the
   launcher owns its console alone. Candidate: `FreeConsole()` in
   `hideOwnConsoleWindow`; measure the flash with
   `scripts/windows-mcp-console-evidence.ps1 -Mode host-consoleless` on a
   Windows Terminal desktop, then ship it in a release and re-run this
   harness against the released launcher. The CI `windows-test` job cannot
   see this: `windows-latest` has no interactive desktop and the existing
   `TestLauncherConsoleProbeHelper` asks the payload about its own window,
   which is the half of the fix that works.
2. **#70**: the v0.1.198 payload is now a Defender hit on this machine under
   1.459.252.0; check before promoting the stable channel past v0.1.197.
