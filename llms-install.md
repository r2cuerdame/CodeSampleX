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

Installs to `$CSX_INSTALL_DIR`, default `~/.local/bin/csx`, then runs `csx
init`, which asks one question. Piped like this, stdin is the consumed curl
pipe, so there is nobody to answer it and the answer depends on the version:
the published `v0.1.0` selects **community** mode, builds from current source
select local-only and print why. Do not rely on either — run step 2, which
sets the mode explicitly.

**Windows (PowerShell)**

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

Installs to `%LOCALAPPDATA%\csx\csx.exe` and adds that directory to the user
PATH (new terminals only — see the warning above about MCP clients).

**No-prompt alternative (any POSIX host)** — download the binary directly and
skip the installer's interactive tail:

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case "$arch" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; esac
mkdir -p "$HOME/.local/bin"
curl -fsSL "https://codesamplex.dev/dl/csx-$os-$arch" -o "$HOME/.local/bin/csx"
chmod +x "$HOME/.local/bin/csx"
```

Available: `csx-linux-amd64`, `csx-linux-arm64`, `csx-darwin-amd64`,
`csx-darwin-arm64`, `csx-windows-amd64.exe`, `csx-windows-arm64.exe`.

## Step 2 — set the mode (a consent decision — do not make it for the user)

`csx init` asks exactly one question: join the community evidence network, or
stay local-only. Answer it explicitly on the command line so the outcome does
not depend on what a piped stdin looked like:

```sh
"$HOME/.local/bin/csx" init --local-only --yes    # nothing ever leaves the machine
"$HOME/.local/bin/csx" init --community  --yes    # ONLY if the user asked to join
```

```powershell
& "$env:LOCALAPPDATA\csx\csx.exe" init --local-only --yes
```

If the user has not said which they want, choose `--local-only`. It is
re-runnable: `csx init --community --yes` later flips the mode, and nothing is
uploaded before that. (Evidence upload is gated on `mode == community`;
an install that never ran `init` has mode `""` and uploads nothing either.)

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
`CSX_HOME`; `CSX_AGENT_HOME` redirects it.

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

`mcp-config` is newer than the published binaries: on `v0.1.0` (what
`https://codesamplex.dev/dl/...` serves today) and on the `v0.1.2` GitHub
release it exits 2 with `csx: unknown command "mcp-config"`. Use this form,
which produces the same object on every version:

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

Pipe an `initialize` request into the server and check the response.

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
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2025-06-18","serverInfo":{"name":"codesamplex","version":"v0.1.0"}}}
```

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

Every command and every output quoted above was run on 2026-08-14, against a
binary built from this repository with `CGO_ENABLED=0 go build ./cmd/csx`
(`csx mcp-config` exists only there so far) unless stated otherwise:

- Linux: `docker run --rm -it alpine:3.22 sh` (`apk add curl`), from an empty
  home directory. The published installer, `init`, agent registration,
  `mcp-config`, `sync`, the handshake and `tools/list` were all run there.
- Windows 11: `mcp-config` in all three forms and the PowerShell handshake.
- The `initialize` handshake above was also run against the published
  `v0.1.0` binary from `https://codesamplex.dev/dl/csx-linux-amd64`, which is
  where the `"version":"v0.1.0"` in the expected output comes from.
- macOS was **not** tested. It takes the same `install.sh` path as Linux,
  and `csx-darwin-amd64` / `csx-darwin-arm64` are published, but no step on
  this page has been executed on a Mac.
