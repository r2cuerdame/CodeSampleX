# Issue #531: public read latency triage (2026-10-05)

## Deployment attempt for phase timing (13:06 UTC onward)

Production still served `v0.2.1 / a6ae2ecb5900f8719e70bb23aabfadd49b4437aa`
at the start of this attempt; all four diagnostic routes returned 200 without
`Server-Timing`. The merged #532 instrumentation is on main commit
`560dfe0f28dce124085e98fd0b3ab128b4a19a9e`. We tagged that commit
`v0.2.2` to follow the repository's existing Release → Farm → Production
deploy path. Main CI [run 37309608306](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37309608306)
passed. Release [run 37314315137](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37314315137)
passed Windows tests, Linux tests/build, signing, security scan, Windows
bootstrap, and GitHub release asset verification. Its MCP Registry publish
step failed with HTTP 504 Gateway Time-out after roughly 60 seconds in both
attempts 1 and 2; the Farm job was skipped. A direct read-only request to the
Registry API also timed out, while its website root returned 200. On attempt
3, the Registry returned HTTP 400 `invalid version: cannot publish duplicate
version`, showing that `v0.2.2` had been accepted despite the lost response.
The same main commit was tagged `v0.2.3`; its normal Release
[run 37317101157](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37317101157)
passed, including an acknowledged Registry publish and verified
[Farm rollout](https://github.com/r2cuerdame/CodeSampleX-Farm/actions/runs/37319107246).

Production deploy
[run 37319704107](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37319704107)
passed eligibility but failed in the staging phase at the existing
`deploy/lightsail/deploy.ps1` release identity check: **"deployment revision
must have exactly one canonical release tag"**. Both `v0.2.2` and `v0.2.3`
point to the same main commit, so the check correctly refused to choose
between them. Its rollback succeeded, and the retained deployment evidence
reports `health: ok`, `servedRevision: a6ae2ecb5900f8719e70bb23aabfadd49b4437aa`
and `rollback: succeeded`. A fresh public `/version` check also returned the
prior `v0.2.1 / a6ae2ecb...` revision. No new server revision was activated.

The safe route to reconcile the public tags and release gate needs an owner
of deployment policy outside this latency Issue. Removing a published tag or
weakening the exact-release check here would expand the Issue and risks
serving an unverified download generation. Until instrumentation is deployed,
the required 30 paired external/`Server-Timing` samples per route cannot be
collected. The latency cause, RED→GREEN fix, and independent QA PASS are
therefore still unknown and unmet.

## Observation

The production SLO probe's 20-request runs on the unchanged
`v0.2.1 / a6ae2ecb5900f8719e70bb23aabfadd49b4437aa` deployment reported
the following server-time p95 values (seconds). The controls include
`/version`, which reads only process build stamps and does not access the
database, blob store, or clock.

| UTC date | `/healthz` (target .3731) | `/v1/stats` (target .3425) | `/v1/shards/npm/zod/3` (target .4155) | `/version` (target .3311) |
| --- | ---: | ---: | ---: | ---: |
| Oct 1 | .923 | .831 | .928 | .952 |
| Oct 2 | .526 | .449 | .612 | .453 |
| Oct 3 | .613 | .444 | .594 | .363 |
| Oct 4 | .340 | .421 | .541 | .518 |
| Oct 5 (GitHub Actions) | .449 | .449 | .531 | .437 |

Sources: the `github-actions` measurements in [#526](https://github.com/r2cuerdame/CodeSampleX/issues/526), [#527](https://github.com/r2cuerdame/CodeSampleX/issues/527), [#528](https://github.com/r2cuerdame/CodeSampleX/issues/528), and the unrelated static-route control [#529](https://github.com/r2cuerdame/CodeSampleX/issues/529). These are all the same GitHub Actions vantage and deployment revision. One 20-request p95 is the nineteenth ordered observation, so these repeated runs are stronger evidence than a single tail sample.

At 12:59 UTC on Oct 5, the same `scripts/perf-slo.py measure` ran for the
normal 20 rounds from this worker's Windows/KR vantage. Its [raw result](issue-531-live-probe-2026-10-05.json)
records the unchanged `v0.2.1 / a6ae2ecb...` deployment before and after,
20 successful requests per path, and server-time p95 of .3495 (`/healthz`),
.2042 (`/v1/stats`), .3883 (`/v1/shards/npm/zod/3`), and .2331 (`/version`)
seconds. All four were below their existing targets. This is a different
vantage from GitHub Actions and a later window, so it cannot establish a
production recovery. It does show that the same deployed revision can return
within target in a full run, while the GitHub Actions run at 10:40 UTC reported
four violations. All four routes had `appTimingCount: 0`, confirming that the
current production revision does not expose this branch's app timing yet.

I also ran the repository's read-only `scripts/perf-slo.py measure` on Oct 5
at 10:05 UTC from the worker's Windows/KR vantage, six rounds through the five
configured paths. The deployment was still `v0.2.1 / a6ae2ecb...` before and
after. All responses were 200. Observed median/p95 server seconds were:

| Path | Median | p95 | TCP RTT median |
| --- | ---: | ---: | ---: |
| `/healthz` | .305 | .745 | .248 |
| `/v1/stats` | .237 | .475 | .244 |
| `/v1/shards/npm/zod/3` | .422 | .716 | .259 |
| `/version` | .232 | .352 | .248 |
| `/v1/verification/jobs?...` | .435 | 1.284 | .252 |

Six samples make p95 the **maximum**, and this vantage differs from GitHub
Actions. This small run corroborates common-route latency; it is not a new SLO
verdict or a replacement for the 20-request production history.

## Route audit and limit

- `/healthz` uses the reserved probe database class and shares a successful
  health read for one second. The store call is `GetLatestStats`.
- `/v1/stats` keeps the builder's rollup in memory for its snapshot interval;
  repeated probe requests within that interval do not each query PostgreSQL.
  Its optional hot-shard hint is separately bounded.
- `/v1/shards/npm/zod/3` reads a materialized document by the shard table's
  primary key. ETag revalidation reads only the ETag, though this SLO probe
  sends an unconditional GET.
- `/version` computes a tiny JSON object from in-process build stamps. Its
  repeated p95 violations rule out a database query shared by the three
  target handlers as the sole explanation.

The shared delay could be host CPU/steal, Go scheduling, Caddy/upstream
queuing, or a network effect left in the probe's `request-to-first-byte minus
one TCP RTT` estimate. Public HTTP samples cannot distinguish those layers.
There is no established route-specific code defect to write a meaningful
baseline-RED/head-GREEN regression test for. Changing one of these handlers,
its latency budget, or the SLO target without that diagnosis would not
establish a fix.

## Evidence needed to resume

The diagnosis was split into [#532](https://github.com/r2cuerdame/CodeSampleX/issues/532).
Its [merged PR #533](https://github.com/r2cuerdame/CodeSampleX/pull/533)
adds `Server-Timing` phases (`middleware`, `db_wait`, `query_handler`,
`serialize`) for the four routes. This supersedes the earlier instrumentation
committed on this Issue branch; the branch will merge that implementation from
main rather than publish two overlapping timing layers. The full Oct 5 probe
still saw no app timing because production was serving `a6ae2ecb...`, not
PR #533. #532 is closed, but its requested production deployment and 30
paired samples per path have not been recorded on the Issue as of this check.

Until those paired samples exist, the route violations cannot be attributed
to a code, host, edge, or network defect with confidence; the `/version`
control argues against a database-only fix. A baseline-RED/head-GREEN
regression for an identified latency defect and independent QA PASS remain
outstanding for this Issue. The prior branch's timing-header tests established
instrumentation only, **not** a repaired p95 SLO.

Correlate the same probe windows with host CPU/steal and memory pressure,
server process CPU/GC/scheduler pauses, PostgreSQL pool wait and query times,
and Caddy upstream timing versus client-side TCP/TLS/request phases. Then
decide whether this is capacity/edge contention, a shared runtime defect, or
a probe-baseline policy problem. No production mutation or paid capacity is
needed for this read-only diagnosis.

## Resumed diagnosis after #534 (2026-10-05 16:06–16:40 UTC)

The [#534 fix](https://github.com/r2cuerdame/CodeSampleX/pull/535) was on
main at `a9fe24839bae5dfda10da04dcd7d6cb99e3674c7`. Its main
[CI run 37335864101](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37335864101)
passed. The new `v0.2.4` tag points at that exact commit. The normal
[Release run 37338225311](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37338225311)
passed Windows/Linux tests, build, signing, publication, and the verified Farm
rollout. The existing
[Production deploy run 37340865409](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37340865409)
passed eligibility and rollout. Its retained artifact reports target, deployed,
and served SHA all `a9fe2483...`, health `ok`, smoke `pass`, rollback
`not-needed`, and migration `0050_builder_status.sql`. Public `/version` then
returned `v0.2.4 / a9fe2483...`, so the four-route measurements below are on
the instrumented deployment, not on the earlier `v0.2.1` revision.

The worker's Windows/KR probe sent 30 successful GETs per route round-robin,
with a fresh TCP/TLS connection and 0.5-second spacing. It used the SLO
formula `request-to-first-byte - TCP connect` and captured the four
`Server-Timing` phases on **all 120 responses**. Raw per-request timestamps,
client timings, phases, and the before/after revision are in
[the 30-round result](issue-531-phase-probe-a.json). The independent
[GitHub Actions post-deploy run 37341878929](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37341878929)
used the existing 20-round SLO script on the same revision; its
[result artifact](issue-531-postdeploy-gha.json) is copied here. Values are
nearest-rank p95 seconds, except the application column, which is milliseconds:

| Route | Target s | Worker server s | Worker app ms | Worker residual ms | GitHub Actions server s |
| --- | ---: | ---: | ---: | ---: | ---: |
| `/healthz` | .3731 | .4286 | 237.796 | 271.165 | 1.0759 |
| `/version` | .3311 | .3267 | .050 | 326.667 | .2926 |
| `/v1/stats` | .3425 | .2733 | 76.930 | 272.252 | .3484 |
| `/v1/shards/npm/zod/3` | .4155 | 1.0494 | 400.154 | 277.065 | .7358 |

`app` is the sum of the instrumented `middleware`, `db_wait`,
`query_handler`, and `serialize` durations. `residual` is client-estimated
server time less that sum, per request. Each column's p95 may come from a
different request, so the columns must not be added. `/version` runs without
a store read: its app p95 was only 0.050 ms while its residual p95 was
326.667 ms. The residual can include Caddy/host scheduling and error in the
one-TCP-RTT network subtraction; this evidence does not separate them.

The database pool did not explain the store-backed tails: `db_wait` p95 was
0.003 ms for `/healthz` and 0.004 ms for the shard route. Their
`query_handler` p95 values were 237.785 ms and 400.052 ms, respectively.
`/healthz` calls `GetLatestStats` and reads the current stats document rather
than only checking connectivity. The shard route reads a materialized JSONB
document by primary key; this document returned 151,460 bytes. These facts
locate latency in the store read and its host execution, but do not establish
whether SQL, storage, CPU contention, or Go scheduling made it slow.

To check whether the full shard body alone causes the violation, a second
[30-pair result](issue-531-shard-pair.json) alternated unconditional GET
with `If-None-Match` against the same ETag. All 30 full responses were 200
with 151,460 bytes; all 30 revalidations were 304 with zero body bytes. The
full path's client/server-estimate p95 was .4639 s and app p95 167.995 ms.
Even the ETag-only path had .4539 s and 80.546 ms, with `db_wait` p95
0.003 ms. One full GET took 1.8601 s externally while app timing was only
3.647 ms. Thus reading/serializing 151 KB is not the sole cause, and the
primary-key ETag read plus transport still have substantial tails.

There is no isolated code defect yet for a baseline-RED/head-GREEN regression.
Changing the health check, caching the shard, or adjusting the SLO target
without isolating the source would leave the observed ETag and static-route
residuals unexplained. The running
[post-deploy observation 37341878738](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37341878738)
may provide host CPU-steal and pool-pressure evidence. PostgreSQL execution
time, host CPU/steal, Go scheduler pauses, and Caddy upstream timing were not
available to this read-only public probe. Independent QA PASS and the required
RED→GREEN regression are still unmet; a handler-only patch is not justified
by these measurements.
