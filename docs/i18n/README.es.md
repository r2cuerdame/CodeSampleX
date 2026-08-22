# CodeSampleX

[![Release](https://img.shields.io/github/v/release/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/r2cuerdame/CodeSampleX/total)](https://github.com/r2cuerdame/CodeSampleX/releases)
[![License](https://img.shields.io/github/license/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/LICENSE)
[![Release pipeline](https://img.shields.io/github/actions/workflow/status/r2cuerdame/CodeSampleX/release.yml?label=release)](https://github.com/r2cuerdame/CodeSampleX/actions/workflows/release.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/go.mod)

> **Tested. Not guessed.** (*Probado. No adivinado.*)

<p align="center">
  <img src="../../internal/web/static/inspector-hero-v1.webp" alt="Inspector de compatibilidad de CodeSampleX" width="560">
</p>

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX es una **red abierta de pruebas de compatibilidad** para librerías, runtimes y toolchains de desarrollo. No resume documentación ni recopila anécdotas: ejecuta builds reales y pruebas de contrato en entornos reales y registrados — y después te muestra dónde funcionaron las cosas de verdad, dónde se rompieron y cuánta certeza tiene de ambas.

- Mapa de compatibilidad: **https://codesamplex.dev**
- La pregunta que responde: *¿funciona ahí?* — esta API, en esta versión, en este sistema operativo, bajo este runtime.
- La respuesta que da: *lo probamos; esto es lo que pasó.*

## ¿Funciona ahí?

Cada resultado es una ejecución registrada con su entorno adjunto, de modo que los datos pivotan en matrices de compatibilidad — OS × runtime, versión × arquitectura, símbolo × OS. Un corte real, de la red en vivo (`axios.post`, medido en agosto de 2026):

```text
github.com/jackc/pgx/v5                  v5.10.0     v5.9.2    v5.7.3
whole package                            ✓ 85% 1156  ✓ 100% 2  ✓ —
Batch                                    ✓ 91% 240   —         —
Identifier                                 60% 15    —         ✓ —
```

Esta cuadrícula no es una ilustración: es [la página en vivo](https://codesamplex.dev/golang/github.com%2Fjackc%2Fpgx%2Fv5).

**Una celda lleva una tasa y una marca, nunca un veredicto.** El porcentaje y el número contiguo son lo que hicieron máquinas reales; la marca significa que nuestra muestra funciona allí. Nuestras ejecuciones son un contenedor fijado repetido; mil ejecuciones informadas son mil situaciones distintas, así que no se suman. `✓ —` significa: hay código que funciona y nadie ha sido visto usándolo todavía. `—` permanece desconocido.

## Por qué importan las pruebas

```text
Documentation   → what should work
Code search     → how somebody used it
Community       → what somebody says worked
CodeSampleX     → what was actually tested
```

Las reglas que mantienen honesto el mapa:

- Que un proyecto compile **nunca** se presenta como que un símbolo funciona. Las observaciones y las verificaciones se cuentan por separado y jamás se suman.
- Las causas desconocidas se quedan en `UNKNOWN`. Un HIT incorrecto es peor que un MISS — `NO_SAFE_MATCH` es una respuesta real.
- La evidencia decae: el peso de un resultado se reduce a la mitad cada 90 días, y las celdas obsoletas lo dicen.
- Las causas de fallo se informan como distribuciones de probabilidad, nunca como certezas inventadas.

## Instalación de la CLI

La CLI es el probador local: envuelve tus builds reales, convierte sus resultados en evidencia anónima y responde desde la red.

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Esa línea necesita `curl` y certificados CA, que las imágenes mínimas (debian-slim, alpine, la mayoría de los contenedores de agentes) no traen — y `curl … | sh` **termina con código 0 cuando falta curl**, porque una tubería informa del estado del último comando. Instala primero los prerrequisitos, o usa wget:

```bash
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

El binario queda en `~/.local/bin`, que por defecto no está en el `PATH` de nadie:

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # the install check — `csx --version` is not a spelling csx accepts
```

Un solo binario, una sola pregunta. `csx init` muestra el contrato de más abajo y pide una única decisión — **JOIN COMMUNITY** o **LOCAL ONLY**. Al canalizarlo a `sh`, la entrada estándar la consume la tubería de descarga, así que `init` toma el valor por defecto anunciado: JOIN COMMUNITY. Puedes desactivarlo en cualquier momento con `csx init --local-only`; ambos flags de modo son reejecutables y no interactivos. Para instalaciones con script o en CI: `csx init --community --yes --no-agents`.

## Probar y consultar

```bash
csx run -- pnpm build              # wrap any build/test — its result becomes evidence
csx search "axios multipart upload"  # a verified answer, graded for YOUR environment
csx scan                           # record which public packages a project uses, no build
csx stats                          # local dashboard: hits, adoptions, queue
csx ui                             # browser dashboard + privacy preview
csx sync                           # warm the shard cache — once, right after install
```

`csx sync` no es un adorno opcional: una instalación recién hecha tiene cero shards en caché, así que todas las búsquedas devuelven `NO_SAFE_MATCH` hasta que sincroniza. Después, el daemon recalienta la caché en segundo plano.

`csx search` califica cada resultado frente a tu entorno registrado — `EXACT`, `COMPATIBLE`, `ADAPTATION_REQUIRED`, `REFERENCE_ONLY` o `NO_SAFE_MATCH` — y enumera el delta exacto (`different`, `adaptationNeeded`) entre donde se demostró la respuesta y donde estás tú.

## Samples verificados

Un sample no es un fragmento de código. Es un proyecto mínimo direccionado por contenido (`sha256:<hex>` de su artefacto canónico) con un **contrato**: aserciones que se ejecutaron sin conexión en un contenedor fijado y pasaron. Fijado por digest de la imagen, no por etiqueta — la etiqueta es un alias para quien lee, el digest es lo que se ejecuta — y el recibo firmado nombra la referencia exacta de la imagen, de modo que cualquiera puede volver a ejecutar los mismos bytes en lugar de creerlo sin más ([docs/adapters.md](../adapters.md#verifier-images)). El ciclo de autoría clean-room es exclusivamente por CLI:

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief, empty workspace
csx sample create <dir>      # ingest the clean-room project
csx sample verify <id>       # resolve → compile → contract, sandboxed
csx sample publish <id>      # requires typing exactly "yes"; leakage findings hard-refuse
```

La publicación escanea en busca de secretos, rutas, nombres de proyecto y URLs privadas — los hallazgos **bloquean** la publicación y no existe flag para saltárselos. Subir el código fuente de un sample deliberadamente no es una capacidad MCP; solo un humano en la CLI puede publicar.

## Hallazgos

¿Dónde se rompe? [Hallazgos](https://codesamplex.dev/findings) es la lista medida de contradicciones: lo que dice la documentación (o la creencia común), junto a lo que midió el contrato — discrepancias con la documentación, fallos específicos de un entorno, fronteras de versión. Cada línea enlaza con el sample publicado cuyo contrato lo demuestra, para que puedas repetir la medición y discrepar.

Los hallazgos derivados por máquina crecen a partir de samples publicados cuyos autores registraron la creencia que corrigen; nadie edita una página para añadirlos.

## Evidencia y calificación

¿Por qué fiarse de una celda? Cada resultado lleva su clase de evidencia, de débil → fuerte:

| Grado | Qué ocurrió realmente |
|-------|------------------------|
| `USAGE_OBSERVATION` | un proyecto real compiló/pasó el typecheck/sus tests con el paquete — observado, débil |
| `ADOPTION_EVIDENCE` | alguien aplicó un sample e informó de si el build pasó después |
| `SAMPLE_VERIFICATION` | el contrato del sample se ejecutó en un contenedor fijado y pasó |
| `CROSS_PASS` | un par independiente lo volvió a ejecutar y volvió a pasar |
| `MATRIX_PASS` | las ejecuciones que pasan abarcan ≥2 fronteras de OS/runtime/navegador |
| `STABLE` | ≥3 pares independientes lo pasan, sin fallos registrados durante 30 días |

Las páginas de sample también muestran la escalera de verificación `L0_SOURCE_ONLY` → `L5_MATRIX_PASS`, y las celdas de la matriz llevan confianza (`HIGH`/`MEDIUM`/`LOW`), indicadores de fallo elevado y fechas de última observación. Solo los **recibos v2** firmados pueden declarar `resolvedPackages` — las versiones que el verificador instaló realmente, no las que escribió un autor; los snapshots archivan cada recibo bajo la versión que de verdad se ejecutó.

Los contadores públicos son un agregado, disponible como JSON sin necesidad de cuenta:

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

| Campo | Qué cuenta |
|-------|----------------|
| `packages` / `symbols` | cobertura: nombres de paquetes públicos y símbolos observados en los datos de compatibilidad |
| `evidence` | registros de observación aceptados; no usuarios, proyectos ni samples verificados |
| `verifiedSamples` | samples distintos con un recibo de contract-PASS en sandbox |
| `peers` / `projectsMonth` | cubetas anónimas distintas de contribuidores diarios/mensuales |
| `postHitBuildsReported` | informes de adopción que incluyeron un PASS o FAIL medido |

CodeSampleX **todavía no mide de forma fiable usuarios únicos/activos, procesos MCP vivos ni instalaciones con éxito**. Cualquier campo `estimated*` de la respuesta de stats se calcula explícitamente con una fórmula y no debe leerse como un recuento medido.

## Worker de contribución

Los entornos de la red son las máquinas de otras personas. Una máquina libre puede contribuir verificación aislada en Docker sin tocar la configuración de MCP ni la de agentes:

```bash
csx init --community --yes --no-agents --no-daemon
csx worker start                         # idle-aware, 2 Docker lanes
csx worker start --parallel 4 --budget 15m
```

El worker acepta únicamente trabajos VERIFY asignados por el servidor (`cross` / `matrix`) — la cola nunca envía un comando de shell arbitrario. Los artefactos están direccionados por contenido y se comprueban por hash; resolve se ejecuta en contenedor; las etapas de compilación y contrato corren sin red en espacios de trabajo Docker desechables con límites fijos de `512m` de memoria / `256` PIDs; que falte el daemon de Docker es un rechazo rotundo, nunca un plan B en el host. Los resultados son recibos v2 firmados con ed25519; los logs crudos de cada etapa se quedan en local.

## API

Los mismos datos que renderiza el sitio web, como JSON, sin cuenta:

| Endpoint | Qué sirve |
|----------|----------------|
| `GET /v1/stats` | el agregado diario de la red |
| `POST /v1/search`, `POST /v2/search` | respuestas calificadas para una consulta + huella de entorno |
| `GET /v1/registry/packages/{purl}` | detalle del paquete + snapshot a nivel de paquete |
| `GET /v1/registry/symbols/{eco}/{package}/{family}` | snapshots por versión para un símbolo |
| `GET /v1/shards/{eco}/{package}/{major}` | el shard de compatibilidad prematerializado (con caché por ETag) |
| `GET /v1/samples/{id}`, `…/artifact` | metadatos del sample, recibos y el código fuente en tar.gz |
| `GET /v1/wanted` | la cola de demanda: lo que se preguntó y no obtuvo respuesta |
| `GET /v1/adapters` | la matriz de capacidades por ecosistema |

## Adaptador para agentes (MCP)

Los agentes de programación consumen la misma red a través de un adaptador — MCP es un conector sobre la CLI y la API, no el producto:

```text
CodeSampleX
├─ CLI   ← primary local tester
├─ API   ← automation / integration
├─ Web   ← compatibility map / reports
└─ MCP   ← agent adapter
```

`csx init` configura automáticamente Claude Code, Codex, Gemini CLI y OpenCode. Cualquier otro cliente MCP stdio (Cursor, Windsurf, Cline, Zed, VS Code) funciona con lo que imprime `csx mcp-config` (`--toml` para Codex) — emite la ruta absoluta del binario, que es lo que necesita un cliente arrancado por un editor. El servidor en sí es `csx mcp`. Ocho herramientas: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats` — y, deliberadamente, ninguna herramienta de publicación.

Pasos de instalación dirigidos a agentes (incluido el bundle MCPB y las descargas directas de binarios con `SHA256SUMS.txt`): [llms-install.md](../../llms-install.md). Las instalaciones comunitarias independientes se actualizan solas mediante un manifiesto firmado con Ed25519, con `csx update rollback` disponible; las instalaciones `local-only` no hacen ninguna petición de actualización.

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

Esto no es telemetría oculta — es el protocolo. Los pares de la comunidad son consumidores **y** productores. El modo solo-local nunca envía nada. Los errores se sanean en local y se convierten en huellas antes de cualquier uso; los paquetes privados y desconocidos nunca salen de la máquina; la vista previa de privacidad en `csx ui` muestra los payloads exactos antes de que salgan. Un `NO_SAFE_MATCH` aporta una tupla Wanted respetuosa con la privacidad — el paquete público, su versión exacta y, cuando la petición nombró un único paquete inequívoco, los símbolos públicos solicitados — nunca el prompt del usuario. El despliegue público es **solo-seeder para el código fuente de los samples**; la búsqueda, la evidencia, los recibos y el tablón de peticiones están abiertos sin cuenta.

## Ecosistemas (Public v1)

**Escaneados y verificados** — proyectos detectados, paquetes resueltos por lockfile, samples verificados de principio a fin: Node/TypeScript (npm, pnpm, yarn — referencia), Python (pip, uv), Go, Rust/Cargo. Los samples de Node se ejecutan en el runtime que declaran, así que los resultados de Bun y Deno son reales, no supuestos.

**Solo verificados** — aún sin escáner de proyectos, pero los samples publicados se compilan y se prueban por contrato en un contenedor fijado: PHP/Composer, Ruby/Bundler, Dart/pub, Elixir/Hex. La verificación por contrato de Java (Maven/Gradle) fija carriles exactos de JDK 8/11/17/21/25.

Matriz de capacidades honesta: [docs/adapters.md](../adapters.md) — la confianza de la resolución de símbolos siempre viene etiquetada (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Arquitectura

Un único binario en Go (`csx`: daemon + CLI + MCP + nodo par + verificador) y un servidor pequeño (`csx-server`: PostgreSQL + explorador de compatibilidad renderizado en el servidor detrás de Caddy). Los samples se direccionan por contenido y se distribuyen con prioridad caché-local → pares → seeder principal. Los samples descargados nunca se ejecutan directamente en el host: resolve corre en un sandbox fijado con los scripts de instalación deshabilitados donde el ecosistema lo permite, el artefacto se vuelve a hashear tras resolve, y las etapas de compilación y contrato corren sin red. Consulta [goal.md](../../goal.md), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md).

## Compilar desde el código fuente

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## Licencia

Código: Apache-2.0. Los samples publicados usan **MIT-0** por defecto.
