# Issue #433 independent operations baseline

Recorded on 2026-09-15 14:34–15:16 UTC (2026-09-15 23:34–2026-09-16 00:16 KST).
This is an independent, read-only operations baseline. It does not use the FABLE
or OPUS issue-433 audit artifacts, does not change production or the Farm, and
does not propose or implement a fix. Local GitHub artifact downloads and browser
screenshots were placed only in temporary directories and are not part of this
commit. Credentials and raw SQL/request text were neither printed nor retained.

## Executive result

The latency problem is live and asymmetric. In one bounded route-by-route probe,
the public pages and APIs returned HTTP 200 with 154–648 ms TTFB, while the
authenticated `/admin` page took 8.963 s to first byte and the authenticated
`/admin/api/farm` read exceeded the 15 s client deadline. A prior composite
probe also exceeded its 12 s deadline before it could attribute the slow route.

At essentially the same time the 2-vCPU host showed a run queue of 15–17, 0%
idle and 80–82% steal in the two interval samples. PostgreSQL had 11 active
backends (four waiting on `DataFileRead`), and the private pool panel showed
578 interactive pool refusals, 22 interactive statement timeouts, and a 19.597 s
maximum background acquisition wait during the server's first 10m43s. These
measurements prove contention and user-visible admin latency, but they do not
prove that the external Farm is the cause: the privacy-safe query-family
attribution query itself hit its 5 s statement limit, the most recent Farm SQL
diagnostics are unavailable due to command/SQL timeouts, and the long-lived
backend recorded during the latest failed deployment had an empty
`application_name`.

The repository and GitHub release path is working up through release for current
`origin/main` (`8e4f4fc4c660d3176658faf3a1d0f058d091974f`, tag `v0.1.191`), but the
production rollout of that SHA failed and performed a proved exact rollback.
Live `/version` still serves the previous known-good `8e822f11766b0ebb23a0b756c85f6be5e0d07247`
(`v0.1.189`), and `/healthz` returned `ok`.

## Scope, provenance, and unavailable inputs

| Item | Timestamp / proof | Result |
|---|---|---|
| Repository baseline | `2026-09-15T15:05:24Z`; `git fetch --no-tags origin main`, then `git rev-parse HEAD origin/main` | HEAD and freshly fetched `origin/main` were both `8e4f4fc4c660d3176658faf3a1d0f058d091974f`; worktree was clean before this report. |
| DevHotel MCP | `2026-09-15T14:35:46Z`; session tool inventory plus `codex mcp list` | Unavailable. No DevHotel tool was exposed, and the configured-server list contained `codex_app`, `csx`, `cua_repl`, `node_repl`, and `openaiDeveloperDocs`, but no DevHotel entry. No fallback test environment was mutated. |
| RDC | `2026-09-15T14:34:38Z`; RDC `ping` | Available (`pong`) and used first for repository, GitHub, browser, host, and DB inspection. |
| Browser harness | `2026-09-15T14:42:42Z`; `python scripts/admin-playwright.py --production --baseline --out <temp>` | Existing read-only production baseline passed and captured the authenticated dashboard. Chromium launch through completion took 10.64 s; this duration includes browser startup and is not presented as page TTFB. |
| CodeSampleX MCP | Before using Playwright, `search_known_solution` for `pkg:pypi/playwright` and `sync_playwright` / `Page.goto` / `APIRequestContext.get` | `NO_SAFE_MATCH`; the existing repository harness was used and its result was measured locally. |
| FABLE/OPUS issue-433 artifacts | Audit boundary | Not opened, searched, or used. Issue #433 itself was read only to establish the requested scope. |

## Live low-impact latency reproduction

The detailed route probe used one sequential GET per route, no concurrency, no
query strings, a 15 s client timeout, and `ResponseHeadersRead` so `ttfb_ms`
ends when response headers arrive. The authenticated client obtained the
existing DPAPI-protected admin credential in memory; the credential was not put
in argv, output, or an artifact.

Window: `2026-09-15T14:45:44.721Z`–`2026-09-15T14:46:11.304Z`.

| Surface | Status | TTFB | Total | Body bytes |
|---|---:|---:|---:|---:|
| `/healthz` | 200 | 639.4 ms | 644.6 ms | 2 |
| `/` | 200 | 154.1 ms | 314.7 ms | 50,825 |
| `/v1/stats` | 200 | 238.5 ms | 317.2 ms | 43,243 |
| `/v1/wanted` | 200 | 160.6 ms | 163.1 ms | 21,302 |
| `/golang/github.com/jackc/pgx/v5/v5.10.0` | 200 | 647.6 ms | 960.5 ms | 64,809 |
| `/admin` (authenticated) | 200 | 8,963.2 ms | 9,112.7 ms | 40,752 |
| `/admin/api/farm` (authenticated) | client timeout | >15,019.4 ms | unavailable | unavailable |

An immediately preceding sequential composite measurement (`14:44:00Z`) hit a
12 s `HttpClient` deadline and aborted before accumulated per-route rows were
printed. It is retained only as evidence that the slow behavior reproduced; it
cannot safely be assigned to a route. The successful Playwright baseline at
`14:42:42Z` and the later 8.963 s admin TTFB show that the admin surface was
reachable, not continuously down.

At `2026-09-15T15:04Z`, independent read-only RDC URL reads returned:

```text
/healthz: ok
/version: {service: csx-server, version: v0.1.189,
           revision: 8e822f11766b0ebb23a0b756c85f6be5e0d07247,
           environment: production, builtAt: 2026-09-15T09:34:20Z}
```

## Host and database state

### Host pressure

Read-only SSH sample at `2026-09-15T14:54:54Z`:

```text
uptime load average: 10.18 8.24 8.09
memory: 1906 MiB total, 1494 used, 80 free, 411 available
swap:   2047 MiB total, 153 used

vmstat interval rows (r b, free KiB, us sy id wa st):
17 0, 81856, 15 3 0 0 82
15 0, 81584, 16 4 0 0 80
```

The first `vmstat` row was the since-boot average (45% steal) and is not mixed
with the two one-second interval rows above. On the documented 2-vCPU Lightsail
bundle, a run queue of 15–17, zero idle, and 80–82% steal is direct host CPU
pressure. This is a point-in-time observation, not a duration or root-cause
assignment.

At `2026-09-15T14:56:23Z`, all three containers were running and the server and
database were Docker-healthy. Raw `docker stats --no-stream` values were:

| Container | CPU | Memory | Limit share | PIDs |
|---|---:|---:|---:|---:|
| Caddy | 70.49% | 92.71 MiB / 1.861 GiB | 4.86% | 11 |
| server | 388.75% | 588.7 MiB / 768 MiB | 76.65% | 11 |
| PostgreSQL | 695.74% | 484.8 MiB / 640 MiB | 75.75% | 17 |

Docker CPU percentages are recorded raw. They are not converted into physical
CPU use because the simultaneous steal signal makes that conversion unsafe.

### Pool admission, waits, timeouts, and request fan-out

The authenticated dashboard baseline at `2026-09-15T14:42:52Z` reported process
uptime 10m43s, DB read `<1 ms`, pool occupancy `5 / 8`, and `pressure present`.
These are process-lifetime counters, not request counts:

| Class | In use / displayed cap | Attempts | First | Explicit retries | Follow-ups | Acquired | Waited | Max wait | Pool rejects | Follow-ups stopped | Caller cancels | Other failures | Statement timeouts |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Interactive/user read | 2 / 2 | 5,009 | 4,870 | 0 | 139 | 4,423 | 2,132 | 624 ms | 578 | 6 | 2 | 0 | 22 |
| Collection/aggregation | 3 / 4 | 1,918 | 1,528 | 2 | 388 | 1,899 | 140 | 19,597 ms | 0 | 0 | 19 | 0 | 9 |
| Health probe | 0 / 1 | 58 | 58 | 0 | 0 | 58 | 0 | unavailable | 0 | 0 | 0 | 0 | 0 |

The 527 total follow-up acquisitions (139 interactive plus 388 background) are
the available in-process fan-out signal. They do not identify endpoint-level
fan-out, and one request may contribute multiple acquisitions. The six stopped
interactive follow-ups prove the guard terminated additional work after a pool
refusal. The healthy probe reserve explains why `/healthz` can stay green while
the admin/Farm surfaces are slow.

### PostgreSQL activity and locks

At `2026-09-15T14:58:11Z`, a fixed, 5-second-bounded query returned:

| State / wait | Connections | Oldest query/backend age |
|---|---:|---:|
| active / no wait event | 7 | 378.961 s |
| active / `IO:DataFileRead` | 4 | 35.601 s |
| idle / `Client:ClientRead` | 2 | 1.041 s |

All 122 reported locks were granted; there were no ungranted rows in the fixed
`pg_locks` aggregation. Therefore this sample shows CPU/I/O work and does not
show a lock waiter. It does not exclude locks outside the instant sampled.

### Sequential scans and cumulative statement cost

`pg_stat_user_tables` at `2026-09-15T14:58:11Z` showed the following leading
cumulative scan counters:

| Table | seq_scan | seq_tup_read | idx_scan | live rows | dead rows |
|---|---:|---:|---:|---:|---:|
| `sample_packages` | 3,784,331 | 25,063,436,313 | 1,889,102,824 | 8,677 | 92 |
| `evidence_agg` | 255,375 | 9,404,233,946 | 276,019,564 | 318,448 | 18,657 |
| `samples` | 2,576,486 | 7,557,234,881 | 1,810,901,083 | 8,346 | 321 |
| `receipts` | 1,352,809 | 6,951,172,864 | 347,835,792 | 10,966 | 1,706 |
| `wanted` | 3,278,900 | 3,710,153,594 | 6,490,593 | 4,681 | 641 |
| `dependency_edge` | 123,729 | 3,372,922,267 | 5,981,539 | 109,701 | 17,703 |
| `compatibility_snapshots` | 86,822 | 1,203,569,848 | 84,181,828 | 21,692 | 4,731 |
| `verification_jobs` | 153,411 | 968,097,159 | 1,490,995 | 13,166 | 1,842 |
| `packages` | 188,187 | 715,070,861 | 271,761,329 | 6,330 | 1,060 |
| `authoring_drafts` | 126,734 | 585,523,588 | 23,022,291 | 7,222 | 60 |

The database-level `stats_reset` value was null, so a precise table-counter
window start is unavailable. These values prove substantial cumulative
sequential work but are not a rate and do not identify the current slow route.
At `15:06:24Z`, the DB cumulative totals included 824,220,555 blocks read,
32,074,582,484 hits, 1,049,676 temp files, 4,955,796,675,209 temp bytes, and
three deadlocks. `pg_stat_statements_info.stats_reset` was
`2026-09-09T05:49:06.497517Z`.

The top privacy-safe `pg_stat_statements` rows at `14:59:57Z` were numeric only:

| queryid | calls | total ms | mean ms | max ms | I/O ms | temp blocks |
|---:|---:|---:|---:|---:|---:|---:|
| -8919612395518868614 | 338,014 | 96,247,208.9 | 284.7 | 7,999.7 | 83,106,868.5 | 0 |
| 5126368844612351286 | 10,442,964 | 66,667,371.9 | 6.4 | 5,847.8 | 2,291.3 | 0 |
| -1034863950460726248 | 20,051,213 | 41,580,045.4 | 2.1 | 787.1 | 23,074,273.9 | 0 |
| -1838375435808251149 | 193 | 35,664,329.6 | 184,789.3 | 444,552.2 | 23,895,794.4 | 10,073,582 |
| 5754581964168977175 | 2,126 | 22,224,468.5 | 10,453.7 | 22,855.1 | 9,678,375.3 | 0 |
| -4616489319632358898 | 2,341,720 | 15,098,617.5 | 6.4 | 1,518.7 | 2,217,453.3 | 0 |
| -827627249904167911 | 2,525,592 | 14,233,546.2 | 5.6 | 36,715.4 | 9,793,521.0 | 0 |
| -8292064883623834489 | 7,717 | 13,403,446.0 | 1,736.9 | 9,374.0 | 180,359.9 | 0 |
| -8060285611411695903 | 1,059 | 10,741,022.7 | 10,142.6 | 22,814.4 | 3,002,085.7 | 0 |
| 2969576699904556945 | 434,308 | 9,945,815.5 | 22.9 | 4,718.3 | 4,535,064.3 | 0 |

The repository's fixed `scripts/pg-slow-queries.py` attribution catalog maps
SQL text to static source labels inside PostgreSQL and returns only labels and
metrics. Re-running its six-family `CASE` classification with a 5 s statement
timeout at `2026-09-15T15:07:41Z` was canceled by PostgreSQL. The raw query text
was not exported, and the attribution is therefore **unavailable**, not guessed.

## Existing diagnostic evidence

### Successful deployment followed by failed independent observation

Production deploy run **34953128122** successfully deployed
`8e822f11766b0ebb23a0b756c85f6be5e0d07247` at 09:33–09:40 UTC. Its retained
artifact `production-evidence-34953128122` records `health=ok`, `smoke=pass`,
`rollback=not-needed`, migration `0041_anonymous_credential_adoption.sql`, and
server start `2026-09-15T09:39:31.766870854Z`.

The automatically triggered post-deploy observation run **34953824709** then
failed incident-only (09:40–09:47 UTC), explicitly set
`rollbackRequested=false`, and did not mutate or roll back production. Its
retained artifact reports:

- peak server CPU 374.08%, peak server memory 51.61%, and peak load1 10.76;
- 37 DB-pressure lines in the observation window: 29 pool-busy and 7
  query-timeout lines, with maximum logged wait 0.396 s;
- retained server counters of 41 pool-busy, 9 query-timeout, 1 admission-refused,
  and 3 deferred-refused events;
- zero OOM, restart, and container-die events;
- an active-builder latency round where `/healthz`, `/`, `/v1/wanted`, the otel
  package page, and the sample page returned 200 at 0.638–1.523 s TTFB, while
  the pgx package page returned HTTP `000`; a later sample of that page returned
  200 at 3.035 s TTFB;
- only one of five required active-builder rounds, and a terminal sample that
  did not remain settled, so convergence evidence was insufficient.

This historical observation independently matches the current pool/CPU/latency
pattern. It does not identify the originating SQL or prove Farm causality.

### Current-main rollout failure and exact rollback

Production deploy run **34981234895** (current main) passed every eligibility
step, then failed `Deploy and verify`. The retained artifact records:

- target/operational SHA `8e4f4fc4c660d3176658faf3a1d0f058d091974f`;
- previous and post-recovery served SHA
  `8e822f11766b0ebb23a0b756c85f6be5e0d07247`;
- failure `database clients remain after stopping the builder` during the
  36.885 s quiescence phase;
- one rollback server backend with empty `application_name`, server container
  address `172.18.0.2/32`, backend start `14:21:33Z`, query start `14:24:19Z`,
  and privacy-safe query hash `30ae449e1a2205810c65bad8f56080de`;
- helper/recovery cleanup passed; `rollback-server.sh` and `rollback-caddy.sh`
  passed; top-level `rollback=succeeded`, `health=ok`, and `smoke=not-started`.

This proves a server-container DB client outlived builder shutdown and blocked
quiescence. The empty application name prevents a responsible Web/Farm/
aggregation lane from being assigned. Post-deploy run **34982412012** was
correctly skipped because the deployment was not successful.

### Farm and authoring diagnostics

| Run | Window / scope | Result |
|---:|---|---|
| 34652398407, `Farm SQL phase diagnostic` | 90 s active SQL-family sampler; 2026-09-11 | `availability=unavailable`, `failureClass=command_timeout`; no identity, capability, window, or sample data. |
| 34925082405, latest `Farm scalar throughput` | fixed one-hour scalar read; 2026-09-15 03:28 UTC | `availability=unavailable`, `failureClass=command_timeout`, `failureStage=sql_read`; counts and identity unavailable. |
| 34663575432, prior successful scalar throughput | 2026-09-12 00:02–01:02 UTC | 3 accepted/3 PASS receipts; no attributed generated drafts; `cleanWindowEligible=true`. This is historical and cannot stand in for current throughput. |
| 34644571418, authoring funnel | retained current-container logs, 2026-09-11 19:30–20:30 UTC | 46 polls; wanted read/eligible sums 9,200/3,342; expansion 8,000/8,000; served `NO_WORK=6`, `WANTED=17`, `FINDING=0`, `EXPANSION=13`, `DEPENDENCY=10`; zero logged timeout/pool fallback events. Retention coverage was not proven. |

The current Farm SQL signals are thus unavailable with positive timeout proof.
Combined with the empty backend `application_name`, this prevents a defensible
Farm-versus-Web contention split. What is proven is contention on the shared
production host/database and much worse latency for admin/Farm aggregation than
for the sampled public routes.

## Exact PR, test, release, deploy, rollback, and health paths

### Canonical path and current IDs

| Stage | Authority / file | Current evidence |
|---|---|---|
| Change review and merge | GitHub ruleset **21240909** (`Protect main`) | PR **#432** merged as current main SHA. Ruleset requires a PR and status context `Test` (GitHub Actions integration 15368), but requires 0 approvals, does not require thread resolution, is non-strict, and repository-role actor 5 may always bypass; the current authenticated user reports `current_user_can_bypass=always`. |
| PR tests | `.github/workflows/ci.yml` (workflow ID **340227257**) | PR CI run **34977499400**: `Test` passed; `Windows` skipped by design for pull requests. `Test` runs vet, unit/contract tests, real PostgreSQL 17 integration tests with skip detection, and end-to-end pool-pressure tests. |
| Main tests | same workflow, triggered by push to main | Run **34978538035** for current SHA succeeded. On main both `Test` and the separate Windows job are authoritative. |
| Release decision | protected `v*` tag; ruleset **20977301** forbids tag update/deletion | `v0.1.191` points at current main. `.github/workflows/release.yml` workflow ID **333711079**, run **34978559507**, succeeded. |
| Release stages | `release.yml`: `release-ref` -> `windows-test` -> `build`; then `sign` -> `windows-bootstrap` -> `publish` -> `farm` (with `defender-scan` informational alongside) | Build retests and cross-compiles; only `sign` enters environment `codesamplex-release-signing` (environment ID **20083466876**); publish verifies the exact draft asset set and atomically publishes; `farm` dispatches `CodeSampleX-Farm/deploy.yml` and requires that exact tag rollout to succeed before the Release run can be green. |
| Production eligibility | `.github/workflows/production-deploy.yml` (workflow ID **341066397**), `eligibility` job | Manual dispatch only; serialized by `codesamplex-production`; requires immutable target and previous SHA, canonical main ancestry, deploy-gate verdict `pass`, `requires_human_decision=no`, safe/additive side-effect class, successful same-target main CI, successful same-target Release/Farm run, and a target-specific GitHub tracking issue. Current run **34981234895** passed eligibility. |
| Production mutation | same workflow, `deploy` job, environment `codesamplex-production` (ID **20467919712**) | Only this job receives the SSH identity. It calls `deploy/lightsail/deploy-production.ps1`, which wraps `deploy/lightsail/deploy.ps1` and the offline-migration host/controller protocol. Current rollout failed and rolled back. |
| Retained deploy authority | artifact `production-evidence-<run-id>` uploaded with `if: always()` | Current artifact proves failure/rollback; known-good run **34953128122** proves the currently served SHA. A log or comment alone is not deploy evidence. |
| Post-deploy observation | `.github/workflows/post-deploy-observation.yml` (workflow ID **352105123**) | Runs after a successful Production deploy/reconciliation or by explicit retry. It validates the deployment artifact, performs bounded latency/pressure/convergence reads, uploads `post-deploy-observation-<deploy-run>-<observer-run>`, comments on the validated tracking issue, and never deploys or rolls back. Current-main observation **34982412012** skipped; known-good observation **34953824709** failed incident-only. |

### Rollback implementation

- `deploy/lightsail/deploy-production.ps1:121-150` rejects previous-SHA drift,
  classifies rollback-critical outcomes, and re-reads current identity to prove
  the previous revision/image/config/environment was restored.
- `deploy/lightsail/deploy.ps1:508-557` snapshots present/absent compose/env,
  container run state, current image, `latest` tag, Caddy state, and release
  directory before activation. `deploy.ps1:956-1189` attempts independent
  server, Caddy, and credential rollback and fails if exact recovery cannot be
  proved.
- `deploy/lightsail/rollback-server.sh` restores compose/env/dist and exact
  image/container state, then requires the restored server `/healthz` to return
  `ok`; `rollback-caddy.sh` restores or removes Caddy state according to the
  predeployment markers.
- `deploy/lightsail/offline-migration.py:629-709` owns rollback cleanup for the
  detached host-side migration protocol and records `rolled-back` only after
  both rollback scripts succeed.

### Live-health authority

- `GET /healthz` is the reserved-pool liveness/readiness signal and currently
  returns `ok`.
- `GET /version` is the served-build identity signal and currently names the
  previous SHA `8e822f1…`; configured image labels alone are insufficient.
- `deploy.ps1` checks container health, proxy health, exact served revision, and
  representative public content inside the rollback boundary before commit.
- `post-deploy-observation.yml` owns the longer builder-convergence, resource,
  pool-pressure, OOM/restart/die, and active/post-settle TTFB observation. Its
  incident-only failure does not retroactively authorize rollback.

## Local verification of repository contracts

The command was run through CodeSampleX `run_observed_command` after the
read-only investigation:

```text
go test ./scripts ./deploy/lightsail
```

Result: overall exit 1. `github.com/r2cuerdame/codesamplex/scripts` passed in
8.314 s, covering the CI, release, production-deploy, post-deploy observation,
and related workflow contract tests. `deploy/lightsail` failed after 65.758 s
only in the Windows execution of `failclosed_test.go`: the fixture explicitly
reported `flock unavailable`, then Windows `timeout.exe` rejected the POSIX
arguments (`Invalid syntax`). No production code was changed to mask this
platform mismatch. The canonical Linux main CI run **34978538035** is green;
this local failure remains an unavailable Windows proof for the affected
remote-runner fixture, not evidence that the GitHub release/deploy contracts
failed.

## Reproduction commands (secrets redacted)

All production commands below are read-only. Replace placeholders from the
protected local/environment configuration; never paste credential values into
argv or logs.

```powershell
# Tool availability and repository identity
codex mcp list
git fetch --no-tags origin main
git rev-parse HEAD
git rev-parse origin/main
git status --short --branch

# GitHub state and retained evidence
gh issue view 433 --json number,title,state,url,createdAt,updatedAt,labels,body
gh api repos/r2cuerdame/CodeSampleX/commits/<sha>/pulls
gh pr checks 432 --json name,state,bucket,startedAt,completedAt,link,workflow
gh api repos/r2cuerdame/CodeSampleX/rulesets/21240909
gh api repos/r2cuerdame/CodeSampleX/rulesets/20977301
gh run list --workflow <workflow-file> --limit 10 --json databaseId,headSha,status,conclusion,createdAt,updatedAt,event,url
gh run download <run-id> -D <temporary-directory>

# Existing read-only browser regression (DPAPI secret remains in memory)
python scripts/admin-playwright.py --production --baseline --out <temporary-directory>

# Host pressure. Git for Windows ssh.exe was used because the Windows inbox
# ssh.exe returned exit 1 without diagnostics in this session.
ssh -i <production-key> -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes `
  -o UserKnownHostsFile=<pinned-known-hosts> ubuntu@<production-host> `
  'date -u; uptime; free -m; vmstat 1 3; docker ps; docker stats --no-stream'

# DB reads were base64-fed on stdin so no SQL or password appeared in argv.
# Each session began with statement_timeout=5s and lock_timeout=1s.
docker exec -i codesamplex-db-1 psql -X -v ON_ERROR_STOP=1 -U csx -d csx
```

The fixed DB statements aggregated only `pg_stat_activity` state/wait counts,
`pg_locks` counts, `pg_stat_user_tables` counters, `pg_stat_database` counters,
and numeric `pg_stat_statements` metrics/query IDs. No `query` column,
connection string, request body, raw log, or admin content was exported.

## Interpretation boundary and next evidence

This baseline establishes real contention, real admin/Farm API latency, current
pool refusals/timeouts, active I/O waits, and very large cumulative sequential
work. It also establishes that public routes can remain comparatively healthy
because the probe connection is reserved and interactive work is bounded.

It does **not** establish which SQL family caused the live interval, whether
steal is sustained beyond the two interval samples, or whether the external
Farm is competing with Web traffic. Closing those evidence gaps requires a
fresh successful Farm SQL-phase/throughput diagnostic, short counter deltas
rather than lifetime totals, and non-empty per-lane `application_name`
attribution. Those are follow-up investigation targets; no change is made here.
