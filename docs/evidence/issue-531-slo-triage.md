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

Sources: the `github-actions` measurements in [#526](https://github.com/r2cuerdame/CodeSampleX/issues/526), [#527](https://github.com/r2cuerdame/CodeSampleX/issues/527), [#528](https://github.com/r2cuerdame/CodeSampleX/issues/528), and the unrelated static-route control [#529](https://github.com/r2cuerdame/CodeSampleX/issues/529). These are all the same GitHub Actions vantage and deployment revision. One 20-request p95 is the nineteenth ordered observation, so these repeated runs are stronger evidence than a single tail sample.

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

Correlate the same probe windows with host CPU/steal and memory pressure,
server process CPU/GC/scheduler pauses, PostgreSQL pool wait and query times,
and Caddy upstream timing versus client-side TCP/TLS/request phases. Then
decide whether this is capacity/edge contention, a shared runtime defect, or
a probe-baseline policy problem. No production mutation or paid capacity is
needed for this read-only diagnosis.
