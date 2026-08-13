# CodeSampleX

> **Deja de resolver el mismo código dos veces.** (Stop solving the same code twice.)

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX es una **caché de razonamiento distribuida y local-first** para LLMs de programación. En lugar de que cada agente del planeta vuelva a deducir cómo funciona una librería pública — y se tope una y otra vez con las mismas incompatibilidades de versión — CodeSampleX recopila **Evidencia** anónima de compatibilidad procedente de entornos de desarrollo reales y sirve **Samples mínimos verificados** con el delta exacto entre una respuesta que se sabe correcta y tu proyecto.

- Sitio web y explorador de compatibilidad: **https://codesamplex.dev**
- Una pregunta que tu LLM deja de responder una y otra vez: *¿funciona realmente `axios.post` con axios 1.12 + Node 22 + pnpm + Windows 11 — y si no, en qué etapa falla?*
- Funciona con **Claude Code, Codex, Gemini CLI, OpenCode** — y con cualquier cliente MCP (Cursor, Windsurf, Cline, Zed, VS Code).

## Instalación

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Un solo binario, una sola pregunta. `csx init` muestra el contrato de la comunidad y pide una única decisión: **JOIN COMMUNITY** o **LOCAL ONLY**. Todo lo demás (daemon, registro MCP para Claude Code / Codex / Gemini CLI / OpenCode, reglas de agente) es automático.

## El contrato

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

Esto no es telemetría oculta: es el protocolo. Los pares de la comunidad son consumidores **y** productores. El modo solo-local nunca envía nada. La vista previa de privacidad en `csx ui` muestra los payloads exactos antes de que salgan de tu máquina.

## Cómo funciona

```text
you build/test through csx (or your agent does)
→ local analysis: public packages, lockfile-resolved versions, symbols, environment
→ raw errors sanitized locally into fingerprints (paths/names/secrets stripped)
→ anonymous evidence batches → Compatibility Graph on codesamplex.dev
→ your LLM asks CSX first: nearest verified Sample + environment delta
→ it reasons about the DELTA, not the whole problem
```

Cuatro capas, mantenidas honestamente separadas:

| Capa | Qué es | Confianza |
|-------|------------|-------|
| Evidence Network | hechos anónimos de paquete/versión/símbolo/entorno/etapa/resultado | débil→fuerte, etiquetada por clase |
| Compatibility Graph | mapa probabilístico agregado por entorno (incl. contexto de ejecución: Node/Chrome/Safari/Electron/…) | vista derivada |
| Sample Pool | proyectos mínimos aprobados por el usuario, clean-room, direccionados por contenido | verificados por contrato y con verificación cruzada |
| Agent Delivery | MCP/CLI: sample más cercano + delta + fallos conocidos | graduado de EXACT a NO_SAFE_MATCH |

Que un proyecto compile nunca se presenta como que un símbolo funciona. Las causas desconocidas se quedan en `UNKNOWN`. Un HIT incorrecto es peor que un MISS: `NO_SAFE_MATCH` es una funcionalidad, no un defecto.

## Integración con agentes (MCP)

**Configurado automáticamente por `csx init`:** Claude Code · Codex · Gemini CLI · OpenCode.

**Cualquier otro cliente MCP** (Cursor, Windsurf, Cline, Zed, VS Code) también sirve: `csx` es un servidor MCP stdio estándar:

```json
{"mcpServers": {"csx": {"command": "csx", "args": ["mcp"]}}}
```

Independiente del modelo: la misma evidencia de compatibilidad sirve a Claude, GPT y Codex, Gemini, Llama — cualquier modelo capaz de llamar a una herramienta MCP.

Herramientas: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats`. Publicar un sample deliberadamente **no** es una capacidad MCP: requiere tu aprobación explícita por CLI tras una vista previa completa.

```bash
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

## Ecosistemas (Public v1)

Node/TypeScript (npm, pnpm, yarn — referencia), Python (pip, uv), Go, Rust/Cargo. Matriz de capacidades honesta: [docs/adapters.md](../adapters.md) — ningún adaptador afirma instrumentar símbolos en tiempo de ejecución en la v1, y la confianza en la resolución de símbolos siempre viene etiquetada (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Arquitectura

Un único binario en Go (`csx`: daemon + CLI + MCP + nodo peer + verificador) y un servidor pequeño (`csx-server`: PostgreSQL + explorador renderizado en servidor detrás de Caddy). Los samples se direccionan por contenido (`sha256`) y se distribuyen con prioridad a la caché local → peers → seeder principal. Los samples descargados nunca se ejecutan directamente en tu host: se resuelven con `--ignore-scripts`, la compilación y la ejecución del contrato ocurren sin red dentro de un sandbox, y los recibos van firmados con ed25519. Consulta [goal.md](../../goal.md) (especificación del producto), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md).

## Compilar desde el código fuente

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## Licencia

Código: Apache-2.0. Los samples publicados usan **MIT-0** por defecto.
