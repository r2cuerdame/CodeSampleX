# #813 `/v1/stats` p95 violation: the seconds are behind Caddy

## What the probe measured

Production `v0.2.25` / `0d61c36072b25d352ad798b29e4e51920e780ef7`, GitHub
`ubuntu-latest` vantage, run
[44230329361](https://github.com/r2cuerdame/CodeSampleX/actions/runs/44230329361)
at 2026-11-18T03:29:00Z. Seconds:

| | median | p95 |
| --- | ---: | ---: |
| server time (SLO metric, target 0.343) | 0.443 | 0.757 |
| in-process (Server-Timing, 20 of 20) | 0.000 | 0.000 |
| outside the process | 0.443 | 0.757 |

The same run had `/healthz`, `/version`, the shard lookup and the verifier
queue over target as well (#636, #637, #739, #600). Every daily run since
2026-11-01 was taken at 03:26–03:35 UTC.

## What a second vantage adds

Measured from the operator workstation (Windows, KR, round trip 0.03 s) on
2026-11-18, outside that 03:xx UTC window, against the same build. Seconds:

| run | path | server median / p95 | in-process median / p95 | outside median / p95 |
| --- | --- | ---: | ---: | ---: |
| 08:36Z, 20 rounds | `/v1/stats` | 0.218 / 0.548 | 0.000 / 0.000 | 0.218 / 0.548 |
| 08:36Z, 20 rounds | `/version` | 0.333 / 0.693 | 0.000 / 0.000 | 0.333 / 0.693 |
| 08:44Z, 10 rounds | `/v1/stats` | 0.333 / 0.763 | 0.079 / 0.245 | 0.268 / 0.518 |
| 08:44Z, 10 rounds | `/version` | 0.212 / 0.452 | 0.000 / 0.000 | 0.212 / 0.452 |
| 08:44Z, 10 rounds | `/healthz` | 0.650 / 1.491 | 0.351 / 0.793 | 0.292 / 1.007 |
| 08:44Z, 10 rounds | edge only (`https://www.codesamplex.dev/`) | 0.045 / 0.125 | | |

The edge reference is the www redirect that Caddy answers itself
(`deploy/caddy/Caddyfile`, `redir @www`); it never reaches csx-server. In the
same rounds Caddy answered it in 0.045 s median while `/version`, which spends
under 0.1 ms inside csx-server, took 0.212 s. An earlier 15-round comparison
at 08:40Z gave the same split: redirect 0.039 / 0.353, `/version`
0.435 / 2.348, `/v1/stats` 0.517 / 1.786.

## What it shows

- The slowdown is not the 03:15 UTC backup window, the runner's network or
  Caddy and TLS. It is there at 08:4xZ from a 0.03 s vantage, and the path
  Caddy answers alone stays fast.
- The time outside the process is spent between Caddy and the first
  middleware of csx-server: the upstream hop and the csx-server process
  before it starts its timer. Handlers that do no I/O measure zero inside.
  Handlers that wait on the network inside the timer pick up the same delay:
  `/healthz` spent 0.35–0.79 s on a single database ping (`query_handler`).
  That pattern is what a csx-server process that cannot get CPU promptly
  looks like. The Caddy process on the same host was not slow in the same
  minutes.
- The in-process `/v1/stats` time at 08:44Z is the hot-shard wait. A single
  `GET /v1/stats` at 08:46:56Z reported `query_handler;dur=87.801` and carried
  no `hotShards` key, i.e. the hint read had not produced keys and the caller
  waited on it. #808 (merged as #812) stops callers waiting after a failed
  read. It is on `main` but not in production.
- The builder reported a timed-out full pass at `farm_coverage_read`, five
  failures in a row, and stats `generatedAt` 2026-11-16T10:40:52Z (`/v1/builder`
  at 08:39Z). That is #666.

## What changed for this Issue

The probe Issue table gains an `edge only (Caddy answers, no upstream)` row,
so each probe comment on #813 and the other SLO Issues shows whether the time
outside the process is in Caddy or behind it (method:
`docs/performance-slo.md`).

## What closes the Issue

The probe closes #813 after two consecutive passing runs. Production is 58
commits behind `main` (`be9661d`), and #808 reaches the probe only with a
deploy. Deploys are the owner's gate (`.loop/project.md`,
`deployWorkflow: not_required`). If the time behind Caddy stays after that
deploy, the remaining cause is CPU for csx-server on the 2-vCPU host. More
capacity is a new cost and needs owner approval. This change does not propose
it, and the 0.343 s target stays as #511 set it.

## Unknown

- Host CPU steal, csx-server's Go scheduler latency and garbage-collection
  share at these times. The `go_runtime` lines are in the container log and
  the host sampler is behind operator authentication; neither is in the
  repository or the operations snapshot.
- Whether the hot-shard read failed or was still running when the 08:46Z
  request waited. The response cannot tell the two apart.
- Whether the nightly backup adds to the 03:xx UTC runs. Its crontab lives on
  the host.
