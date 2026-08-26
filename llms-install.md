# Installing CodeSampleX (`csx`) — instructions for an AI agent

This file is written for an agent installing the `csx` MCP server unattended.
A human-readable overview is in [README.md](README.md).

`csx` is a single static binary. Installing it means three things:

1. the binary is on disk,
2. the MCP client points at it **by absolute path**,
3. the local compatibility cache has been warmed once (`csx sync`).

Skipping (2) is the mistake almost everyone makes. Skipping (3) makes the
first search return `NO_SAFE_MATCH` no matter what the network knows.

---

## First, which of the two install paths you are on

They are separate, and mixing them is where the confusion starts. Steps 1–5
below are the **CLI path**. Take the MCPB path instead only if your client
installs `.mcpb` bundles.

| | **CLI install** (steps 1–5) | **MCPB bundle** |
|---|---|---|
| What you install | one binary, at `~/.local/bin/csx` or `%LOCALAPPDATA%\csx\csx.exe` | `codesamplex-mcp.mcpb` from a GitHub release, through the client's own installer |
| Who writes the client config | you do, from `csx mcp-config` — **absolute path** | the client does, from the bundle's `mcp_config`, which uses `${__dirname}/server/<binary>` and needs no PATH |
| Serves | every stdio MCP client, plus the CLI itself (`csx run`, `csx search`, `csx sample`) | that one MCP client. The bundle carries no CLI on your PATH. |
| `csx init` | run it (step 2) — it sets the mode and registers detected agents | not run for you. The server reports mode `uninitialized`, uploads nothing, and answers from an empty cache until a mode is set. |
| Updates | signed, automatic, `csx update rollback` available | owned by the client that installed it; use that client's package-update flow. The binary inside a bundle never self-modifies. |
| Privacy policy | [PRIVACY.md](PRIVACY.md) | same document; the manifest's `privacy_policies` array points at it |

There is no third path. A repository-local `.mcp.json` is **not** an install
recipe: this repository deliberately carries none, because a checked-in
`{"command": "csx"}` is exactly the entry the next section measures as broken.

---

## Read this before you write any config

An MCP client is not started from a login shell. It inherits whatever
environment its editor or GUI process had, which normally does **not**
include `~/.local/bin` — the directory the installer puts `csx` in. So this
config fails even on a machine where `csx` works fine in the user's terminal:

```json
{ "mcpServers": { "csx": { "command": "csx", "args": ["mcp"] } } }
```

Measured in a clean container, with the binary installed and working:

```
$ env -i /bin/sh -c "csx mcp"
/bin/sh: csx: not found
```

Always use the absolute path:

```json
{ "mcpServers": { "csx": { "command": "/root/.local/bin/csx", "args": ["mcp"] } } }
```

Step 3 below prints that object for you, with the path this machine actually
installed to. Do not hand-write it.

---

## Step 1 — install the binary

**macOS / Linux**

```sh
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Check `curl` and CA certificates first. Container images you are likely to be
running in do not have them: `debian:13-slim` and `alpine:3.22` both lack
curl, and a curl without `ca-certificates` fails TLS with `curl: (77)`. In
both cases **the pipeline still exits 0** — the exit status of `a | b` is
`b`'s, and `sh` succeeds at reading empty stdin — so an agent that checks the
exit code concludes the install worked while nothing was installed. Either
install the prerequisites, or drop curl from the bootstrap entirely; the
script itself uses wget when curl is absent:

```sh
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

Installs to `$CSX_INSTALL_DIR`, default `~/.local/bin/csx`, then runs `csx
init`, which asks one question. Piped like this, stdin is the consumed
download pipe, so nobody can answer it. `init` therefore takes its advertised
default, community mode. Run step 2 explicitly anyway so the intended mode is
visible and reproducible.

Nothing adds `~/.local/bin` to `PATH`. The installer prints the export line
once, at install time, and no later command repeats it. Until you run it, the
next command in this file fails with `sh: csx: not found` (exit 127):

```sh
export PATH="$HOME/.local/bin:$PATH"
csx version    # prints the installed release, e.g. `csx v0.1.7`; `csx --version` exits 2
```

Every step below calls `csx` by absolute path so it works either way.

**Windows (PowerShell)**

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

Installs to `%LOCALAPPDATA%\csx\csx.exe` and adds that directory to the user
PATH (new terminals only — see the warning above about MCP clients).

**No-prompt alternative (any POSIX host)** — download the binary directly and
skip the installer's interactive tail. Use this one if your policy forbids
piping a downloaded script into a shell; it is a complete install path, not a
fallback:

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case "$arch" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; esac
mkdir -p "$HOME/.local/bin"
curl -fsSL "https://codesamplex.dev/dl/csx-$os-$arch" -o "$HOME/.local/bin/csx"
chmod +x "$HOME/.local/bin/csx"
"$HOME/.local/bin/csx" version
```

Available: `csx-linux-amd64`, `csx-linux-arm64`, `csx-darwin-amd64`,
`csx-darwin-arm64`, `csx-windows-amd64.exe`, `csx-windows-arm64.exe`. The same
files, plus `SHA256SUMS.txt`, are assets on every GitHub release, which is
where to get them if you want to check the download:

```sh
rel=https://github.com/r2cuerdame/CodeSampleX/releases/latest/download
curl -fsSL "$rel/csx-linux-amd64" -o csx
curl -fsSL "$rel/SHA256SUMS.txt" -o SHA256SUMS.txt
grep ' csx-linux-amd64$' SHA256SUMS.txt | sed 's/ .*/  csx/' | sha256sum -c -
install -m 0755 csx "$HOME/.local/bin/csx"
```

The binary is statically linked (`CGO_ENABLED=0` Go), so it runs on musl —
alpine, distroless and scratch images need no glibc shim.

A third source, if all you have is the MCPB bundle: `codesamplex-mcp.mcpb` is
a zip. `unzip codesamplex-mcp.mcpb` yields `manifest.json` and
`server/csx-darwin-arm64`, `server/csx-linux-amd64`,
`server/csx-windows-amd64.exe` — the same binaries, needing `chmod +x`. Its
sha256 is published as `codesamplex-mcp.mcpb.sha256`.

## Step 2 — set the mode (community is the default)

`csx init` asks exactly one question: join the community evidence network, or
stay local-only. Community is the default, including when stdin is unavailable.
Pass the mode explicitly so an automated installation is self-documenting:

```sh
"$HOME/.local/bin/csx" init --community  --yes    # default: network benefits + anonymous evidence
"$HOME/.local/bin/csx" init --local-only --yes    # explicit opt-out: project evidence stays local
```

```powershell
& "$env:LOCALAPPDATA\csx\csx.exe" init --community --yes
```

If the user has not said which they want, choose `--community`, the product
default. A user who explicitly requests local-only must be given
`--local-only`. Both choices are re-runnable. Evidence upload is gated on
`mode == community`; an install that never ran `init` has mode `""` and
uploads nothing.

`init` also registers `csx` with any of these agents whose config directory
already exists, writing the absolute path for you:

| Agent      | detected by                | MCP entry written to               | usage rule written to               |
|------------|----------------------------|------------------------------------|-------------------------------------|
| Claude Code| `~/.claude/`               | `~/.claude.json`                   | `~/.claude/CLAUDE.md`               |
| Codex      | `~/.codex/`                | `~/.codex/config.toml`             | `~/.codex/AGENTS.md`                |
| Gemini CLI | `~/.gemini/`               | `~/.gemini/settings.json`          | `~/.gemini/GEMINI.md`               |
| OpenCode   | `~/.config/opencode/`      | `~/.config/opencode/opencode.json` | `~/.config/opencode/AGENTS.md`      |

Edits are marker-fenced and idempotent; re-run `csx init` after installing one
of these agents. `--no-agents` skips this entirely and writes nothing outside
`CSX_HOME` (default `~/.csx`); `CSX_AGENT_HOME` redirects it.

In a clean container none of those directories exist, so `init` prints
`skipped (not detected)` for all four. That is the expected result of a
correct install, not a symptom — step 3 covers registering the server by hand.

## Step 3 — register the server with any other MCP client

For Cursor, Cline, Windsurf, Zed, VS Code and anything else that speaks stdio
MCP, print the entry and paste it into that client's server config:

```sh
"$HOME/.local/bin/csx" mcp-config          # JSON: {"mcpServers": {"csx": {...}}}
"$HOME/.local/bin/csx" mcp-config --toml   # TOML: [mcp_servers.csx]
"$HOME/.local/bin/csx" mcp-config --path   # just the absolute path
```

Example output (paths differ per machine — use what your run prints):

```json
{
  "mcpServers": {
    "csx": {
      "args": [
        "mcp"
      ],
      "command": "/root/.local/bin/csx"
    }
  }
}
```

`mcp-config` was introduced in `v0.1.3`. The installer serves the current
stable release; do not infer its version from this document. Binaries that
predate the command (`v0.1.0`, `v0.1.2`)
exit 2 with `csx: unknown command "mcp-config"`. This form produces the same
object on every version, so it is safe to run unconditionally:

```sh
CSX_BIN="${CSX_INSTALL_DIR:-$HOME/.local/bin}/csx"
"$CSX_BIN" mcp-config 2>/dev/null || printf '{\n  "mcpServers": {\n    "csx": {\n      "command": "%s",\n      "args": ["mcp"]\n    }\n  }\n}\n' "$CSX_BIN"
```

```powershell
$csx = "$env:LOCALAPPDATA\csx\csx.exe"
$cfg = & $csx mcp-config
if ($LASTEXITCODE -ne 0) {
    $cfg = @{ mcpServers = @{ csx = @{ command = $csx; args = @('mcp') } } } | ConvertTo-Json -Depth 5
}
$cfg
```

On the fallback branch the old binary also prints its usage text to stderr
before the JSON appears; ignore it, only stdout is captured.

If the client takes a command and arguments in a form and not a file, the two
fields are: command = the absolute path above, args = `["mcp"]`.

`csx mcp` resolves its state directory from `CSX_HOME`, falling back to
`$HOME/.csx`. If your client launches servers with a stripped environment
(no `HOME`), it exits with `csx mcp: config: resolve home: $HOME is not
defined`; add `"env": {"CSX_HOME": "/absolute/path/.csx"}` to the entry.

## Step 4 — warm the compatibility cache

```sh
"$HOME/.local/bin/csx" sync
```

Expected: `warmed shard keys: <n>` with n well above zero (111 and 116 on two
clean installs the same day — the number tracks what the network currently
publishes), `uploaded batches: 0`.

**Community mode only.** In `local-only` mode — and before `csx init` has run
at all — `csx sync` is a complete no-op: it downloads no shards, makes no
server request of any kind, and reports `warmed shard keys: 0`. That is the
local-only contract, not a failure, and it is held there by
`internal/daemon/localonly_test.go`. A local-only install therefore answers
from an empty cache until it is given one another way.

This is not optional. Measured on a fresh install, before `sync`, every
search misses and says so:

```
MATCH: NO_SAFE_MATCH
This install has no compatibility shards cached yet, so the miss above says
nothing about what the network knows. Run `csx sync` ...
```

After `sync`, the same query returns `MATCH: EXACT` with a sample id. A
long-running `csx daemon` re-warms in the background; a one-shot install
should call `sync` once here.

## Step 5 — verify the install (do not skip)

Pipe an `initialize` request into the server and check the response. Nothing
else has to be running: `csx mcp` is a standalone stdio process, needs no
daemon, and answers even on an install where `init` was never run (it reports
mode as uninitialized and uploads nothing — it does not silently default to
community). Framing is newline-delimited JSON, one message per line, not
`Content-Length` headers.

**macOS / Linux**

```sh
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"install-check","version":"0"}}}' \
  | "$HOME/.local/bin/csx" mcp
```

**Windows (PowerShell)**

```powershell
'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"install-check","version":"0"}}}' |
  & "$env:LOCALAPPDATA\csx\csx.exe" mcp
```

Expected — one line of JSON on stdout:

```json
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2025-06-18","serverInfo":{"name":"codesamplex","version":"<installed-version>"}}}
```

`version` mirrors whatever binary you installed, so do not match on it. The
server answers `protocolVersion` `2025-06-18` whatever you requested — asking
for `2024-11-05` and getting `2025-06-18` back is a normal negotiation, not a
failure.

**Pass criterion:** the response contains `"name":"codesamplex"`. As a check
you can branch on:

```sh
if printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"install-check","version":"0"}}}' \
  | "$HOME/.local/bin/csx" mcp | grep -q '"name":"codesamplex"'; then
  echo "PASS: csx MCP server responds"
else
  echo "FAIL: no MCP response"
fi
```

Optional stronger check — `tools/list` must return these ten tool names:

```sh
printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | "$HOME/.local/bin/csx" mcp
```

`search_known_solution`, `get_sample`, `explain_compatibility`,
`run_observed_command`, `report_sample_adoption`, `report_anomaly`,
`report_csx_issue`, `propose_public_sample`, `list_local_hits`,
`get_local_stats`. There is deliberately no publish tool: publishing a sample
requires explicit CLI approval by the human.

`report_anomaly` is for one situation only: a CSX answer and a **measured**
local run concretely disagree. It files a verification request, not a bug
report — an independent re-run decides, and nothing is confirmed by reporting
it. A submission with no local PASS/FAIL behind it is refused.

`report_csx_issue` is the same idea aimed at CodeSampleX itself rather than at
a package. It is opt-in: nothing instructs an agent to call it after a
failure, no ticket is created, and a week with no reports is normal.

Then restart the MCP client so it picks up the new server entry.

## Signed automatic updates

**Migration:** binaries through v0.1.11 cannot update themselves because they
do not contain this updater or its trust root. Rerun the official installer one
final time. Its `csx update adopt` step creates the standalone ownership marker;
after that, signed updates can proceed automatically. The installer verifies
`SHA256SUMS.txt` to catch partial or corrupt transfer, but the checksum is served
from the same HTTPS origin as the binary. Initial installation therefore still
trusts the install-script/download origin; the signed updater is the independent
trust boundary only for releases after bootstrap.

The first-party installers register the installed absolute path as a
standalone CodeSampleX binary. Community installs then check the **stable**
channel about every six hours, with per-installation jitter and persisted
exponential backoff. Every update manifest is Ed25519-signed by the release
key embedded in the installed binary; the client also verifies the selected
OS/architecture, exact release URL, byte length and SHA-256 before a staged
binary is allowed to replace the stable path. A partial download, bad
signature, replayed sequence or failed staged-binary self-test leaves the old
binary untouched.

On Windows, the migration installer puts a stable `csx.exe` launcher at the
public path and immutable payloads under `payloads/vMAJOR.MINOR.PATCH/`. A
verified update writes the new payload completely, then atomically flips one
`active.json` document containing current and previous descriptors. The launcher
forwards arguments, stdio, environment and exit status without adding protocol
output. Existing MCP sessions keep their current payload; the next client or
worker restart selects the new one. A manifest requiring a newer launcher
protocol fails closed and asks for the official installer; payload updates never
self-modify the launcher.

Useful commands:

```sh
csx update check       # verify the signed manifest, but do not install
csx update             # verify, install atomically, preserve csx.previous
csx update status      # version, last/next check, restart state
csx update rollback    # restore the preserved previous binary
csx config set autoUpdate off   # opt out
csx config set autoUpdate auto  # default: automatic only in community mode
```

`local-only` and uninitialized installations make no automatic update request;
running `csx update` yourself is an explicit one-shot request. Only the stable
channel is accepted in v1.

The two long-running roles activate safely in different ways:

- A contributor worker finishes every already-admitted verification, admits
  no new job, then exits with status 75. The native per-user service definitions
  below treat that as a failure and immediately start the new stable binary.
- A stdio MCP server belongs to the editor/client that launched it. It installs
  the verified replacement on disk but never kills the active JSON-RPC session.
  It emits a restart-required update notice; restart the MCP client/editor to
  activate the new build.

An MCPB is owned by the MCP client that installed it and is not self-modified.
Use that client's Registry/package update flow instead. The preserved previous
payload provides bounded manual rollback. The Windows launcher keeps both
descriptors in one crash-consistent pointer, but deliberately does not guess
that a newly activated payload is unhealthy and auto-toggle at startup; use
`csx update rollback` explicitly so a deterministic startup failure cannot
become a flip-flop loop.

If an update command reports that `update.lock` already exists, first verify
that no `csx update`, worker, or MCP process is currently checking an update.
Only then remove `$CSX_HOME/update/update.lock` (default
`~/.csx/update/update.lock`) and retry. The v1 lock fails closed after a crash;
it never guesses that an old-looking lock is abandoned and risks two concurrent
replacements. Native advisory locks are a future hardening item.

## Worker-only machine

Use this path for a spare machine that should contribute Docker verification
but must not be connected to an MCP client or inspect a local project. It runs
one contributor process and deliberately skips both agent integration and the
background sync daemon.

This path requires **csx v0.1.7 or later**. `worker start` existed in v0.1.6,
but that release did not yet support `csx init --no-daemon`; combining the
current worker instructions with a v0.1.6 binary fails during initialization.
After installing, verify the boundary before creating a service:

```sh
csx version
csx init --help 2>&1 | grep -- --no-daemon
```

If either check shows an older or unsupported binary, upgrade first. Do not
silently omit `--no-daemon`, because that would leave a second background
process running on a machine intended to be worker-only.

The normal installers run interactive `csx init`. Set `CSX_WORKER_ONLY=1` so
the same first-party installers instead run the exact worker-only initialization:

**macOS / Linux**

```sh
curl -fsSL https://codesamplex.dev/install.sh | CSX_WORKER_ONLY=1 sh
```

**Windows (PowerShell)**

```powershell
$env:CSX_WORKER_ONLY = '1'
try { irm https://codesamplex.dev/install.ps1 | iex }
finally { Remove-Item Env:\CSX_WORKER_ONLY -ErrorAction SilentlyContinue }
```

Both forms run:

```sh
csx init --community --yes --no-agents --no-daemon
```

The result is community mode with an identity, warmed public cache and no MCP
entry, agent rule or background daemon. A worker still requires a reachable
Docker daemon. If Docker is missing, stop: installing it or granting
administrator access is a separate human decision.

The verifier polls server-assigned `cross` and `matrix` jobs. It claims a job
only when every closed requirement (adapter, ecosystem, runtime and exact
runtime version, execution context, sandbox capability, and any declared
browser/framework requirements) maps to an environment this machine's pinned
runner can prepare. A matrix run reuses the downloaded content-addressed
artifact unchanged and overlays only the requested execution environment onto
an in-memory manifest copy. It never edits the artifact or substitutes a
nearby locally available runtime.

Run the worker in the foreground to inspect it:

```sh
/absolute/path/to/csx worker start --mode verify --parallel 2 --budget idle
```

For persistent operation, use exactly one native per-user service. Its command
must use the absolute binary path above, start automatically, restart on
failure, and refuse duplicate instances:

| OS | Native service | Stable name |
|---|---|---|
| Linux | `systemd --user` unit | `csx-worker.service` |
| macOS | LaunchAgent | `dev.codesamplex.worker` |
| Windows | Scheduled Task | `CodeSampleX Contributor Worker` |

The worker's successful update handoff uses exit status **75** after all active
lanes drain. Keep the documented failure-restart settings: they are what turns
that deliberate nonzero exit into activation of the new binary. Repeated exits
from a genuinely broken build remain bounded by the native service manager;
the preserved `.previous` binary is available for manual rollback.

On Linux, write `~/.config/systemd/user/csx-worker.service`, replacing the
executable path with the one printed by the installer:

```ini
[Unit]
Description=CodeSampleX Contributor Worker
After=docker.service

[Service]
ExecStart=/absolute/path/to/csx worker start --mode verify --parallel 2 --budget idle
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
```

Then run `systemctl --user daemon-reload`,
`systemctl --user enable --now csx-worker.service`, and verify with
`systemctl --user is-active csx-worker.service`. Enabling user lingering makes
the service start before login and is a separate system-level choice; do not
enable it without the machine owner's approval.

On macOS, write `~/Library/LaunchAgents/dev.codesamplex.worker.plist`, again
using the installed absolute path:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>dev.codesamplex.worker</string>
  <key>ProgramArguments</key><array>
    <string>/absolute/path/to/csx</string><string>worker</string><string>start</string>
    <string>--mode</string><string>verify</string><string>--parallel</string><string>2</string>
    <string>--budget</string><string>idle</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
</dict></plist>
```

Load it with
`launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.codesamplex.worker.plist`
and verify with `launchctl print gui/$(id -u)/dev.codesamplex.worker`.

On Windows, create the current-user task from PowerShell:

```powershell
$csx = "$env:LOCALAPPDATA\csx\csx.exe"
$action = New-ScheduledTaskAction -Execute $csx -Argument 'worker start --mode verify --parallel 2 --budget idle'
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet -Hidden -MultipleInstances IgnoreNew -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' -Action $action -Trigger $trigger -Settings $settings -Description 'Docker-isolated CodeSampleX verification worker' -Force
Start-ScheduledTask -TaskName 'CodeSampleX Contributor Worker'
Get-ScheduledTask -TaskName 'CodeSampleX Contributor Worker' | Select-Object TaskName, State
```

An empty server queue is healthy. The process waits; it is not a failed
installation. `csx daemon status` should report `not running` on a worker-only
machine because `--no-daemon` intentionally leaves only the contributor worker.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Installer exits 0 but `csx` does not exist; output was `curl: not found` or `curl: (77)` | `curl … \| sh` reports `sh`'s status, not curl's | install `curl` + `ca-certificates`, or use the wget form (step 1) |
| `sh: csx: not found` (exit 127) immediately after a successful install | `~/.local/bin` is not on `PATH` | `export PATH="$HOME/.local/bin:$PATH"`, or call csx by absolute path |
| `csx: unknown command "--version"` (exit 2) | there is no `--version` flag | `csx version` |
| Client reports the server failed to start; nothing in its log but a spawn error | config used bare `csx`; the client's PATH has no `~/.local/bin` | use the absolute path from step 3 |
| `csx: unknown command "mcp-config"` (exit 2) | binary predates the command (`v0.1.0`, `v0.1.2`) | use the fallback snippet in step 3 |
| `flag provided but not defined: -no-daemon` during worker-only setup | binary is v0.1.6 or older | upgrade to v0.1.7 or later, confirm `csx init --help` lists `--no-daemon`, then rerun worker-only initialization |
| `csx mcp: config: resolve home: $HOME is not defined` | client launches servers with a stripped environment | set `CSX_HOME` in the entry's `env` |
| Every search returns `NO_SAFE_MATCH` right after install | shards not cached yet | `csx sync` (step 4) |
| `csx: command not found` in the user's own terminal | `~/.local/bin` not on their PATH | `export PATH="$HOME/.local/bin:$PATH"` in their shell profile |
| Windows: works in a new terminal but not the one used to install | the installer edits the *user* PATH | open a new terminal, or call the exe by full path |

## What the install wrote

| Path | What |
|---|---|
| `~/.local/bin/csx` | macOS/Linux standalone binary |
| `%LOCALAPPDATA%\csx\csx.exe` | Windows stable launcher |
| `%LOCALAPPDATA%\csx\payloads\vMAJOR.MINOR.PATCH\csx-payload.exe` | immutable Windows payload selected by `active.json` |
| `%LOCALAPPDATA%\csx\active.json` | atomic Windows current/previous/rollback-hold descriptors |
| `~/.csx/` (or `$CSX_HOME`) | config.json, identity.json, csx.db, cas/, samples/, logs/ |
| agent config files listed in step 2 | MCP entry + usage rule, marker-fenced |

To uninstall: delete the binary, delete `~/.csx`, and remove the `csx` entry
and the `<!-- csx:begin -->…<!-- csx:end -->` / `# csx:begin…# csx:end` blocks
from the agent files.

## What was verified, and where

The original MCP installation path was run on 2026-08-14 against the then
published `v0.1.3` binary. The worker-only path is a later feature and requires
v0.1.7 or newer as stated above. Version strings in example output are
placeholders; `https://codesamplex.dev/dl/csx-<os>-<arch>` and the latest
GitHub release assets are the current source of truth unless a section states
an explicit historical minimum:

- `docker run --rm alpine:3.22` and `docker run --rm debian:13-slim`, from an
  empty home directory, by agents given nothing but this page and the README.
  The published installer, the missing-`curl` and missing-`ca-certificates`
  failures, the exit-0 masking, the piped `init` default, `mcp-config`,
  `sync`, the handshake, `tools/list` and a live `tools/call` were all
  observed there.
- The no-pipe path was taken to a full handshake on `alpine:3.22` without ever
  piping a script into a shell: release binary and `.mcpb` (opened with
  `unzip`), `chmod +x`, `init --community --yes --no-agents`, `csx mcp`.
- Windows 11: the historical direct-binary `mcp-config` forms and PowerShell
  handshake were observed against `./cmd/csx`. The stable-launcher updater has
  native Windows unit/integration coverage in the release workflow; a published
  installer migration is not claimed until that release is deployed.
- macOS was **not** tested. It takes the same `install.sh` path as Linux,
  and `csx-darwin-amd64` / `csx-darwin-arm64` are published, but no step on
  this page has been executed on a Mac.
- Derived, not executed as written: the `wget -qO-` bootstrap (`install.sh`
  selects wget when curl is absent, so only the outer fetch changes) and the
  `SHA256SUMS.txt` pipeline (the asset is published and the `.mcpb` checksum
  was verified in-container; the sums file itself was not).
