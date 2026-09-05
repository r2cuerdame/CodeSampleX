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

- **Evidence** — real builds on real developer machines, with the environment, stage, structured termination, and secret-safe normalized failure fingerprint. Missing or legacy failure detail is shown as an Evidence gap, never invented as a cause. It arrives automatically wherever csx is installed, so it reaches platforms no container lane can: no container reproduces a macOS host — Docker on a Mac runs a Linux VM, which is a different machine wearing the same laptop — npm publishes no Windows image, and the long tail of musl, ARM, corporate base images and actually-installed runtime versions is unbounded. This is the only thing that can fill the map.
- **Findings** — the contradictions we detected in that evidence: what a competent developer or model expects, measured against what happened. These are ours to stand behind, and they are what a model most often gets wrong, because they are exactly what the documentation does not say.
- **Samples** — runnable code we write and verify ourselves. A bonus, deliberately: a container farm can never cover the space, so a verified sample is a confidence tier above the evidence, never the condition for having an answer at all.

Which is why a miss is not empty. When nothing has been proven for your case the grade stays `NO_SAFE_MATCH` — and the recorded observations come back with it, as recorded.

- Compatibility map: **https://codesamplex.dev**
- The question it answers: *does it run there?* — this API, on this version, on this OS, under this runtime.
- The answer it gives: *here is what happened, and here is where it ran.* Never who: reporters are anonymous peer buckets, and no identity is collected to show.

## What ships today

CodeSampleX is no longer just a search command. The shipped product now exposes several surfaces, all backed by the same evidence model:

| Surface | Current role |
|---|---|
| [Compatibility](https://codesamplex.dev/compatibility) | browse recorded compatibility by package, version, symbol and environment |
| [Dependencies](https://codesamplex.dev/dependencies) | reverse dependency atlas: which recorded parent releases pulled a dependency release; an edge is **not** a compatibility verdict |
| [Gaps](https://codesamplex.dev/gaps) | completeness census across Sample / Evidence / Dependency, replacing the old website Wanted ranking |
| [Findings](https://codesamplex.dev/findings) | measured contradictions and version/environment boundaries backed by reproducible samples |
| [Features](https://codesamplex.dev/features) | current MCP tool contracts and public read API |
| [Samples](https://codesamplex.dev/samples) · [Adapters](https://codesamplex.dev/adapters) · [Stats](https://codesamplex.dev/stats) | verified artifacts, ecosystem capability matrix, and public network rollups |

The CLI is the canonical local interface. `csx help` is generated from the commands in the binary; this table is pinned to it by a test so a new command cannot silently outgrow the README.

<!-- BEGIN:CSX-CLI-SURFACE -->
| Command | What it does |
|---|---|
| `config` | read or change local settings |
| `daemon` | run, start, stop, or inspect the background sync daemon |
| `hook` | enable, disable, inspect, or self-test the automatic build-failure lookup installed into supported coding agents |
| `init` | choose community/local-only mode, configure agents, warm the first cache, and start background sync |
| `login` | sign in with GitHub when you want attributed sample publishing |
| `mcp` | run the stdio MCP server used by agent clients |
| `mcp-config` | print JSON/TOML/path configuration for MCP clients |
| `run` | run a command and record sanitized evidence for public dependencies |
| `sample` | propose, create, preview, verify, publish, remove, list, or review pending clean-room samples |
| `sample-worker` | operate the private authoring work/session handoff lane |
| `scan` | detect public dependencies without running a build |
| `search` | search verified samples graded against the target environment |
| `stats` | show local cache, queue, hit, and adoption counters |
| `sync` | warm compatibility shards and flush queued community uploads (`--uploads-only` when needed) |
| `ui` | open the local dashboard and privacy preview |
| `update` | securely check, install, inspect, or roll back signed updates |
| `version` | print the installed csx version |
| `worker` | contribute Docker-isolated cross/matrix verification work |
<!-- END:CSX-CLI-SURFACE -->

The network also installs an optional **build-failure hook** into supported coding agents. When an agent's shell build fails, the hook classifies the failing build step, sanitizes the error locally, searches CodeSampleX, and stays silent on a miss or unrelated command. `csx hook status` shows what is installed; `csx hook check` proves the registered hook path with a throwaway failing build.

## Does it run there?

Every result is tied to the environment that produced it, so the same data can be pivoted into version × symbol, OS × runtime, version × architecture, browser/runtime, libc, and other recorded dimensions. Use the [live compatibility explorer](https://codesamplex.dev/compatibility) or a package page such as [pgx/v5](https://codesamplex.dev/golang/github.com%2Fjackc%2Fpgx%2Fv5) for current counts; the README deliberately does not freeze live network numbers into a dated screenshot. The shape below is illustrative only:

```text
                                         v5.10.0     v5.9.2
github.com/jackc/pgx/v5                  ▤ —         —
Batch                                    ▤ —         —
```

A matrix cell carries **observations** and, when available, a sample document. Observation counts say what recorded builds reached — compile, typecheck, test — and are counts of neither users nor machines. The sample document says that a reproducible contract exists at that coordinate; its state reflects those sample verification runs. Those two evidence classes are deliberately never added together.

A project compiling is therefore never presented as an individual symbol working. `—` remains unknown, and a missing sample remains a missing sample. The map records what happened at the coordinate instead of promoting a small set of runs into a universal `PASS` verdict.

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

On Windows the installer keeps the stable launcher at `%LOCALAPPDATA%\csx\csx.exe` and adds `%LOCALAPPDATA%\csx` to the user PATH. On macOS/Linux the default is `${CSX_INSTALL_DIR:-$HOME/.local/bin}/csx`; if that directory is not already on PATH, add it:

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # install check — the supported spelling is `csx version`
```

One binary, one question. `csx init` shows the contract below and asks a single choice — **JOIN COMMUNITY** or **LOCAL ONLY**. Piped into `sh`, stdin is consumed by the download pipe, so `init` takes the advertised default: JOIN COMMUNITY. Opt out any time with `csx init --local-only`; both mode flags are re-runnable and non-interactive. For scripted or CI setups: `csx init --community --yes --no-agents`.

## Test and check

```bash
csx run -- pnpm build                 # wrap a build/test; its result becomes sanitized evidence
csx search "axios multipart upload"   # verified answers graded for YOUR environment
csx --debug search "axios multipart upload" # same answer plus local decision trace
csx hook status                       # see which supported coding-agent hooks are installed
csx hook check                        # prove the failure-hook path with a throwaway failing build
csx scan                              # record public dependency usage without running a build
csx stats                             # local dashboard: cache, hits, adoption, queues
csx ui                                # browser dashboard + privacy preview
csx sync                              # manually refresh shards / flush queues when needed
csx mcp-config                        # print MCP client configuration
csx update check                      # verify whether a signed update is available
```

In **community mode**, `csx init` already performs a bounded first cache warm and starts the background sync daemon. A healthy new install should not need a ritual `csx sync` before its first question. If the installer says the warm was partial or unavailable, `csx sync` is the explicit recovery path; `csx sync --uploads-only` flushes queued uploads without doing a shard rescan.

`local-only` is intentionally different: automatic network access is disabled, so it may have a cold compatibility cache by design. Re-running `csx init --community` is the explicit way to join the network later.

`csx search` grades every result against the recorded target environment — `EXACT`, `COMPATIBLE`, `ADAPTATION_REQUIRED`, `REFERENCE_ONLY`, or `NO_SAFE_MATCH` — and lists the concrete environment delta rather than pretending a nearby run is identical.

### Automatic failure lookup

The `hook` path is the part an agent should not have to remember. For supported coding agents, a failed shell build can be routed through CodeSampleX automatically. The hook only intervenes for recognized build/test commands, sends no raw log, and says nothing when there is no safe match. Use `csx hook off` to disable it, and `csx hook check` whenever you want measured proof that the registered hook still fires.

## Verified samples

A sample is not a snippet. It is a minimal, content-addressed project (`sha256:<hex>` of its canonical artifact) with a **contract**: assertions that were executed offline in a pinned container and passed. Pinned by image digest, not by tag — the tag is an alias for readers, the digest is what runs — and the signed receipt names the exact image reference, so anyone can re-run the same bytes rather than take the word for it ([docs/adapters.md](docs/adapters.md#verifier-images)). The clean-room authoring loop is CLI-only:

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief + scaffolded workspace
csx sample create <dir>      # ingest the clean-room project
csx sample preview <id>      # show EVERYTHING that would be published
csx sample verify <id>       # resolve → compile → contract, sandboxed
csx sample publish <id>      # requires typing exactly "yes"; leakage findings hard-refuse
csx sample pending           # list agent-prepared drafts waiting for human review
```

Use `csx login github` if you want sample publication attributed to your GitHub identity; anonymous publishing remains an explicit CLI choice. Publishing scans for secrets, paths, project names and private URLs — findings **block** publication with no override flag. Uploading sample source is deliberately not an MCP capability; only a human at the CLI can publish.

## Findings

Where does it break? [Findings](https://codesamplex.dev/findings) is the measured contradiction list: what the documentation (or common belief) says, next to what the contract measured — documentation mismatches, environment-specific failures, version boundaries. Every line links to the published sample whose contract proves it, so you can re-run the measurement and disagree.

Machine-derived findings grow from published samples whose authors recorded the belief they correct; nobody edits a page to add them.


## Dependency atlas

[Dependencies](https://codesamplex.dev/dependencies) reads the recorded dependency graph from the child side: **which parent releases pulled this exact dependency release?** That is useful for blast-radius and upgrade questions that a package page cannot answer by itself. An edge means a resolver placed those releases together in an observed project; it is deliberately **not** presented as proof that the pair is compatible.

## Coverage gaps

[Gaps](https://codesamplex.dev/gaps) is the corpus-completeness view. It lists what each coordinate is still missing across three independent assets — **Sample, Evidence, Dependency** — rather than ranking only what somebody happened to search for. The demand queue still exists as `GET /v1/wanted` for automation and scheduling, but the public website uses `/gaps` because demand and completeness are different questions.

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
| `GET /v1/wanted` | demand queue API: what was asked for and not answered (the website completeness view is `/gaps`) |
| `GET /v1/adapters` | the per-ecosystem capability matrix |
| `GET /version` | which build of the server answered, and in which environment |

## Agent adapter (MCP)

Coding agents consume the same network through an adapter — MCP is a connector on top of the CLI and API, not the product:

```text
CodeSampleX
├─ CLI   ← primary local tester
├─ API   ← automation / integration
├─ Web   ← compatibility map / reports
└─ MCP   ← agent adapter
```

`csx init` configures Claude Code, Codex, Gemini CLI, Antigravity (agy), and OpenCode automatically. Any other stdio MCP client (Cursor, Windsurf, Cline, Zed, VS Code) works from what `csx mcp-config` prints (`--toml` for Codex) — it emits the absolute binary path, which a client started by an editor needs. The live contract is also rendered at [Features](https://codesamplex.dev/features). The server itself is `csx mcp`. Ten tools: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `report_anomaly`, `report_csx_issue`, `propose_public_sample`, `list_local_hits`, `get_local_stats` — and deliberately no publish tool.

`report_anomaly` is the one that points the other way. When a CSX answer and the agent's own machine **concretely** disagree — the network served a passing conclusion for a coordinate that failed here, a returned symbol signature is not what the package exports — the agent can file that as a verification request. It is not a bug report: a report queues an independent re-run on the same fleet that produces every other receipt, and only that receipt can confirm it. A submission with nothing measured behind it is refused, the same mismatch reported twice is one report and one re-run, and nothing a report says reaches any public page before a verifier agrees with it. The reporter's guess at the cause travels in its own field and never decides the verdict.

`report_csx_issue` is the same idea aimed at us rather than at a package: an answer that displaced the failure you were actually looking at, a recommendation from an ecosystem the question never mentioned, a tool contract that made a model behave wrongly. It is opt-in and deliberately quiet — nothing tells an agent to call it after a failure, no ticket is created, and a week with no reports is a normal week. A defect a hundred agents meet is one row whose occurrence count goes up, and once that row is linked to a bug every later report answers with the link. The two channels share ingest, redaction and dedupe and share nothing after it: a defect in this product can never become compatibility evidence.

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

**Observed evidence only** — project detection and build observation without container verifiers: Unreal Engine (`.uproject` via `adapters/unreal` — reports targeted engine versions as observed evidence on developer workstations; no container verification lane).

Honest capability matrix: [docs/adapters.md](docs/adapters.md) — symbol resolution confidence is always labeled (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Architecture

Single Go binary (`csx`: daemon + CLI + MCP + peer node + verifier) and a small server (`csx-server`: PostgreSQL + server-rendered compatibility explorer behind Caddy). Samples are content-addressed and distributed local-cache-first → peers → main seeder. Downloaded samples never run on the host directly: resolve runs in a pinned sandbox with install scripts disabled where the ecosystem supports it, the artifact is re-hashed after resolve, and compile/contract stages run network-off. Current canonical references are [docs/architecture.md](docs/architecture.md), [docs/schema.md](docs/schema.md), [docs/execution-context.md](docs/execution-context.md), [docs/diagnostics.md](docs/diagnostics.md), [docs/operations.md](docs/operations.md), and [docs/activation-funnel.md](docs/activation-funnel.md) — the latter being what may and may not be measured between install and a first useful answer, and why unique/active users are not on that list. [goal.md](goal.md) is the initial product plan.

## Operating model, principles, and Public v1 scope

These are standing product decisions carried over from the initial plan ([goal.md](goal.md) — now a deprecated stub; the full plan text lives in git history). Code and the documents above win wherever the implementation has evolved past the plan.

**Operating model.** The public network is free to join. Operation is funded by sponsorship (the GitHub Sponsors model in the initial plan; the site footer carries the Sponsor link). The plan reserves a possible future paid Hosted API for API-only consumers who contribute no evidence, storage, or verification — that tier is deliberately outside Public v1.

**Non-negotiable principles.** Breaking any of these requires the project owner's explicit approval:

- Real project source is never transmitted automatically, and automatic evidence covers packages on public registries only.
- Community peers are consumers and producers at once — that is the protocol, not hidden telemetry.
- The Evidence network and the Sample pool stay independent systems, and a project compiling is never presented as an individual API succeeding.
- Unknown causes stay `UNKNOWN`; `NO_SAFE_MATCH` beats a forced HIT.
- The product's default behavior depends on no server-side LLM inference and no central build farm, and local features keep working, as far as they can, while the central server is down.
- Publishing sample source and contributing idle-CPU verification each require their own explicit consent.
- A sample without a contract never climbs past L2 (compiled) on the verification ladder.
- No blockchain, token economy, or global ledger — ever.
- The core client and the public protocol stay open source.

**Out of scope for Public v1** (initial-plan scope decision; where the shipped product has since gone further — e.g. the verified-only ecosystems above — the shipped product wins): enterprise/private package networks, SSO/SLA/on-premise, API-only billing, dedicated IDE extensions for every editor, full shell interception for every agent, runtime symbol instrumentation for every language, Android/iOS/Unreal/Unity/C++ verifiers (note: Unreal Engine project detection and engine-version observation are supported via `adapters/unreal`), a central large-scale build farm, generic project memory or architecture/business-logic sharing, automatic source publication, definitive failure-cause verdicts, and a DHT-only network.

**Success metrics.** Success is not judged by sample counts or sign-ups. The metrics the initial plan commits to: agent search invocation rate, Precision@1, accepted-HIT rate, post-HIT build pass rate, adaptation distance, evidence coverage, cross-verification rate, failure-attribution confidence, and estimated reasoning avoided — the last explicitly an estimate, never a measured count (see the stats table above).

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
