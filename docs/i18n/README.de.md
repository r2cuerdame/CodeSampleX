# CodeSampleX

> **Löse denselben Code nie zweimal.** (Stop solving the same code twice.)

**Sprachen:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX ist ein **local-first verteilter Reasoning-Cache** für Coding-LLMs. Statt dass jeder Agent auf der Welt neu herleitet, wie eine öffentliche Library funktioniert — und dabei immer wieder auf dieselben Versionsinkompatibilitäten läuft — sammelt CodeSampleX anonyme Kompatibilitäts-**Evidence** aus echten Entwicklungsumgebungen und liefert **verifizierte minimale Samples** mit dem exakten Delta zwischen einer bekannt funktionierenden Antwort und deinem Projekt.

- Website & Compatibility Explorer: **https://codesamplex.dev**
- Eine Frage, die dein LLM nicht mehr ständig neu beantworten muss: *funktioniert `axios.post` tatsächlich auf axios 1.12 + Node 22 + pnpm + Windows 11 — und falls nicht, in welcher Phase bricht es?*

## Installation

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Ein Binary, eine Frage. `csx init` zeigt den Community-Vertrag und stellt eine einzige Wahl — **JOIN COMMUNITY** oder **LOCAL ONLY**. Alles Weitere (Daemon, MCP-Registrierung für Claude Code / Codex / Gemini CLI / OpenCode, Agent-Regeln) läuft automatisch.

## Der Vertrag

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

Das ist keine versteckte Telemetrie — es ist das Protokoll. Community-Peers sind Konsumenten **und** Produzenten. Der Local-only-Modus sendet niemals etwas. Die Privacy-Vorschau in `csx ui` zeigt die exakten Payloads, bevor sie deinen Rechner verlassen.

## Wie es funktioniert

```text
you build/test through csx (or your agent does)
→ local analysis: public packages, lockfile-resolved versions, symbols, environment
→ raw errors sanitized locally into fingerprints (paths/names/secrets stripped)
→ anonymous evidence batches → Compatibility Graph on codesamplex.dev
→ your LLM asks CSX first: nearest verified Sample + environment delta
→ it reasons about the DELTA, not the whole problem
```

Vier Schichten, sauber getrennt gehalten:

| Schicht | Was sie ist | Vertrauen |
|-------|------------|-------|
| Evidence Network | anonyme Fakten zu Package/Version/Symbol/Umgebung/Phase/Ergebnis | schwach→stark, klassen-gelabelt |
| Compatibility Graph | aggregierte probabilistische Karte pro Umgebung (inkl. Execution Context: Node/Chrome/Safari/Electron/…) | abgeleitete Sicht |
| Sample Pool | nutzerfreigegebene, Clean-Room-, content-addressed minimale Projekte | contract-verifiziert, kreuz-verifiziert |
| Agent Delivery | MCP/CLI: nächstliegendes Sample + Delta + bekannte Fehlschläge | abgestuft EXACT→NO_SAFE_MATCH |

Dass ein Projekt kompiliert, wird nie als funktionierendes Symbol ausgegeben. Unbekannte Ursachen bleiben `UNKNOWN`. Ein falscher HIT ist schlimmer als ein MISS — `NO_SAFE_MATCH` ist ein Feature.

## Agent-Integration (MCP)

`csx init` registriert den MCP-Server automatisch. Tools: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats`. Das Veröffentlichen eines Samples ist bewusst **keine** MCP-Fähigkeit — es erfordert deine explizite CLI-Freigabe nach einer vollständigen Vorschau.

```bash
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

## Ökosysteme (Public v1)

Node/TypeScript (npm, pnpm, yarn — Referenz), Python (pip, uv), Go, Rust/Cargo. Ehrliche Fähigkeitsmatrix: [docs/adapters.md](../adapters.md) — kein Adapter behauptet in v1 Runtime-Symbol-Instrumentierung, und die Konfidenz der Symbolauflösung ist immer gekennzeichnet (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Architektur

Ein einzelnes Go-Binary (`csx`: Daemon + CLI + MCP + Peer-Node + Verifier) und ein kleiner Server (`csx-server`: PostgreSQL + server-gerenderter Explorer hinter Caddy). Samples sind content-addressed (`sha256`) und werden local-cache-first → Peers → Haupt-Seeder verteilt. Heruntergeladene Samples laufen nie direkt auf deinem Host — Auflösung mit `--ignore-scripts`, Kompilierung und Contract-Lauf ohne Netzwerk in einer Sandbox, Receipts sind ed25519-signiert. Siehe [goal.md](../../goal.md) (Produktspezifikation), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md).

## Aus dem Quellcode bauen

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## Lizenz

Code: Apache-2.0. Veröffentlichte Samples stehen standardmäßig unter **MIT-0**.
