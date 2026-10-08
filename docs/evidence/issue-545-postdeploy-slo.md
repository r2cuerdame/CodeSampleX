# Issue #545: post-deploy five-route SLO observation

## Deployment identity

- PR #543 merged as `63f5dbe16cb464e4a4b1b1fdbbe1ecbc563739b7`, the canonical `main` SHA at dispatch.
- Tag `v0.2.6` points to that exact commit. [Release run 37710916362](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37710916362) passed its tests, publication, and farm rollout.
- [Production deploy run 37712518001](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37712518001) passed eligibility and rollout, using previous production SHA `a9fe24839bae5dfda10da04dcd7d6cb99e3674c7` and target `63f5dbe16cb464e4a4b1b1fdbbe1ecbc563739b7`.
- GitHub production deployment `6924970868` records the same target SHA.
- Public read-only `GET https://codesamplex.dev/version` returned `environment=production`, `version=v0.2.6`, and the full target revision before and after the observation.
- A read-only server-container log sample at `2026-10-08T01:39:32Z` recorded `go_max_procs=2`.

## Protocol and result

The existing [issue #531 collector](issue-531-window-probe.py) ran through the pinned production SSH identity without writing to the host. It made one local-Caddy read-only GET per route at most every five seconds. Each one-minute UTC window uses request-to-first-byte less one local TCP round trip for server-time p95, and `/proc/stat` deltas for CPU steal. A failed or non-2xx request consumes the 10-second timeout in p95. The [JSONL raw record](issue-545-window-2026-10-08.jsonl) retains each status and individual sample. Its nine windows span `2026-10-08T01:30:23Z` through `01:39:24Z`; each route has 108 samples. Recalculation from the individual samples matched every recorded p95. The collector exited 0.

SLO targets and p95 values are seconds. The five routes are `/healthz`, `/version`, `/v1/stats`, `/v1/shards/npm/zod/3`, and `/v1/verification/jobs?peerId=ed25519:0123456789abcdef&limit=10`, respectively.

| UTC window start | CPU steal % | healthz (.3731) | version (.3311) | stats (.3425) | shard (.4155) | jobs (.5771) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 01:30:23 | 74.61 | 10.0000 | .3858 | .3060 | 1.8421 | 1.9940 |
| 01:31:23 | 64.07 | 1.5181 | .1746 | .2413 | .5583 | .5499 |
| 01:32:23 | 58.47 | .7188 | .4180 | .3209 | .5561 | .4902 |
| 01:33:23 | 64.28 | 1.7478 | .5604 | .5638 | 1.5148 | .8794 |
| 01:34:23 | 63.45 | .9536 | .8749 | .5474 | .8721 | 1.1867 |
| 01:35:24 | 23.04 | .6235 | .3154 | .3112 | .4894 | .5534 |
| 01:36:24 | .40 | .1084 | .0500 | .0828 | .0807 | .1251 |
| 01:37:24 | .46 | .0601 | .0533 | .0560 | .0582 | .0649 |
| 01:38:24 | .30 | .0517 | .0470 | .0558 | .0576 | .0538 |

Violation counts by route are **6/9, 4/9, 2/9, 6/9, and 3/9**. Only `/healthz` had a failed response: one HTTP 503 in the first window. All five routes met their SLOs in the final three windows, when CPU steal was .30–.46%. The earlier high-steal windows had recurring violations. This is an observed association, not proof that CPU steal is the sole cause. The required condition that all five paths remain within SLO was **not met**.

## Decision needed

No further production change was made. Luna should choose the operational response. The paid option considered here is a virtual move to the Lightsail Linux 8 GB / 2 vCPU public-IPv4 bundle: **USD 44 per month** to **AWS**, **recurring**, with **no physical host operation**. If the current instance is on the inferred USD 12 per month 2 GB bundle, the plan-price difference would be USD 32 per month; its actual billed tier was not read. AWS lists a 30% CPU baseline for the USD 44 bundle versus 20% for the USD 12 bundle. A latency benefit is unproven; selecting new spend needs Source approval. [AWS pricing](https://aws.amazon.com/lightsail/pricing/) and [AWS CPU baseline documentation](https://docs.aws.amazon.com/lightsail/latest/userguide/baseline-cpu-performance.html) were checked on 2026-10-08.

No source code or workflow was changed in this Issue. The existing deployment workflow applied PR #543; no separate manual configuration change, new secret, new cost, or physical host operation was introduced. The Issue remains open pending Luna's decision.
