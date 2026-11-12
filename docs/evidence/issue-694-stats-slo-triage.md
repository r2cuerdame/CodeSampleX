# #694 `/v1/stats` p95 violation: triage

## What the probe measured

Both violating runs were against production `v0.2.23` /
`b33f00ea4fd1303fcad45fd1224c29b27dcc7db4` (committed 2026-10-14), from the
same GitHub `ubuntu-latest` vantage. Server time, seconds:

| run | `/v1/stats` median / p95 | `/version` median / p95 | target stats / version |
| --- | ---: | ---: | ---: |
| 2026-11-11T03:31:52Z ([42952553532](https://github.com/r2cuerdame/CodeSampleX/actions/runs/42952553532)) | unknown / 3.800 | 1.106 / 3.317 | 0.343 / 0.331 |
| 2026-11-12T03:28:23Z ([43147983502](https://github.com/r2cuerdame/CodeSampleX/actions/runs/43147983502)) | 0.515 / 0.910 | 0.437 / 0.922 | 0.343 / 0.331 |

Sources: the #694 body and the probe's comments on #637. `/healthz` is open
for the same reason (#636).

## Why the stats handler is not the difference

On the deployed revision, `/version` encodes a small struct of the process's
own build stamps (`internal/httpapi/version.go` at `b33f00e`). It goes through
the same route middleware as `/v1/stats` and reads no database on the request
path. In the same run it was as slow as `/v1/stats`: p95 0.922 s against 0.910 s. On 2026-11-11
both paths had a p95 over 3 s. Every path in the run slowed by roughly
the same amount. That points to time spent before or around the Go
handler, in the proxy, the host's CPU or the process scheduler. #666 is open
for the builder's `farm_coverage_read` timing out on the same host. Whether
that overlapped these runs is not recorded.

## The code production runs vs. `main`

Production is 100 commits behind `main` at `3c32cd7`. The `/v1/stats` request
path on `main` already includes three fixes that production does not run:

- #598: the merged stats body is cached, so a poll no longer decodes and
  re-encodes the whole rollup.
- #638: a process that already holds a hot-shard hint no longer waits even
  25 ms for its refresh.
- #652: an expired rollup is served while one background read replaces it.
  Callers no longer queue behind the cache lock for a database read.

These reach production only with a deploy. Deploys are the owner's gate
(`.loop/project.md`, `deployWorkflow: not_required`).

## What changed for this Issue

The probe now records each request's in-process time from the server's
`Server-Timing` header. It also records server time minus that, per request
(`docs/performance-slo.md`). The next comment on #694, #636 or #637 will
show directly whether the seconds are inside the process or outside it.
Without that split, a host-wide slowdown cannot be told apart from a slow
handler.

## Unknown

- The in-process time of the two violating runs. The deployed build sends
  `Server-Timing`, but the probe did not read it before this change.
- Host CPU, Caddy queueing and builder activity at 03:28Z and 03:31Z. No
  host telemetry for those minutes is in the repository or the operations
  snapshot.
