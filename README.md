# CodeSampleX

[![Release](https://img.shields.io/github/v/release/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/r2cuerdame/CodeSampleX/total)](https://github.com/r2cuerdame/CodeSampleX/releases)
[![License](https://img.shields.io/github/license/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/LICENSE)
[![Release pipeline](https://img.shields.io/github/actions/workflow/status/r2cuerdame/CodeSampleX/release.yml?label=release)](https://github.com/r2cuerdame/CodeSampleX/actions/workflows/release.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/go.mod)

> **Tested. Not guessed.**

<p align="center">
  <img src="internal/web/static/inspector-hero-v1.webp" alt="CodeSampleX compatibility inspector" width="560">
</p>

**Languages:** [English](README.md) · [한국어](docs/i18n/README.ko.md) · [日本語](docs/i18n/README.ja.md) · [简体中文](docs/i18n/README.zh-CN.md) · [Español](docs/i18n/README.es.md) · [Français](docs/i18n/README.fr.md) · [Deutsch](docs/i18n/README.de.md) · [Português (BR)](docs/i18n/README.pt-BR.md) · [Русский](docs/i18n/README.ru.md)

CodeSampleX is an **open compatibility testing network** for developer libraries, runtimes and toolchains. It does not summarize documentation and it does not collect anecdotes. It records what real builds actually did, in environments recorded rather than assumed — then shows you where things worked, where they broke, and in which environment.

Two things are the asset, and a third is a bonus:

- **Evidence** — real builds on real developer machines, with the environment, the stage they reached, and the public error code when they failed. It arrives automatically wherever csx is installed, so it reaches platforms no container lane can: no container reproduces a macOS host — Docker on a Mac runs a Linux VM, which is a different machine wearing the same laptop — npm publishes no Windows image, and the long tail of musl, ARM, corporate base images and actually-installed runtime versions is unbounded. This is the only thing that can fill the map.
- **Findings** — the contradictions we detected in that evidence: what a competent developer or model expects, measured against what happened. These are ours to stand behind, and they are what a model most often gets wrong, because they are exactly what the documentation does not say.
- **Samples** — runnable code we write and verify ourselves. A bonus, deliberately: a container farm can never cover the space, so a verified sample is a confidence tier above the evidence, never the condition for having an answer at all.

Which is why a miss is not empty. When nothing has been proven for your case the grade stays `NO_SAFE_MATCH` — and the recorded observations come back with it, as recorded.

- Compatibility map: **https://codesamplex.dev**
- The question it answers: *does it run there?* — this API, on this version, on this OS, under this runtime.
- The answer it gives: *here is what happened, and here is where it ran.* Never who: reporters are anonymous peer buckets, and no identity is collected to show.

## Does it run there?

Every result is a recorded execution with its environment attached, so the data pivots into compatibility matrices — version × symbol, OS × runtime, version × architecture. A real slice, copied from the live network on 2026-08-23:

```text
                                         v5.10.0     v5.9.2    v5.7.3
github.com/jackc/pgx/v5                  ≡ 82% 1209  ≡ 100% 2  ≡ —
Batch                                    ≡ 80% 689   —         —
ParseConfig                              ≡ 82% 1188  —         —
```

That grid is not an illustration — it is [the live page](https://codesamplex.dev/golang/github.com%2Fjackc%2Fpgx%2Fv5), so the numbers above have moved on since they were copied.

**A cell carries a rate and a mark, and never a verdict.** The percentage and the number beside it are observations: 82% of 1,209 recorded observations got through. An observation is one stage a build reached — compile, typecheck, test — so a single build files several, and the count is a count of neither builds nor machines nor people. The `≡` mark is our own sample: it ran here and came back clean. It is deliberately not a check, because a check is an approval stamp and this network does not grade; `≡` is a line in a log. One clean run or a hundred, the mark says the same thing, and a run of ours that failed carries `✕` instead. There is no `PASS`, because "PASS" read as the general claim *this works here* when what was measured is *four runs, four passed*.

The two are kept apart on purpose. Our runs are one pinned container repeated; a thousand reported observations come from a thousand different situations, so adding them would let three of ours pose as evidence of the same kind. A cell reading `≡ —` says exactly that: we have working code for it, and nobody has been seen using it yet.

Colour carries how the runs came out, so a mostly-failing cell reddens without another glyph to learn. `—` stays unknown — never "works", never "broken". Nothing is inferred from the package's ecosystem or its docs.

## Why testing matters

```text
Documentation   → what should work
Code search     → how somebody used it
Community       → what somebody says worked
CodeSampleX     → what was actually tested
```

The rules that keep the map honest:

- A project compiling is **never** presented as a symbol working. Observations and verifications are counted separately and never summed.
- Unknown causes stay `UNKNOWN`. A wrong HIT is worse than a MISS — `NO_SAFE_MATCH` is a real answer.
- Evidence does not decay, and no cell is marked stale. An observation is one pinned release in one pinned environment bucket at one stage, and none of those move: that a build failed there is exactly as true a year later. What can change is the environment, and a different environment is a different cell.
- Failure causes are reported as probability distributions, never invented certainties.

## Install the CLI

The CLI is the local tester: it wraps your real builds, turns their outcomes into anonymous evidence, and answers from the network.

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

That line needs `curl` and CA certificates, which minimal images (debian-slim, alpine, most agent containers) do not have — and `curl … | sh` **exits 0 when curl is missing**, because a pipeline reports the last command's status. Install prerequisites first, or use wget:

```bash
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

The binary lands in `~/.local/bin`, which is on nobody's `PATH` by default:

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # the install check — `csx --version` is not a spelling csx accepts
```

One binary, one question. `csx init` shows the contract below and asks a single choice — **JOIN COMMUNITY** or **LOCAL ONLY**. Piped into `sh`, stdin is consumed by the download pipe, so `init` takes the advertised default: JOIN COMMUNITY. Opt out any time with `csx init --local-only`; both mode flags are re-runnable and non-interactive. For scripted or CI setups: `csx init --community --yes --no-agents`.

## Test and check

```bash
csx run -- pnpm build              # wrap any build/test — its result becomes evidence
csx search "axios multipart upload"  # a verified answer, graded for YOUR environment
csx scan                           # record which public packages a project uses, no build
csx stats                          # local dashboard: hits, adoptions, queue
csx ui                             # browser dashboard + privacy preview
csx sync                           # warm the shard cache — once, right after install
```

`csx sync` is not optional garnish: a fresh install has zero shards cached, so every search returns `NO_SAFE_MATCH` until it syncs. The daemon re-warms in the background afterwards.

`csx search` grades every result against your recorded environment — `EXACT`, `COMPATIBLE`, `ADAPTATION_REQUIRED`, `REFERENCE_ONLY`, or `NO_SAFE_MATCH` — and lists the exact delta (`different`, `adaptationNeeded`) between where the answer was proven and where you are.

## Verified samples

A sample is not a snippet. It is a minimal, content-addressed project (`sha256:<hex>` of its canonical artifact) with a **contract**: assertions that were executed offline in a pinned container and passed. Pinned by image digest, not by tag — the tag is an alias for readers, the digest is what runs — and the signed receipt names the exact image reference, so anyone can re-run the same bytes rather than take the word for it ([docs/adapters.md](docs/adapters.md#verifier-images)). The clean-room authoring loop is CLI-only:

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief, empty workspace
csx sample create <dir>      # ingest the clean-room project
csx sample verify <id>       # resolve → compile → contract, sandboxed
csx sample publish <id>      # requires typing exactly "yes"; leakage findings hard-refuse
```

Publishing scans for secrets, paths, project names and private URLs — findings **block** publication with no override flag. Uploading sample source is deliberately not an MCP capability; only a human at the CLI can publish.

## Findings

Where does it break? [Findings](https://codesamplex.dev/findings) is the measured contradiction list: what the documentation (or common belief) says, next to what the contract measured — documentation mismatches, environment-specific failures, version boundaries. Every line links to the published sample whose contract proves it, so you can re-run the measurement and disagree.

Machine-derived findings grow from published samples whose authors recorded the belief they correct; nobody edits a page to add them.

## Evidence and grading

Why trust a cell? Every result carries its evidence class, weak → strong:

| Grade | What actually happened |
|-------|------------------------|
| `USAGE_OBSERVATION` | a real project built/typechecked/tested with the package — observed, weak |
| `ADOPTION_EVIDENCE` | someone applied a sample and reported whether the build then passed |
| `SAMPLE_VERIFICATION` | the sample's contract executed in a pinned container and passed |
| `CROSS_PASS` | a peer key other than the one that published it re-ran it and it passed again |
| `MATRIX_PASS` | passing receipts span ≥2 OS/runtime-major/browser-family boundaries |
| `STABLE` | ≥3 distinct peer keys pass it, no failure recorded for 30 days |

A peer is a key, not a person and not a machine. A peer id is the hash of a self-generated ed25519 key with no registration behind it, so one operator can hold as many as they run workers. "Distinct peer keys" means the same coordinate was reported from more than one place; it is never a head count, and nothing here identifies who ran anything.

Sample pages also badge the verification ladder `L0_SOURCE_ONLY` → `L5_MATRIX_PASS`, and matrix cells carry confidence (`HIGH`/`MEDIUM`/`LOW`), elevated-failure flags, and last-seen dates. Only signed **v2 receipts** may claim `resolvedPackages` — the versions the verifier actually installed, not the versions an author typed; snapshots file each receipt under the version that really ran.

The public counters are a rollup, available as JSON without an account:

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

| Field | What it counts |
|-------|----------------|
| `packages` / `symbols` | coverage: public package names and observed symbols in the compatibility data |
| `evidence` | accepted observation records; not users, projects, or verified samples |
| `verifiedSamples` | distinct samples with a sandbox contract-PASS receipt |
| `peers` / `projectsMonth` | distinct anonymous daily/monthly contributor buckets |
| `postHitBuildsReported` | adoption reports that included a measured PASS or FAIL |

CodeSampleX does **not yet measure reliable unique/active users, live MCP processes, or successful installs**. Any `estimated*` field in the stats response is explicitly formula-based and must not be read as a measured count.

## Contributor worker

The network's environments are other people's machines. A spare one can contribute Docker-isolated verification without touching MCP or agent config:

```bash
csx init --community --yes --no-agents --no-daemon
csx worker start                         # idle-aware, 2 Docker lanes
csx worker start --parallel 4 --budget 15m
```

The worker accepts only server-assigned VERIFY jobs (`cross` / `matrix`) — the queue never sends an arbitrary shell command. Artifacts are content-addressed and hash-checked; resolve is containerized; compile and contract stages run network-off in disposable Docker workspaces with fixed `512m` memory / `256` PID limits; a missing Docker daemon is a hard refusal, never a host fallback. Results are ed25519-signed v2 receipts; raw stage logs stay local.

## API

The same data the website renders, as JSON, without an account:

| Endpoint | What it serves |
|----------|----------------|
| `GET /v1/stats` | the daily network rollup |
| `POST /v1/search`, `POST /v2/search` | graded answers for a query + environment fingerprint |
| `GET /v1/registry/packages/{purl}` | package detail + package-level snapshot |
| `GET /v1/registry/symbols/{eco}/{package}/{family}` | per-version snapshots for one symbol |
| `GET /v1/shards/{eco}/{package}/{major}` | the pre-materialized compatibility shard (ETag-cached) |
| `GET /v1/samples/{id}`, `…/artifact` | sample metadata, receipts, and the tar.gz source |
| `GET /v1/peers/for-sample/{sampleId}` | peers holding that sample, for fetching it without this server |
| `GET /v1/wanted` | the demand queue: what was asked for and not answered |
| `GET /v1/adapters` | the per-ecosystem capability matrix |

## Agent adapter (MCP)

Coding agents consume the same network through an adapter — MCP is a connector on top of the CLI and API, not the product:

```text
CodeSampleX
├─ CLI   ← primary local tester
├─ API   ← automation / integration
├─ Web   ← compatibility map / reports
└─ MCP   ← agent adapter
```

`csx init` configures Claude Code, Codex, Gemini CLI and OpenCode automatically. Any other stdio MCP client (Cursor, Windsurf, Cline, Zed, VS Code) works from what `csx mcp-config` prints (`--toml` for Codex) — it emits the absolute binary path, which a client started by an editor needs. The server itself is `csx mcp`. Eight tools: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats` — and deliberately no publish tool.

Agent-directed install steps (including the MCPB bundle and direct binary downloads with `SHA256SUMS.txt`): [llms-install.md](llms-install.md). Standalone community installs auto-update over an Ed25519-signed manifest with `csx update rollback` available; `local-only` installs make no update request.

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

This is not hidden telemetry — it is the protocol. Community peers are consumers **and** producers. Local-only mode never sends anything. Errors are sanitized locally into fingerprints before any use; private and unknown packages never leave the machine; the privacy preview in `csx ui` shows the exact payloads before they leave. A `NO_SAFE_MATCH` contributes a privacy-safe Wanted tuple — the public package, its exact version and, when the request named one unambiguous package, the requested public symbols — never the user's prompt. The public deployment is **seeded-only for sample source**; search, evidence, receipts and the wanted board are open without an account.

## Privacy Policy

The contract above is what the code does. [PRIVACY.md](PRIVACY.md) states the same thing as a policy, field by field, naming the file that enforces each boundary: the exact documents community mode uploads, the requests that are downloads rather than uploads, what the server stores and for how long, and what `local-only` means when it says it sends nothing. It is versioned in this repository rather than served from a page that can be edited without a trace, and it is the URL the MCPB bundle's `privacy_policies` array points at.

## Ecosystems (Public v1)

**Scanned and verified** — projects detected, packages lockfile-resolved, samples verified end to end: Node/TypeScript (npm, pnpm, yarn — reference), Python (pip, uv), Go, Rust/Cargo. Node samples run on the runtime they declare, so Bun and Deno results are real rather than assumed.

**Verified only** — no project scanner yet, but published samples are built and contract-tested in a pinned container: PHP/Composer, Ruby/Bundler, Dart/pub, Elixir/Hex. Java (Maven/Gradle) contract verification pins exact JDK 8/11/17/21/25 lanes.

Honest capability matrix: [docs/adapters.md](docs/adapters.md) — symbol resolution confidence is always labeled (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Architecture

Single Go binary (`csx`: daemon + CLI + MCP + peer node + verifier) and a small server (`csx-server`: PostgreSQL + server-rendered compatibility explorer behind Caddy). Samples are content-addressed and distributed local-cache-first → peers → main seeder. Downloaded samples never run on the host directly: resolve runs in a pinned sandbox with install scripts disabled where the ecosystem supports it, the artifact is re-hashed after resolve, and compile/contract stages run network-off. See [goal.md](goal.md), [docs/execution-context.md](docs/execution-context.md), [docs/operations.md](docs/operations.md).

## Building from source

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

That needs no database: the `internal/serverstore` PostgreSQL tests skip
themselves when `CSX_TEST_DSN` is unset. They are not optional in CI —
[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs every pull request
against a disposable `postgres:17-alpine` and fails the run if any of them
skipped. That run is a required check on `main`, so a red one stops the merge —
the ruleset behind that, and what to do when the job is renamed, is in
[docs/operations.md](docs/operations.md). To run them the same way locally:

```bash
docker run -d --rm --name csx-pg -e POSTGRES_USER=csx -e POSTGRES_PASSWORD=csx \
  -e POSTGRES_DB=csx -p 5432:5432 postgres:17-alpine
CSX_TEST_DSN="postgres://csx:csx@localhost:5432/csx?sslmode=disable" \
  CSX_REQUIRE_TEST_DSN=1 go test ./...
```

Each test migrates its own throwaway schema and drops it afterwards, so the
suite is repeatable and two runs can share one database. `CSX_REQUIRE_TEST_DSN`
turns a missing DSN into a failure instead of a skip, which is what CI sets.

## License

Code: Apache-2.0. Published samples default to **MIT-0**.
