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
csx version    # prints e.g. `csx v0.1.3`; `csx --version` exits 2
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

`mcp-config` ships in `v0.1.3` and later, which is what
`https://codesamplex.dev/dl/...` and the latest GitHub release serve today —
verified in a clean container. Binaries that predate it (`v0.1.0`, `v0.1.2`)
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
publishes), `uploaded batches: 0`. Works in local-only mode: warming
downloads shards, it uploads nothing.

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
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2025-06-18","serverInfo":{"name":"codesamplex","version":"v0.1.3"}}}
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

Optional stronger check — `tools/list` must return these eight tool names:

```sh
printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | "$HOME/.local/bin/csx" mcp
```

`search_known_solution`, `get_sample`, `explain_compatibility`,
`run_observed_command`, `report_sample_adoption`, `propose_public_sample`,
`list_local_hits`, `get_local_stats`. There is deliberately no publish tool:
publishing a sample requires explicit CLI approval by the human.

Then restart the MCP client so it picks up the new server entry.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Installer exits 0 but `csx` does not exist; output was `curl: not found` or `curl: (77)` | `curl … \| sh` reports `sh`'s status, not curl's | install `curl` + `ca-certificates`, or use the wget form (step 1) |
| `sh: csx: not found` (exit 127) immediately after a successful install | `~/.local/bin` is not on `PATH` | `export PATH="$HOME/.local/bin:$PATH"`, or call csx by absolute path |
| `csx: unknown command "--version"` (exit 2) | there is no `--version` flag | `csx version` |
| Client reports the server failed to start; nothing in its log but a spawn error | config used bare `csx`; the client's PATH has no `~/.local/bin` | use the absolute path from step 3 |
| `csx: unknown command "mcp-config"` (exit 2) | binary predates the command (`v0.1.0`, `v0.1.2`) | use the fallback snippet in step 3 |
| `csx mcp: config: resolve home: $HOME is not defined` | client launches servers with a stripped environment | set `CSX_HOME` in the entry's `env` |
| Every search returns `NO_SAFE_MATCH` right after install | shards not cached yet | `csx sync` (step 4) |
| `csx: command not found` in the user's own terminal | `~/.local/bin` not on their PATH | `export PATH="$HOME/.local/bin:$PATH"` in their shell profile |
| Windows: works in a new terminal but not the one used to install | the installer edits the *user* PATH | open a new terminal, or call the exe by full path |

## What the install wrote

| Path | What |
|---|---|
| `~/.local/bin/csx` (Windows: `%LOCALAPPDATA%\csx\csx.exe`) | the binary |
| `~/.csx/` (or `$CSX_HOME`) | config.json, identity.json, csx.db, cas/, samples/, logs/ |
| agent config files listed in step 2 | MCP entry + usage rule, marker-fenced |

To uninstall: delete the binary, delete `~/.csx`, and remove the `csx` entry
and the `<!-- csx:begin -->…<!-- csx:end -->` / `# csx:begin…# csx:end` blocks
from the agent files.

## What was verified, and where

Every command and every output quoted above was run on 2026-08-14 against the
published `v0.1.3` binary — `https://codesamplex.dev/dl/csx-<os>-<arch>` and
the GitHub release assets — unless stated otherwise:

- `docker run --rm alpine:3.22` and `docker run --rm debian:13-slim`, from an
  empty home directory, by agents given nothing but this page and the README.
  The published installer, the missing-`curl` and missing-`ca-certificates`
  failures, the exit-0 masking, the piped `init` default, `mcp-config`,
  `sync`, the handshake, `tools/list` and a live `tools/call` were all
  observed there.
- The no-pipe path was taken to a full handshake on `alpine:3.22` without ever
  piping a script into a shell: release binary and `.mcpb` (opened with
  `unzip`), `chmod +x`, `init --community --yes --no-agents`, `csx mcp`.
- Windows 11: `mcp-config` in all three forms and the PowerShell handshake,
  against a binary built from this repository with `CGO_ENABLED=0 go build
  ./cmd/csx`. The container runs above did not cover Windows.
- macOS was **not** tested. It takes the same `install.sh` path as Linux,
  and `csx-darwin-amd64` / `csx-darwin-arm64` are published, but no step on
  this page has been executed on a Mac.
- Derived, not executed as written: the `wget -qO-` bootstrap (`install.sh`
  selects wget when curl is absent, so only the outer fetch changes) and the
  `SHA256SUMS.txt` pipeline (the asset is published and the `.mcpb` checksum
  was verified in-container; the sums file itself was not).
