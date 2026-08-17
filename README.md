# CodeSampleX

> **Stop solving the same code twice.**

<p align="center">
  <img src="internal/web/static/inspector-hero-v1.webp" alt="CodeSampleX compatibility inspector" width="560">
</p>

**Languages:** [English](README.md) · [한국어](docs/i18n/README.ko.md) · [日本語](docs/i18n/README.ja.md) · [简体中文](docs/i18n/README.zh-CN.md) · [Español](docs/i18n/README.es.md) · [Français](docs/i18n/README.fr.md) · [Deutsch](docs/i18n/README.de.md) · [Português (BR)](docs/i18n/README.pt-BR.md) · [Русский](docs/i18n/README.ru.md)

CodeSampleX is a **local-first distributed reasoning cache** for coding LLMs. Instead of every agent on Earth re-deriving how a public library works — and re-hitting the same version incompatibilities — CodeSampleX collects anonymous compatibility **Evidence** from real development environments and serves **verified minimal Samples** with the exact delta between a known-good answer and your project.

- Website & Compatibility Explorer: **https://codesamplex.dev**
- One question your LLM stops re-answering: *does `axios.post` actually work on axios 1.12 + Node 22 + pnpm + Windows 11 — and if not, at which stage does it break?*
- Works with **Claude Code, Codex, Gemini CLI, OpenCode** — and any MCP client (Cursor, Windsurf, Cline, Zed, VS Code).

## Live network

[Records](https://codesamplex.dev/records) · [Findings](https://codesamplex.dev/findings) · [Wanted](https://codesamplex.dev/wanted) · [Contribute](https://codesamplex.dev/contribute)

The public counters are a five-minute rollup, available as JSON without an account:

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

Those counters describe network data and protocol activity, not an audience estimate:

| Field | What it counts |
|-------|----------------|
| `verifiedSamples` | distinct samples with a sandbox contract-PASS receipt |
| `evidence` | accepted observation records; not users, projects, executions, or independently verified samples |
| `packages` / `symbols` | public package names and observed symbols represented in the compatibility data |
| `peers` | distinct anonymous daily peer buckets that contributed evidence |
| `projectsMonth` | distinct anonymous monthly project buckets that contributed evidence |
| `postHitBuildsReported` | adoption reports that included a measured PASS or FAIL after using a sample |

CodeSampleX does **not yet measure reliable unique/active users, live MCP processes, or successful installs**. HTTP requests and release/binary download responses can be counted separately, but retries, automation, mirrors, CI, and deployment traffic mean those numbers are not people or completed installs. Any `estimated*` field in the stats response is explicitly formula-based and must not be read as a measured count.

### Demand-led coverage

In community mode, a `NO_SAFE_MATCH` contributes a privacy-safe Wanted tuple instead of the user's prompt: public package, exact version and, when the request contains one unambiguous package, each retained requested symbol within the bounded report. A multi-package v1 request stays package-only rather than inventing which symbol belongs to which dependency.

[Wanted](https://codesamplex.dev/wanted) ranks the unanswered tuples by demand, and the production queue takes them before broad hot-package expansion. A Wanted row closes only when a live, non-quarantined sample has a contract-PASS receipt and contains the exact canonical package version and requested symbol; a source-only upload or a sample for another release/API is not treated as the answer. `GET /v1/wanted` exposes the same privacy-safe actionable queue for contributors.

Package URLs can also render an explicit **Wanted-only** page before the first verified sample exists. That page reports demand and missing coverage only: it does not manufacture a compatibility result, version matrix, or evidence claim.

### Inspecting recorded environments

[Records](https://codesamplex.dev/records) can be filtered by ecosystem, recorded operating system, recorded runtime/execution context, and evidence basis (`observed` or `verified`). [Findings](https://codesamplex.dev/findings) uses the same environment filters and separates finding source (`official`, `common belief`, or `sample contract`). Environment filters match recorded fingerprints only; CodeSampleX does not infer an OS or runtime from the package ecosystem. Sample detail pages show the declared environment alongside verification-run environments, sandbox capability, per-stage results, and receipt-backed verification level.

The public deployment is **seeded-only for sample source**: official samples are clean-room projects whose provenance can be established. Search, evidence submission, verification receipts, the wanted board, and `NO_SAFE_MATCH` requests remain open without an account. See [Contribute](https://codesamplex.dev/contribute) for the paths that are open to everyone.

## Install

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

That line needs `curl` and CA certificates, which minimal images (debian-slim, alpine, most agent containers) do not have — and `curl … | sh` **exits 0 when curl is missing**, because a pipeline reports the last command's status, not curl's. So install the prerequisites first, or skip curl entirely; the installer falls back to wget once it is running:

```bash
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

The binary lands in `~/.local/bin`, which is on nobody's `PATH` by default. The installer prints this once and nothing repeats it, so the next command you run is `csx: not found` unless you do:

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # the install check — `csx --version` is not a spelling csx accepts
```

One binary, one question. `csx init` shows the community contract and asks a single choice — **JOIN COMMUNITY** or **LOCAL ONLY**. Everything else (daemon, MCP registration for Claude Code / Codex / Gemini CLI / OpenCode, agent rules) is automatic.

Piped into `sh`, that question cannot be asked: stdin is the pipe, `init` reads EOF, prints *"No answer received (input is not a terminal), so nothing will be shared"* and picks **LOCAL ONLY**. Either answer it up front with `csx init --community` / `csx init --local-only` (both re-runnable, both non-interactive), or download and run instead of piping — which also makes a failed download a failed command:

```bash
curl -fsSL https://codesamplex.dev/install.sh -o install.sh && sh install.sh
```

Installing it as an MCP server from an agent, a script, or a directory listing: **[llms-install.md](llms-install.md)** — exact ordered steps for macOS, Linux and Windows, including a no-pipe binary download and an MCP handshake check.

For scripted or CI setups: `csx init --community --yes --no-agents` does config + identity only and writes nothing outside `CSX_HOME` (default `~/.csx`); agent config paths otherwise honor `CSX_AGENT_HOME` when you need them somewhere other than your OS user home.

## The contract

```text
You get                              You contribute
✓ Public compatibility knowledge     ✓ Public package/version usage
✓ Verified code answers              ✓ Public API/symbol usage when detectable
✓ Local agent integration            ✓ Build/typecheck/test result
✓ Public sample cache                ✓ Sanitized failure fingerprints

Never shared automatically
✕ Source code        ✕ Repository/project name   ✕ File names or paths
✕ Source snippets    ✕ Secrets or env variables  ✕ Private packages
✕ Raw compiler/runtime logs
```

This is not hidden telemetry — it is the protocol. Community peers are consumers **and** producers. Local-only mode never sends anything. The privacy preview in `csx ui` shows the exact payloads before they leave your machine.

## How it works

```text
you build/test through csx (or your agent does)
→ local analysis: public packages, lockfile-resolved versions, symbols, environment
→ raw errors sanitized locally into fingerprints (paths/names/secrets stripped)
→ anonymous evidence batches → Compatibility Graph on codesamplex.dev
→ your LLM asks CSX first: nearest verified Sample + environment delta
→ it reasons about the DELTA, not the whole problem
```

Four layers, kept honestly separate:

| Layer | What it is | Trust |
|-------|------------|-------|
| Evidence Network | anonymous package/version/symbol/env/stage/result facts | weak→strong, class-labeled |
| Compatibility Graph | aggregated probabilistic map per environment (incl. execution context: Node/Chrome/Safari/Electron/…) | derived view |
| Sample Pool | user-approved, clean-room, content-addressed minimal projects | contract-verified, cross-verified |
| Agent Delivery | MCP/CLI: nearest sample + delta + known failures | graded EXACT→NO_SAFE_MATCH |

A project compiling is never presented as a symbol working. Unknown causes stay `UNKNOWN`. A wrong HIT is worse than a MISS — `NO_SAFE_MATCH` is a feature.

Verification receipts now have two wire versions. Legacy v1 receipts remain readable, but only a signed **v2 receipt** can carry `resolvedPackages`: canonical package URLs read after a successful resolve from what the verifier actually installed. Missing or ambiguous provenance produces no version claim. The server rejects unsorted, non-canonical, undeclared, cross-ecosystem, or resolve-without-PASS claims, and compatibility snapshots file each receipt under the version that actually ran rather than the version an author typed into a manifest.

## Agent integration (MCP)

**Configured automatically by `csx init`:** Claude Code · Codex · Gemini CLI · OpenCode.

**Any other MCP client** — Cursor, Windsurf, Cline, Zed, VS Code — works too; `csx` is a standard stdio MCP server. Run this and paste what it prints:

```
csx mcp-config          # JSON for Cursor, Cline, Windsurf, Zed, VS Code
csx mcp-config --toml   # TOML for Codex
```

It prints the **absolute path** of your install, which is the part that matters: the install script puts `csx` in `~/.local/bin`, and an MCP client is not started from a login shell — it inherits whatever environment its editor had. A bare `{"command": "csx"}` therefore fails even after you have fixed your own `PATH`. Run it *after* the `export PATH` above, or call it by full path.

The server itself is `csx mcp` — stdio, one JSON-RPC message per line, no daemon required first. `mcp-config` emits it as `args`, but a client that asks for command and arguments in separate fields wants exactly: command = that absolute path, args = `["mcp"]`.

Model-agnostic: the same compatibility evidence serves Claude, GPT and Codex, Gemini, Llama — any model that can call an MCP tool.

Clients that install [MCPB](https://github.com/anthropics/mcpb) bundles can use `codesamplex-mcp.mcpb` from the [latest release](https://github.com/r2cuerdame/CodeSampleX/releases/latest) instead. It carries one binary per platform (darwin-arm64, linux-amd64, windows-amd64).

If you will not pipe a script into a shell — or you are on an architecture the bundle omits — take the binary directly: the same release publishes `csx-{linux,darwin}-{amd64,arm64}`, `csx-windows-{amd64,arm64}.exe` and `SHA256SUMS.txt`, and `https://codesamplex.dev/dl/csx-<os>-<arch>` serves the same file. It is statically linked, so it runs on musl/alpine with no glibc. Copy-pasteable download + checksum + `chmod` steps are in [llms-install.md](llms-install.md).

Tools: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats`. Uploading sample source is deliberately **not** an MCP capability. `propose_public_sample` creates only a sanitized clean-room brief; an authorized seeder still has to use the CLI, review the complete preview, and type an explicit approval.

```bash
csx sync                   # warm the shard cache — once, right after install
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

`csx sync` is not optional garnish. A fresh install has zero shards cached, so every search returns `NO_SAFE_MATCH` until it syncs — indistinguishable, if you skip this, from a network that knows nothing. A long-running `csx daemon` re-warms in the background; a one-shot install calls `sync` once.

## Ecosystems (Public v1)

**Scanned and verified** — your project is detected, its packages resolved from the lockfile, and samples are verified end to end: Node/TypeScript (npm, pnpm, yarn — reference), Python (pip, uv), Go, Rust/Cargo. Node samples are verified on the runtime they declare, so Bun and Deno results are real rather than assumed.

**Verified only** — no project scanner yet, but published samples are built and contract-tested in a pinned container, so a compatibility answer for these ecosystems is as trustworthy as any other: PHP/Composer, Ruby/Bundler, Dart/pub, Elixir/Hex.

Honest capability matrix: [docs/adapters.md](docs/adapters.md) — no adapter claims runtime symbol instrumentation in v1, and symbol resolution confidence is always labeled (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Architecture

Single Go binary (`csx`: daemon + CLI + MCP + peer node + verifier) and a small server (`csx-server`: PostgreSQL + server-rendered explorer behind Caddy). Samples are content-addressed (`sha256`) and distributed local-cache-first → peers → main seeder. Their case identity is derived from the claim itself; stale or hand-copied `caseId` values are refused.

Downloaded samples never run on your host directly. Resolution runs in a pinned sandbox with install scripts disabled where the ecosystem supports it; the immutable artifact is re-hashed after resolve; compile and contract stages run network-off. The resulting ed25519-signed v2 receipt covers the stage verdicts, environment, logs digest, and any package versions the resolver could actually establish. Compatibility aggregation keeps receipt/package sets scoped together, so one run cannot be flattened into evidence for a version or dependency set it never executed. See [goal.md](goal.md) (product spec), [docs/execution-context.md](docs/execution-context.md), [docs/operations.md](docs/operations.md).

## Building from source

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## License

Code: Apache-2.0. Published samples default to **MIT-0**.
