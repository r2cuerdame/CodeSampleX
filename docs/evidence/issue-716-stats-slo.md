# #716 `/v1/stats` p95 violation: where the seconds are

## What the probe measured

Production `v0.2.23` / `b33f00ea4fd1303fcad45fd1224c29b27dcc7db4`, GitHub
`ubuntu-latest` vantage, run
[43358818391](https://github.com/r2cuerdame/CodeSampleX/actions/runs/43358818391)
at 2026-11-13T03:29:05Z. This is the first run whose result splits server time
with the `Server-Timing` header (#694). Seconds:

| path | server median / p95 | in-process median / p95 | outside median / p95 | target |
| --- | ---: | ---: | ---: | ---: |
| `/v1/stats` | 0.351 / 0.665 | 0.000 / 0.000 (20 of 20) | 0.351 / 0.665 | 0.343 |
| `/version` | 0.420 / 1.063 | 0.000 / 0.000 (20 of 20) | 0.420 / 1.063 | 0.331 |
| `/healthz` | 0.777 / 10.000 | 0.319 / 1.120 (18 of 20) | 0.307 / 0.662 | 0.373 |

Sources: the #716 body and the same run's comments on #637 (`/version`) and
#636 (`/healthz`). The previous run (2026-11-12T03:28:23Z) had `/v1/stats` p95
0.910 s; its in-process time was not recorded.

## What this rules out

Every `/v1/stats` request spent under half a millisecond between the server's
outer middleware and the end of serialization. `/version` serves a fixed
in-memory struct and was slower than `/v1/stats` in the same run. The
handler, the stats cache and the database are not where the p95 goes, and
no change to the stats handler can move it. The seconds are spent before the
clock starts or after it stops: in Caddy, in the host's CPU scheduler, or in
the Go process outside the handler. #545 measured the same pattern with
76–79% CPU steal on the 2-vCPU host (`docs/evidence/issue-545-postdeploy-slo.md`).

## What changed for this Issue

One part of "after it stops" was on the request path. The timed routes
(`/healthz`, `/readyz`, `/version`, `/v1/stats`, shard lookup) buffer the body
so `Server-Timing` can include serialization. net/http then held that small
body until every outer middleware returned. In production that included
csx-server's pool pressure line and the `http_request` record written
synchronously to stdout (the container's json-file log). `timed` now
declares `Content-Length` and flushes as soon as the handler finishes, so that
work no longer sits in front of the client's first byte.
`TestTimedRoutesAnswerBeforeOuterMiddlewareReturns` fails without the
flush. How much of the measured outside time this removes is unknown until
the change is deployed and the probe runs again.

## What closes the Issue

The probe closes #716 after two consecutive passing runs against production.
Production is 148 commits behind `main` (`65414b1`), and deploys are the
owner's gate (`.loop/project.md`, `deployWorkflow: not_required`). This
change and the earlier stats fixes listed in
`docs/evidence/issue-694-stats-slo-triage.md` reach the probe only with a
deploy. If the outside time stays after that deploy, the remaining cause is
host capacity. More capacity would be a new cost and needs owner approval.
Changing the target is a decision #511 did not take, so this change leaves
it alone.

## Unknown

- Host CPU steal, Caddy upstream time and the json-file log pipe latency at
  03:29Z on 2026-11-13. No host telemetry for that window is in the repository
  or in the operations snapshot.
- The exact in-process time below 0.5 ms. The header carries microseconds,
  the probe keeps 0.1 ms, and the Issue table prints milliseconds.
