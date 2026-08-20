# CodeSampleX

> **Tested. Not guessed.** (*Getestet, nicht geraten.*)

<p align="center">
  <img src="../../internal/web/static/inspector-hero-v1.webp" alt="CodeSampleX-Kompatibilitätsinspektor" width="560">
</p>

**Sprachen:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX ist ein **offenes Kompatibilitäts-Testnetzwerk** für Entwickler-Libraries, Runtimes und Toolchains. Es fasst keine Dokumentation zusammen und sammelt keine Anekdoten: Es führt echte Builds und Contract-Tests in echten, aufgezeichneten Umgebungen aus — und zeigt dir dann, wo etwas tatsächlich funktioniert hat, wo es brach und wie sicher es sich bei beidem ist.

- Kompatibilitätskarte: **https://codesamplex.dev**
- Die Frage, die es beantwortet: *Läuft es dort?* — diese API, in dieser Version, auf diesem OS, unter dieser Runtime.
- Die Antwort, die es gibt: *Wir haben es getestet; das ist dabei passiert.*

## Läuft es dort?

Jedes Ergebnis ist eine aufgezeichnete Ausführung mit angehängter Umgebung, deshalb lassen sich die Daten in Kompatibilitätsmatrizen pivotieren — OS × Runtime, Version × Architektur, Symbol × OS. Ein echter Ausschnitt aus dem Live-Netzwerk (`axios.post`, gemessen im August 2026):

```text
axios.post · axios 1.12.2                node 22            node 24
linux                                    ■ verified 4/4       —
windows                                  ○ observed 3/9 ! ?
```

Diese Zeile ist keine Illustration — sie ist [die Live-Seite](https://codesamplex.dev/npm/axios/1.12.2/axios.post).

**Eine Zelle nennt eine Quote und ihre Grundlage. Nie ein Urteil.** `■ verified` heißt, dass wir selbst einen Vertrag in einem gepinnten Container ausgeführt haben; `○ observed` heißt, dass echte Maschinen Läufe aufgezeichnet und gemeldet haben. Die Zahl ist die Messung — Erfolge pro Lauf — und ein einzelnes `1/1` sagt deshalb, wie dünn der Beleg ist, statt sich hinter einem Zeichen zu verstecken, das wie hundert übereinstimmende Läufe aussah. Es gibt kein `PASS` mehr: PASS las sich als die allgemeine Behauptung *das funktioniert hier*, während gemessen wurde *vier Läufe, vier erfolgreich*.

Die Grundlage verschwimmt nie, denn sie ist die Unterscheidung, auf die es ankommt. Beobachtungszahlen übersteigen Verifikationszahlen bei Weitem, also ließen zwei nackte Quoten eine anonyme Zelle maßgeblicher wirken als eine bewiesene. Das Zeichen ist ausgefüllt für einen Lauf, den wir gemacht haben, und hohl für eine Meldung, die wir erhalten haben — ohne Farbe unterscheidbar; Farbe trägt nur, wie die Quote ausfiel, denn ein Fehlschlag ist das seltene, informationsreiche Ereignis und muss ins Auge fallen. `!` markiert eine gemessene Anomalie, `?` schwache oder alte Belege, `—` bleibt unbekannt. Aus dem Ökosystem eines Pakets oder seiner Dokumentation wird nichts abgeleitet.

Der Web-Explorer behandelt jedes 2D-Raster als Schnitt durch einen N-dimensionalen Würfel: Wähle zwei beliebige Dimensionen als Achsen (OS, Runtime, Paketversion, Symbol, Architektur, Paketmanager, Execution Context, libc), fixiere den Rest als Filter und klicke auf eine Zelle, um eine Ebene tiefer zu bohren — bis hinunter zu den exakt gemessenen Kombinationen, deren Symbolseiten die signierten Receipts enthalten.

## Warum Testen zählt

```text
Documentation   → what should work
Code search     → how somebody used it
Community       → what somebody says worked
CodeSampleX     → what was actually tested
```

Die Regeln, die die Karte ehrlich halten:

- Dass ein Projekt kompiliert, wird **nie** als funktionierendes Symbol dargestellt. Beobachtungen und Verifikationen werden getrennt gezählt und nie zusammengerechnet.
- Unbekannte Ursachen bleiben `UNKNOWN`. Ein falscher HIT ist schlimmer als ein MISS — `NO_SAFE_MATCH` ist eine echte Antwort.
- Evidenz verfällt: Das Gewicht eines Ergebnisses halbiert sich alle 90 Tage, und veraltete Zellen sagen das auch.
- Fehlerursachen werden als Wahrscheinlichkeitsverteilungen berichtet, nie als erfundene Gewissheiten.

## Die CLI installieren

Die CLI ist der lokale Tester: Sie umhüllt deine echten Builds, macht aus deren Ergebnissen anonyme Evidenz und antwortet aus dem Netzwerk.

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Diese Zeile braucht `curl` und CA-Zertifikate, die in minimalen Images (debian-slim, alpine, die meisten Agent-Container) fehlen — und `curl … | sh` **endet mit Exit-Code 0, wenn curl fehlt**, weil eine Pipeline den Status des letzten Befehls meldet. Installiere zuerst die Voraussetzungen oder nimm wget:

```bash
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

Das Binary landet in `~/.local/bin`, das standardmäßig bei niemandem im `PATH` liegt:

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # the install check — `csx --version` is not a spelling csx accepts
```

Ein Binary, eine Frage. `csx init` zeigt den unten stehenden Vertrag und stellt eine einzige Wahl — **JOIN COMMUNITY** oder **LOCAL ONLY**. In `sh` gepipet wird stdin von der Download-Pipe verbraucht, also nimmt `init` den angekündigten Default: JOIN COMMUNITY. Aussteigen geht jederzeit mit `csx init --local-only`; beide Modus-Flags sind erneut ausführbar und nicht-interaktiv. Für Skript- oder CI-Setups: `csx init --community --yes --no-agents`.

## Testen und prüfen

```bash
csx run -- pnpm build              # wrap any build/test — its result becomes evidence
csx search "axios multipart upload"  # a verified answer, graded for YOUR environment
csx scan                           # record which public packages a project uses, no build
csx stats                          # local dashboard: hits, adoptions, queue
csx ui                             # browser dashboard + privacy preview
csx sync                           # warm the shard cache — once, right after install
```

`csx sync` ist keine optionale Beigabe: Eine frische Installation hat null Shards im Cache, also liefert jede Suche `NO_SAFE_MATCH`, bis synchronisiert wurde. Danach wärmt der Daemon den Cache im Hintergrund selbst nach.

`csx search` stuft jedes Ergebnis gegen deine aufgezeichnete Umgebung ein — `EXACT`, `COMPATIBLE`, `ADAPTATION_REQUIRED`, `REFERENCE_ONLY` oder `NO_SAFE_MATCH` — und listet das exakte Delta (`different`, `adaptationNeeded`) zwischen dem Ort, an dem die Antwort bewiesen wurde, und dem, an dem du bist.

## Verifizierte Samples

Ein Sample ist kein Snippet. Es ist ein minimales, content-addressiertes Projekt (`sha256:<hex>` seines kanonischen Artefakts) mit einem **Contract**: Assertions, die offline in einem gepinnten Container ausgeführt wurden und bestanden haben. Der Clean-Room-Autorenzyklus läuft ausschließlich über die CLI:

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief, empty workspace
csx sample create <dir>      # ingest the clean-room project
csx sample verify <id>       # resolve → compile → contract, sandboxed
csx sample publish <id>      # requires typing exactly "yes"; leakage findings hard-refuse
```

Beim Veröffentlichen wird nach Secrets, Pfaden, Projektnamen und privaten URLs gescannt — Funde **blockieren** die Veröffentlichung, ohne Override-Flag. Das Hochladen von Sample-Quellcode ist bewusst keine MCP-Fähigkeit; veröffentlichen kann nur ein Mensch an der CLI.

## Findings

Wo bricht es? [Findings](https://codesamplex.dev/findings) ist die gemessene Widerspruchsliste: was die Dokumentation (oder die verbreitete Annahme) sagt, direkt neben dem, was der Contract gemessen hat — Dokumentationsabweichungen, umgebungsspezifische Fehlschläge, Versionsgrenzen. Jede Zeile verlinkt auf das veröffentlichte Sample, dessen Contract sie beweist, sodass du die Messung selbst wiederholen und widersprechen kannst.

Maschinell abgeleitete Findings wachsen aus veröffentlichten Samples, deren Autoren die Annahme festgehalten haben, die sie korrigieren; niemand editiert eine Seite, um sie hinzuzufügen.

## Evidenz und Einstufung

Warum einer Zelle trauen? Jedes Ergebnis trägt seine Evidenzklasse, schwach → stark:

| Einstufung | Was tatsächlich passiert ist |
|-------|------------------------|
| `USAGE_OBSERVATION` | ein echtes Projekt wurde mit dem Paket gebaut/typgeprüft/getestet — beobachtet, schwach |
| `ADOPTION_EVIDENCE` | jemand hat ein Sample angewendet und berichtet, ob der Build danach bestanden hat |
| `SAMPLE_VERIFICATION` | der Contract des Samples wurde in einem gepinnten Container ausgeführt und hat bestanden |
| `CROSS_PASS` | ein unabhängiger Peer hat es erneut ausgeführt, und es hat wieder bestanden |
| `MATRIX_PASS` | bestandene Läufe überspannen ≥2 OS-/Runtime-/Browser-Grenzen |
| `STABLE` | ≥3 unabhängige Peers bestehen es, kein Fehlschlag in 30 Tagen aufgezeichnet |

Sample-Seiten tragen außerdem ein Badge der Verifikationsleiter `L0_SOURCE_ONLY` → `L5_MATRIX_PASS`, und Matrixzellen führen Konfidenz (`HIGH`/`MEDIUM`/`LOW`), Flags für erhöhte Fehlerraten und Zuletzt-gesehen-Daten. Nur signierte **v2-Receipts** dürfen `resolvedPackages` beanspruchen — die Versionen, die der Verifier tatsächlich installiert hat, nicht die Versionen, die ein Autor getippt hat; Snapshots verbuchen jedes Receipt unter der Version, die wirklich gelaufen ist.

Die öffentlichen Zähler sind ein Rollup, verfügbar als JSON ohne Account:

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

| Feld | Was es zählt |
|-------|----------------|
| `packages` / `symbols` | Abdeckung: öffentliche Paketnamen und beobachtete Symbole in den Kompatibilitätsdaten |
| `evidence` | akzeptierte Beobachtungsdatensätze; keine Nutzer, Projekte oder verifizierten Samples |
| `verifiedSamples` | unterschiedliche Samples mit einem Sandbox-Contract-PASS-Receipt |
| `peers` / `projectsMonth` | unterschiedliche anonyme tägliche/monatliche Beitragenden-Buckets |
| `postHitBuildsReported` | Adoptionsberichte, die ein gemessenes PASS oder FAIL enthielten |

CodeSampleX misst **bislang keine verlässlichen eindeutigen/aktiven Nutzer, laufenden MCP-Prozesse oder erfolgreichen Installationen**. Jedes `estimated*`-Feld in der Stats-Antwort ist ausdrücklich formelbasiert und darf nicht als gemessener Zählwert gelesen werden.

## Contributor-Worker

Die Umgebungen des Netzwerks sind die Rechner anderer Leute. Ein übriger Rechner kann Docker-isolierte Verifikation beisteuern, ohne MCP- oder Agent-Konfiguration anzufassen:

```bash
csx init --community --yes --no-agents --no-daemon
csx worker start                         # idle-aware, 2 Docker lanes
csx worker start --parallel 4 --budget 15m
```

Der Worker akzeptiert nur serverseitig zugewiesene VERIFY-Jobs (`cross` / `matrix`) — die Queue schickt niemals einen beliebigen Shell-Befehl. Artefakte sind content-addressiert und hash-geprüft; resolve läuft im Container; Compile- und Contract-Phasen laufen ohne Netzwerk in Wegwerf-Docker-Workspaces mit festen Limits von `512m` Speicher / `256` PIDs; ein fehlender Docker-Daemon ist eine harte Verweigerung, nie ein Host-Fallback. Ergebnisse sind ed25519-signierte v2-Receipts; rohe Phasen-Logs bleiben lokal. Siehe [Contribute](https://codesamplex.dev/contribute).

## API

Dieselben Daten, die die Website rendert, als JSON, ohne Account:

| Endpoint | Was er liefert |
|----------|----------------|
| `GET /v1/stats` | das tägliche Netzwerk-Rollup |
| `POST /v1/search`, `POST /v2/search` | eingestufte Antworten für eine Anfrage + Umgebungs-Fingerprint |
| `GET /v1/registry/packages/{purl}` | Paketdetails + Snapshot auf Paketebene |
| `GET /v1/registry/symbols/{eco}/{package}/{family}` | Snapshots pro Version für ein Symbol |
| `GET /v1/shards/{eco}/{package}/{major}` | der vor-materialisierte Kompatibilitäts-Shard (ETag-gecacht) |
| `GET /v1/samples/{id}`, `…/artifact` | Sample-Metadaten, Receipts und der tar.gz-Quellcode |
| `GET /v1/wanted` | die Nachfrage-Queue: was gefragt und nicht beantwortet wurde |
| `GET /v1/adapters` | die Fähigkeitsmatrix pro Ökosystem |

## Agent-Adapter (MCP)

Coding-Agents konsumieren dasselbe Netzwerk über einen Adapter — MCP ist ein Connector über CLI und API, nicht das Produkt:

```text
CodeSampleX
├─ CLI   ← primary local tester
├─ API   ← automation / integration
├─ Web   ← compatibility map / reports
└─ MCP   ← agent adapter
```

`csx init` konfiguriert Claude Code, Codex, Gemini CLI und OpenCode automatisch. Jeder andere stdio-MCP-Client (Cursor, Windsurf, Cline, Zed, VS Code) funktioniert mit dem, was `csx mcp-config` ausgibt (`--toml` für Codex) — der Befehl gibt den absoluten Binary-Pfad aus, den ein vom Editor gestarteter Client braucht. Der Server selbst ist `csx mcp`. Acht Tools: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats` — und bewusst kein Publish-Tool.

Installationsschritte für Agents (einschließlich MCPB-Bundle und direkter Binary-Downloads mit `SHA256SUMS.txt`): [llms-install.md](../../llms-install.md). Eigenständige Community-Installationen aktualisieren sich automatisch über ein Ed25519-signiertes Manifest, mit `csx update rollback` als Rückweg; `local-only`-Installationen stellen keine Update-Anfrage.

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

Das ist keine versteckte Telemetrie — es ist das Protokoll. Community-Peers sind Konsumenten **und** Produzenten. Der Local-only-Modus sendet niemals etwas. Fehler werden vor jeder Verwendung lokal zu Fingerprints bereinigt; private und unbekannte Pakete verlassen den Rechner nie; die Privacy-Vorschau in `csx ui` zeigt die exakten Payloads, bevor sie ihn verlassen. Ein `NO_SAFE_MATCH` steuert ein datenschutzsicheres Wanted-Tupel bei — das öffentliche Paket, seine exakte Version und, wenn die Anfrage ein eindeutiges Paket benannt hat, die angefragten öffentlichen Symbole — nie den Prompt des Nutzers. Im öffentlichen Deployment ist Sample-Quellcode **seeded-only**; Suche, Evidenz, Receipts und das Wanted-Board sind ohne Account offen.

## Ökosysteme (Public v1)

**Gescannt und verifiziert** — Projekte werden erkannt, Pakete lockfile-aufgelöst, Samples Ende-zu-Ende verifiziert: Node/TypeScript (npm, pnpm, yarn — Referenz), Python (pip, uv), Go, Rust/Cargo. Node-Samples laufen auf der Runtime, die sie deklarieren, deshalb sind Bun- und Deno-Ergebnisse echt statt angenommen.

**Nur verifiziert** — noch kein Projekt-Scanner, aber veröffentlichte Samples werden in einem gepinnten Container gebaut und contract-getestet: PHP/Composer, Ruby/Bundler, Dart/pub, Elixir/Hex. Die Contract-Verifikation für Java (Maven/Gradle) pinnt exakte JDK-Lanes für 8/11/17/21/25.

Ehrliche Fähigkeitsmatrix: [docs/adapters.md](../adapters.md) — die Konfidenz der Symbolauflösung ist immer gekennzeichnet (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Architektur

Ein einzelnes Go-Binary (`csx`: Daemon + CLI + MCP + Peer-Node + Verifier) und ein kleiner Server (`csx-server`: PostgreSQL + server-gerenderter Kompatibilitäts-Explorer hinter Caddy). Samples sind content-addressiert und werden local-cache-first → Peers → Haupt-Seeder verteilt. Heruntergeladene Samples laufen nie direkt auf dem Host: resolve läuft in einer gepinnten Sandbox mit deaktivierten Install-Skripten, wo das Ökosystem das unterstützt, das Artefakt wird nach dem resolve neu gehasht, und Compile-/Contract-Phasen laufen ohne Netzwerk. Siehe [goal.md](../../goal.md), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md).

## Aus dem Quellcode bauen

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## Lizenz

Code: Apache-2.0. Veröffentlichte Samples stehen standardmäßig unter **MIT-0**.
