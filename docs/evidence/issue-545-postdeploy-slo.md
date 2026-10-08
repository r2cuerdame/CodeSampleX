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

## Same-clock host diagnosis (2026-10-08 01:50:21–01:51:21 UTC)

The production `/version` readback was still `63f5dbe16cb464e4a4b1b1fdbbe1ecbc563739b7` (v0.2.6). A further **read-only** one-minute window used the same collector and local-Caddy route protocol as above, with 12 GET samples per path. It measured **79.22% steal**, **19.45% active CPU** across the two-vCPU guest and mean load1 **4.53**. The p95 values (seconds) were:

| Route | p95 | SLO | Result |
| --- | ---: | ---: | --- |
| `/healthz` | 2.4855 | .3731 | over; 12/12 HTTP 200 |
| `/version` | .9502 | .3311 | over; 12/12 HTTP 200 |
| `/v1/stats` | .9538 | .3425 | over; 12/12 HTTP 200 |
| `/v1/shards/npm/zod/3` | 2.1684 | .4155 | over; 12/12 HTTP 200 |
| `/v1/verification/jobs?peerId=ed25519:0123456789abcdef&limit=10` | 6.9476 | .5771 | over; 12/12 HTTP 200 |

In that same minute, `pidstat -u` at five-second intervals reported average CPU **132.85%** for `csx-server` and **7.16%** for Caddy; the PostgreSQL parent process was **0.03%** (its worker processes were not included). The server's 30-second `go_runtime` log samples were `go_max_procs=2`, RSS **626.9 → 626.8 → 623.9 MB**, `gc_count=436 → 454 → 478`, and heap in-use **611.8 → 612.0 → 608.8 MB** at 01:50:02, 01:50:32 and 01:51:02. This is **42 GCs in 60 seconds** with memory near the configured **600 MiB GOMEMLIMIT** and below the **768 MiB cgroup limit**. A host read at 01:49 had **483.8 MB MemAvailable** and **155.6 MB swap used**. These clock-adjacent memory readings are context, not a same-window memory series.

A second [raw one-minute JSONL window](issue-545-window-diagnostic-2026-10-08.jsonl) at **01:56:23–01:57:23 UTC** included all PostgreSQL processes in `pidstat -u -C 'csx-server|caddy|postgres'`, sampled every five seconds. CPU steal was **76.09%**, guest active CPU **18.81%**, mean load1 **3.34**. P95/SLO in route order was **3.6898/.3731, 1.2070/.3311, 1.2760/.3425, 3.2286/.4155, 3.6770/.5771 seconds**; all five routes exceeded, with **12/12 HTTP 200** for each. `csx-server` averaged **130.19%** CPU, Caddy **7.30%**, and the ten PostgreSQL processes summed to **11.14%**. The three runtime samples at 01:56:02, 01:56:32 and 01:57:02 recorded `gc_count=659 → 674 → 691` (**32 GCs/60s**) and RSS **613.2 → 622.8 → 621.7 MB**. A host read at 01:57:36 showed **456.5 MB MemAvailable** and **202.9 MB swap used**.

AWS Lightsail's read-only `get-instance` returned current bundle `small_3_0` (**2 vCPU, 2 GB**); `get-bundles` returned **USD 12/month** for that bundle and **USD 44/month** for `large_3_0` (**2 vCPU, 8 GB**). At 01:25, 01:30, 01:35, 01:40, 01:45 and 01:50 UTC, read-only `get-instance-metric-data` returned `BurstCapacityPercentage` averages **.00770%, .00779%, .01019%, .01840%, .00780%, .00776%** and `BurstCapacityTime` **1.33, 1.35, 1.76, 3.18, 1.35, 1.34 seconds**. Its `CPUUtilization` was about **19.6–20.4%**, at the current bundle's [20% sustainable baseline](https://docs.aws.amazon.com/lightsail/latest/userguide/baseline-cpu-performance.html). AWS [defines these metrics](https://docs.aws.amazon.com/cli/latest/reference/lightsail/get-instance-metric-data.html) as remaining burst capacity and allocated compute utilization; the data points are **five-minute aggregates**, not additional one-minute host observations. They establish almost exhausted CPU burst credit during the SLO failures, but do not identify why the server requested sustained CPU.

The guest's `/proc/stat` active/steal percentages and `pidstat` process CPU percentages use different accounting bases under steal; do not sum them or claim an exact share of physical CPU. The process comparison does show that `csx-server` was the dominant observed user. The local-Caddy probe removes the external network path, and `/version` has no database query. Its p95 also rose with steal and returned within SLO in the earlier low-steal windows, so neither an individual DB query nor the external network can explain the common five-route slowdown. Host CPU scheduling/credit pressure is the strongest observed common factor. Server CPU and rapid GC may be driving the credit pressure, but the available logs lack `gcCPUFraction` and `gcLimiterLastEnabledCycle` for this window; **code-induced GC load versus irreducible capacity is not yet separated**. This does not justify a paid resize or predict its p95.

The earlier `/healthz` **503 is specifically explained** by the server log at **01:31:15 UTC**: `db pressure path=/healthz class=probe cause=pool_busy ... waited=1.049s`, followed by `GET /healthz` status **503** and duration **1048.8 ms**. Other interactive paths had `admission_refused` lines in that minute. The probe's reserved DB admission slot did not become available within its wait budget amid the high-steal/startup pressure. This response is not a Caddy 503 or an externally induced network timeout. The one failure counts as ten seconds in the first window's p95, per the existing SLO protocol.

**Remaining condition:** all five route p95s are still not within SLO. No source, workflow, host setting or production deployment was changed during this diagnosis. The previous USD 44/month AWS Lightsail option (**USD 32/month** above the now-confirmed current bundle price; recurring, AWS payee, no physical host manipulation; [price](https://aws.amazon.com/lightsail/pricing/)) remains an unproven option, **not a proposed purchase**: the diagnosis has not isolated capacity as the only cause or reproduced the load on that specification. A no-cost code/runtime investigation needs an explicit, testable reduction in server CPU/GC demand before any spending decision can be supported.
