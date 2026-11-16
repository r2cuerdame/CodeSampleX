# #738 `/v1/stats` p95 violation: what this run adds

## What the probe measured

Production `v0.2.23` / `b33f00ea4fd1303fcad45fd1224c29b27dcc7db4`, GitHub
`ubuntu-latest` vantage, run
[43551395439](https://github.com/r2cuerdame/CodeSampleX/actions/runs/43551395439)
at 2026-11-14T03:28:47Z. Seconds:

| | median | p95 |
| --- | ---: | ---: |
| server time (SLO metric, target 0.343) | 0.596 | 1.327 |
| in-process (Server-Timing, 20 of 20) | 0.033 | 0.169 |
| outside the process | 0.516 | 1.262 |

No request failed (20 of 20 returned 200). The previous run
(2026-11-13T03:29:05Z, #716) had p95 0.665 s, with an in-process time of
0.000 s on every request.

## What it shows

Outside the process is still most of the p95: 1.262 s of 1.327 s. That is the
same pattern #694, #716 and #545 measured: Caddy, the host's CPU scheduler and
the Go runtime before the outermost middleware. On this 2-vCPU burstable host,
#545 measured 72–79% CPU steal while every route, including the
database-free `/version`, missed its target
(`docs/evidence/issue-545-postdeploy-slo.md`). No change to the stats handler
can move that part.

This run differs from #716 in one way: the in-process p95 is no longer zero.
The deployed revision is the same as before, so the stats path itself did not
change. The probe stored only the sum of the four Server-Timing phases. It
cannot tell whether the 0.169 s was `db_wait` (the pool), `query_handler`
(a rollup read behind the cache lock) or `serialize`.

## What changed for this Issue

The probe now keeps each request's Server-Timing phases (`phaseSamples` in
the artifact) and a p95 per phase (`phaseP95Seconds`). The Issue table has a
new row, `in-process p95 by phase`. The next probe comment on #738 will name
the phase. `scripts/perf_slo_test.py` covers the split, the table row and the
`not reported` case for older artifacts.

## What production does not run yet

Production is 172 commits behind `main` (`68b4463`). These changes to the
`/v1/stats` request path are on `main` but not deployed:

- #598: the merged stats body is cached.
- #638: a process that already has a hot-shard hint does not wait for its
  refresh.
- #652: an expired rollup is served while one background read replaces it,
  so callers do not queue behind the cache lock for a database read.
- #716: the timed routes flush their response before the outer middleware's
  request log and pressure line.

#652 targets exactly the cache-lock wait that a non-zero in-process p95 on
the deployed build would show. Deploys are the owner's gate
(`.loop/project.md`, `deployWorkflow: not_required`). This change reaches the
probe as soon as it is merged, because the workflow runs the script from
`main`.

## What closes the Issue

The probe closes #738 after two consecutive passing runs. The target stays
at 0.343 s: changing it is a decision #511 did not take. If the time outside
the process stays above the target after a deploy, the remaining cause is
host capacity. More capacity is a new cost and needs owner approval. This
change does not propose it.

## Unknown

- Host CPU steal, Caddy upstream time and Builder activity at 03:28Z on
  2026-11-14. No host telemetry for that window is in the repository or in
  the operations snapshot.
- Which phase held this run's 0.169 s. Only the sum was recorded.
