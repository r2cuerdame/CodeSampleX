# CodeSampleX

[![Release](https://img.shields.io/github/v/release/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/r2cuerdame/CodeSampleX/total)](https://github.com/r2cuerdame/CodeSampleX/releases)
[![License](https://img.shields.io/github/license/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/LICENSE)
[![Release pipeline](https://img.shields.io/github/actions/workflow/status/r2cuerdame/CodeSampleX/release.yml?label=release)](https://github.com/r2cuerdame/CodeSampleX/actions/workflows/release.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/go.mod)

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

Jedes Ergebnis ist eine aufgezeichnete Ausführung mit angehängter Umgebung, deshalb lassen sich die Daten in Kompatibilitätsmatrizen pivotieren — OS × Runtime, Version × Architektur, Symbol × OS. Ein Ausschnitt, am 2026-08-23 unverändert aus dem Live-Netzwerk übernommen:

```text
                                         v5.10.0     v5.9.2    v5.7.3
github.com/jackc/pgx/v5                  ◆ 82% 1209  ◆ 100% 2  ◆ —
Batch                                    ◆ 80% 689   —         —
ParseConfig                              ◆ 82% 1188  —         —
```

Dieses Raster ist keine Illustration, sondern [die Live-Seite](https://codesamplex.dev/golang/github.com%2Fjackc%2Fpgx%2Fv5) — die Zahlen oben haben sich seit dem Übernehmen also bereits bewegt.

**Eine Zelle trägt eine Quote und eine Markierung, nie ein Urteil.** Der Prozentsatz und die Zahl daneben sind Beobachtungen: 82 % von 1.209 aufgezeichneten Beobachtungen kamen durch. Eine Beobachtung ist eine Stufe, die ein Build erreicht hat — kompilieren, typprüfen, testen —, ein einzelner Build hinterlässt also mehrere, und die Zahl zählt weder Builds noch Maschinen noch Personen. Das Zeichen `◆` sagt genau eines: Dieses Netzwerk hat seinen eigenen Kontrakt IN DIESER Umgebung ausgeführt, und er kam sauber zurück. Ob es für die Version und die API Code GIBT, ist ein eigenes Zeichen — ein Dokument —, denn ein Beispiel verschwindet nicht, wenn man den OS-Filter umstellt. Es ist bewusst kein Häkchen, denn ein Häkchen ist ein Genehmigungsstempel, und dieses Netzwerk benotet nicht; ein Lauf von uns, der fehlschlug, trägt stattdessen `✕`. `◆ —` heißt: unser Kontrakt lief hier und kam sauber zurück, und an dieser Koordinate wurde noch kein Build gemeldet. `—` bleibt unbekannt.

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
- Evidenz verfällt nicht, und keine Zelle wird als veraltet markiert. Eine Beobachtung ist ein festgepinntes Release, in einem festgepinnten Umgebungs-Bucket, auf einer Stufe, und keines davon bewegt sich: Dass ein Build dort fehlschlug, ist ein Jahr später genauso wahr. Ändern kann sich die Umgebung — und eine andere Umgebung ist eine andere Zelle.
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

Ein Sample ist kein Snippet. Es ist ein minimales, content-addressiertes Projekt (`sha256:<hex>` seines kanonischen Artefakts) mit einem **Contract**: Assertions, die offline in einem gepinnten Container ausgeführt wurden und bestanden haben. Gepinnt per Image-Digest, nicht per Tag — das Tag ist ein Alias für Leser, der Digest ist das, was läuft — und die signierte Quittung nennt die exakte Image-Referenz, sodass jede und jeder dieselben Bytes erneut ausführen kann, statt es glauben zu müssen ([docs/adapters.md](../adapters.md#verifier-images)). Der Clean-Room-Autorenzyklus läuft ausschließlich über die CLI:

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief + scaffolded workspace
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
| `CROSS_PASS` | ein anderer Peer-Schlüssel als der veröffentlichende hat es erneut ausgeführt, und es hat wieder bestanden |
| `MATRIX_PASS` | bestandene Receipts überspannen ≥2 OS-/Runtime-Major-/Browser-Familien-Grenzen |
| `STABLE` | ≥3 verschiedene Peer-Schlüssel bestehen es, kein Fehlschlag in 30 Tagen aufgezeichnet |

Ein Peer ist ein Schlüssel, keine Person und keine Maschine. Eine Peer-ID ist der Hash eines selbst erzeugten ed25519-Schlüssels ohne Registrierung dahinter, ein Betreiber kann also so viele halten, wie er Worker betreibt. „Verschiedene Peer-Schlüssel“ heißt, dieselbe Koordinate wurde von mehr als einer Stelle gemeldet; es ist nie eine Kopfzahl, und nichts hier identifiziert, wer etwas ausgeführt hat.

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

Der Worker akzeptiert nur serverseitig zugewiesene VERIFY-Jobs (`cross` / `matrix`) — die Queue schickt niemals einen beliebigen Shell-Befehl. Artefakte sind content-addressiert und hash-geprüft; resolve läuft im Container; Compile- und Contract-Phasen laufen ohne Netzwerk in Wegwerf-Docker-Workspaces mit festen Limits von `512m` Speicher / `256` PIDs; ein fehlender Docker-Daemon ist eine harte Verweigerung, nie ein Host-Fallback. Ergebnisse sind ed25519-signierte v2-Receipts; rohe Phasen-Logs bleiben lokal.

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

`csx init` konfiguriert Claude Code, Codex, Gemini CLI und OpenCode automatisch. Jeder andere stdio-MCP-Client (Cursor, Windsurf, Cline, Zed, VS Code) funktioniert mit dem, was `csx mcp-config` ausgibt (`--toml` für Codex) — der Befehl gibt den absoluten Binary-Pfad aus, den ein vom Editor gestarteter Client braucht. Der Server selbst ist `csx mcp`. Zehn Tools: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `report_anomaly`, `report_csx_issue`, `propose_public_sample`, `list_local_hits`, `get_local_stats` — und bewusst kein Publish-Tool. `report_csx_issue` ist dieselbe Idee, auf uns gerichtet statt auf ein Paket: eine Antwort, die den Fehler verdrängte, den man gerade ansah; eine Empfehlung aus einem Ökosystem, das die Frage nie erwähnte; ein Tool-Vertrag, der ein Modell falsch handeln ließ. Opt-in und bewusst leise — nichts fordert einen Agenten auf, es nach einem Fehlschlag aufzurufen, es entsteht kein Ticket, und eine Woche ohne Reports ist eine normale Woche. Ein Defekt, den hundert Agenten treffen, ist EINE Zeile mit steigendem Vorkommniszähler; ist diese Zeile einmal mit einem Bug verknüpft, antwortet jeder spätere Report mit dieser Verknüpfung. Beide Kanäle teilen sich Ingest, Redaction und Dedupe und danach nichts mehr: ein Defekt dieses Produkts kann niemals zu Kompatibilitäts-Evidenz werden.

`report_anomaly` zeigt in die andere Richtung. Wenn eine CSX-Antwort und die eigene Maschine des Agents **konkret** widersprechen — das Netz lieferte ein bestandenes Ergebnis für eine Koordinate, die hier fehlschlug; eine zurückgegebene Symbol-Signatur ist nicht die, die das Paket exportiert — kann der Agent das als Verifikationsanfrage einreichen. Es ist kein Bug-Report: ein Report stellt einen unabhängigen erneuten Lauf in dieselbe Flotte ein, die jede andere Quittung erzeugt, und nur diese Quittung kann ihn bestätigen. Eine Einreichung ohne gemessenes lokales Ergebnis wird abgelehnt, derselbe zweimal gemeldete Widerspruch ist ein Report und ein Lauf, und nichts aus einem Report erreicht eine öffentliche Seite, bevor ein Verifier zustimmt. Die Ursachenvermutung des Melders reist in einem eigenen Feld und entscheidet nie über das Urteil.

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

## Datenschutzerklärung

Der Vertrag oben ist das, was der Code tut. [PRIVACY.md](../../PRIVACY.md) sagt dasselbe als Richtlinie, Feld für Feld, und nennt jeweils die Datei, die die Grenze durchsetzt: die genauen Dokumente, die der Community-Modus hochlädt, die Anfragen, die Downloads und keine Uploads sind, was der Server speichert und wie lange, und was `local-only` meint, wenn es sagt, es sende nichts. Sie ist in diesem Repository versioniert statt von einer Seite ausgeliefert, die sich spurlos ändern lässt — und sie ist die URL, auf die das `privacy_policies`-Array des MCPB-Bundles zeigt.

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
