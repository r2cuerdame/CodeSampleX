# CodeSampleX

> **Stop solving the same code twice.**

**Languages:** [English](README.md) · [한국어](docs/i18n/README.ko.md) · [日本語](docs/i18n/README.ja.md) · [简体中文](docs/i18n/README.zh-CN.md) · [Español](docs/i18n/README.es.md) · [Français](docs/i18n/README.fr.md) · [Deutsch](docs/i18n/README.de.md) · [Português (BR)](docs/i18n/README.pt-BR.md) · [Русский](docs/i18n/README.ru.md)

CodeSampleX is a **local-first distributed reasoning cache** for coding LLMs. Instead of every agent on Earth re-deriving how a public library works — and re-hitting the same version incompatibilities — CodeSampleX collects anonymous compatibility **Evidence** from real development environments and serves **verified minimal Samples** with the exact delta between a known-good answer and your project.

- Website & Compatibility Explorer: **https://codesamplex.dev**
- One question your LLM stops re-answering: *does `axios.post` actually work on axios 1.12 + Node 22 + pnpm + Windows 11 — and if not, at which stage does it break?*
- Works with **Claude Code, Codex, Gemini CLI, OpenCode** — and any MCP client (Cursor, Windsurf, Cline, Zed, VS Code).

## Install

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

One binary, one question. `csx init` shows the community contract and asks a single choice — **JOIN COMMUNITY** or **LOCAL ONLY**. Everything else (daemon, MCP registration for Claude Code / Codex / Gemini CLI / OpenCode, agent rules) is automatic.

For scripted or CI setups: `csx init --community --yes --no-agents` does config + identity only and writes nothing outside `CSX_HOME`; agent config paths otherwise honor `CSX_AGENT_HOME` when you need them somewhere other than your OS user home.

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

## Agent integration (MCP)

**Configured automatically by `csx init`:** Claude Code · Codex · Gemini CLI · OpenCode.

**Any other MCP client** — Cursor, Windsurf, Cline, Zed, VS Code — works too; `csx` is a standard stdio MCP server:

```json
{"mcpServers": {"csx": {"command": "csx", "args": ["mcp"]}}}
```

Model-agnostic: the same compatibility evidence serves Claude, GPT and Codex, Gemini, Llama — any model that can call an MCP tool.

Clients that install [MCPB](https://github.com/anthropics/mcpb) bundles can use `codesamplex-mcp.mcpb` from the [latest release](https://github.com/r2cuerdame/CodeSampleX/releases/latest) instead. It carries one binary per platform (darwin-arm64, linux-amd64, windows-amd64); on any other architecture use the install script above.

Tools: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats`. Publishing a sample is deliberately **not** an MCP capability — it requires your explicit CLI approval after a full preview.

```bash
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

## Ecosystems (Public v1)

Node/TypeScript (npm, pnpm, yarn — reference), Python (pip, uv), Go, Rust/Cargo. Honest capability matrix: [docs/adapters.md](docs/adapters.md) — no adapter claims runtime symbol instrumentation in v1, and symbol resolution confidence is always labeled (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Architecture

Single Go binary (`csx`: daemon + CLI + MCP + peer node + verifier) and a small server (`csx-server`: PostgreSQL + server-rendered explorer behind Caddy). Samples are content-addressed (`sha256`) and distributed local-cache-first → peers → main seeder. Downloaded samples never run on your host directly — resolve with `--ignore-scripts`, compile and contract run network-off in a sandbox, receipts are ed25519-signed. See [goal.md](goal.md) (product spec), [docs/execution-context.md](docs/execution-context.md), [docs/operations.md](docs/operations.md).

## Building from source

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## License

Code: Apache-2.0. Published samples default to **MIT-0**.
