# Issue #531: public read latency triage (2026-10-05)

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

The current branch adds `Server-Timing` at the application boundary and at
the health probe, stats read/hint, and shard lookup. `scripts/perf-slo.py`
records those phases per public request in its result artifact while retaining
the original alert metric. An assertion for all four routes failed on the
pre-change branch because `Server-Timing` was absent, then passed with this
instrumentation. The probe's fixture test likewise failed for a missing
`timingMs` field before passing after the collector change. These are
instrumentation regressions, **not** proof that the p95 SLO is repaired.

The app timing can be measured in production only after this branch is
deployed. Until paired samples exist, the Oct 1-4 route violations cannot be
attributed to a code, host, edge, or network defect with confidence; the
`/version` control argues against a database-only fix. The independent QA
verdict and a baseline-RED/head-GREEN regression for an identified latency
defect also remain outstanding.

Local verification on Oct 5: the focused `cmd/csx-server` SLO/health/API
tests passed; `go test ./internal/httpapi ./scripts` passed; and
`go build ./cmd/csx-server` passed. A broader run including
`cmd/csx-server` and `deploy/lightsail` integration tests failed where those
tests attempted the configured PostgreSQL at `127.0.0.1:5433`: connection
refused. That run does not establish an application test failure.

Correlate the same probe windows with host CPU/steal and memory pressure,
server process CPU/GC/scheduler pauses, PostgreSQL pool wait and query times,
and Caddy upstream timing versus client-side TCP/TLS/request phases. Then
decide whether this is capacity/edge contention, a shared runtime defect, or
a probe-baseline policy problem. No production mutation or paid capacity is
needed for this read-only diagnosis.
