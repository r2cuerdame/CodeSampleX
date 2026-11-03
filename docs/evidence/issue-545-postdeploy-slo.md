# Issue #545: post-deploy five-route SLO observation

## Deployment identity

- PR #543 merged as `63f5dbe16cb464e4a4b1b1fdbbe1ecbc563739b7`, the canonical `main` SHA at dispatch.
- Tag `v0.2.6` points to that exact commit. [Release run 37710916362](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37710916362) passed its tests, publication, and farm rollout.
- [Production deploy run 37712518001](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37712518001) passed eligibility and rollout, using previous production SHA `a9fe24839bae5dfda10da04dcd7d6cb99e3674c7` and target `63f5dbe16cb464e4a4b1b1fdbbe1ecbc563739b7`.
- GitHub production deployment `6924970868` records the same target SHA.
- The deployment's subsequent [post-deploy observation](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37713181513) ended in `failure` at 02:03:27 UTC. A successful rollout and matching served SHA establish the identity of this measurement, **not** operational acceptance of the release.
- Public read-only `GET https://codesamplex.dev/version` returned `environment=production`, `version=v0.2.6`, and the full target revision before and after the observation.
- A read-only server-container log sample at `2026-10-08T01:39:32Z` recorded `go_max_procs=2`.

## Protocol and result

The existing [issue #531 collector](issue-531-window-probe.py) ran through the pinned production SSH identity without writing to the host. It made one local-Caddy read-only GET per route at most every five seconds. Each one-minute UTC window uses request-to-first-byte less one local TCP round trip for server-time p95, and `/proc/stat` deltas for CPU steal. A failed or non-2xx request consumes the 10-second timeout in p95. The [JSONL raw record](issue-545-window-2026-10-08.jsonl) retains each status and individual sample. Its nine windows span `2026-10-08T01:30:23Z` through `01:39:24Z`; each route has 108 samples. Recalculation from the individual samples matched every recorded p95. The collector exited 0.

Evidence correction: the collector had hardcoded `runtime: "unavailable on served v0.2.4"` in each JSONL row. That label was not a runtime measurement and contradicted the independently verified v0.2.6 production identity above. It was removed from the nine original rows and the diagnostic row; window times, host metrics, route samples, and p95 values were preserved.

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

**Issue #545 disposition: BLOCKED by the measured five-route SLO failure.** The same deployment evidence was posted to the related alert threads: [#526 `/healthz`](https://github.com/r2cuerdame/CodeSampleX/issues/526#issuecomment-6087940829), [#527 `/v1/stats`](https://github.com/r2cuerdame/CodeSampleX/issues/527#issuecomment-6087941345), and [#528 `/v1/shards/npm/zod/3`](https://github.com/r2cuerdame/CodeSampleX/issues/528#issuecomment-6087941839). Those comments document the historical v0.2.6 observation; they do not establish a pass for a later production SHA.

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

The existing no-cost governor was already acting: the production log at **01:53:17 UTC** recorded `reason=host-cpu-steal builder=paused farm_ingest=2`. The server still consumed about 130% CPU and all five routes failed in the 01:56 window while that automatic Builder-shedding policy was active. Occasional `interactive-pool-pressure` transitions also briefly paused Farm ingest; no operator setting was changed.

The guest's `/proc/stat` active/steal percentages and `pidstat` process CPU percentages use different accounting bases under steal; do not sum them or claim an exact share of physical CPU. The process comparison does show that `csx-server` was the dominant observed user. The local-Caddy probe removes the external network path, and `/version` has no database query. Its p95 also rose with steal and returned within SLO in the earlier low-steal windows, so neither an individual DB query nor the external network can explain the common five-route slowdown. Host CPU scheduling/credit pressure is the strongest observed common factor. Server CPU and rapid GC may be driving the credit pressure, but the available logs lack `gcCPUFraction` and `gcLimiterLastEnabledCycle` for this window; **code-induced GC load versus irreducible capacity is not yet separated**. This does not justify a paid resize or predict its p95.

The earlier `/healthz` **503 is specifically explained** by the server log at **01:31:15 UTC**: `db pressure path=/healthz class=probe cause=pool_busy ... waited=1.049s`, followed by `GET /healthz` status **503** and duration **1048.8 ms**. Other interactive paths had `admission_refused` lines in that minute. The probe's reserved DB admission slot did not become available within its wait budget amid the high-steal/startup pressure. This response is not a Caddy 503 or an externally induced network timeout. The one failure counts as ten seconds in the first window's p95, per the existing SLO protocol.

**Remaining condition:** the measured v0.2.6 windows did not establish an all-five-route SLO pass. No source, workflow, host setting or production deployment was changed during this diagnosis. The previous USD 44/month AWS Lightsail option (**USD 32/month** above the now-confirmed current bundle price; recurring, AWS payee, no physical host manipulation; [price](https://aws.amazon.com/lightsail/pricing/)) remains an unproven option, **not a proposed purchase**: the diagnosis has not isolated capacity as the only cause or reproduced the load on that specification.

## Later production identity check

At `2026-10-09T16:30:29Z`, read-only `GET https://codesamplex.dev/version` returned `version=v0.2.13`, `environment=production`, and full revision `decd00ebf5ce5d222deefcaaa0f747f264b623d3`. That revision descends from PR #543's merge commit, but does not equal the measured v0.2.6 deployment SHA. No new five-route measurement was started against this later deployment. The historical windows above remain tied to the separately verified v0.2.6 SHA and cannot establish the later deployment's p95.

A second read-only `/version` check at `2026-10-09T17:09:40Z` returned the same production version and revision. The historical result remains an SLO failure for the exact v0.2.6 SHA; it is not a measurement of v0.2.13.

At the 2026-10-10 repair readback, read-only `/version` returned `version=v0.2.14`, `environment=production`, and revision `352cdaff49658bfa2e2fe2712fda427d20dba25c`. No five-route measurement was made for this later revision. The v0.2.6 SLO failure cannot be relabeled as a pass because production has advanced.

At a later 2026-10-10 read-only check, `/version` returned `version=v0.2.17`, `environment=production`, and revision `05f58dbb2ac85fa1de4769c4860dc9cea85f350b`. The separate [performance probe run 38020620466](https://github.com/r2cuerdame/CodeSampleX/actions/runs/38020620466) reported p95 violations for `/healthz` (1.571s against .373s), `/v1/stats` (.766s against .343s), and `/v1/shards/npm/zod/3` (1.092s against .415s) at that later SHA. Its external vantage and sampling protocol differ from this Issue's same-clock host windows; it does not supply a new five-route PASS or replace the historical v0.2.6 measurement.

**Open question for Luna, outside this PR:** Should a follow-up under the existing alerts [#526](https://github.com/r2cuerdame/CodeSampleX/issues/526), [#527](https://github.com/r2cuerdame/CodeSampleX/issues/527), and [#528](https://github.com/r2cuerdame/CodeSampleX/issues/528) investigate server CPU/GC demand and define a testable reduction before reconsidering capacity? This evidence-only Issue does not authorize that investigation, a code change, paid resize, or a fresh claim of SLO acceptance.

## Remeasurement on current production deployment (2026-11-03 KST / 2026-11-02 UTC)

Per Owner directives (Issue #545 comments 104 and 105), the five routes and host metrics were remeasured against the current production deployment under the same read-only protocol on existing infrastructure (`small_3_0`, USD 12/month).

### Deployment identity

- Production `/version` readback: `version=v0.2.23`, `revision=b33f00ea4fd1303fcad45fd1224c29b27dcc7db4` (built 2026-10-14T08:34:59Z, `environment=production`).
- Revision `b33f00ea4fd1303fcad45fd1224c29b27dcc7db4` is a confirmed descendant of PR #543 merge SHA `63f5dbe16cb464e4a4b1b1fdbbe1ecbc563739b7` (`git merge-base --is-ancestor` exit 0).
- Production host: AWS Lightsail Linux `small_3_0` (2 vCPU, 2 GB RAM, 60 GB SSD), existing monthly cost **USD 12/month** payable to AWS (existing recurring expense; no new spending authorization required).
- Production container: `codesamplex-server-1` healthy with `go_max_procs=2`.

### Protocol and measurement

- Measured from `2026-11-02T23:33:24Z` to `23:42:24Z` (nine consecutive 1-minute UTC windows, 9 minutes total, within the 30-minute limit).
- Execution: read-only probe through the pinned production SSH identity without writing to the host, without load generation, without configuration changes, and without server restart.
- Sampling: 12 GET requests per path per 1-minute window (at most one request every 5 seconds), totaling 108 samples per path.
- Raw JSONL record: [issue-545-window-2026-11-03.jsonl](issue-545-window-2026-11-03.jsonl).

### Nine-window results

SLO targets and p95 values are seconds. Non-2xx responses consume the 10.0-second timeout budget in the p95 calculation.

| UTC window start | CPU steal % | CPU active % | Load1 | healthz (.3731) | version (.3311) | stats (.3425) | shard (.4155) | jobs (.5771) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 23:33:24 | 72.22 | 18.26 | 3.32 | 0.9617 | 0.7919 | 0.8463 | 1.6728 | 2.4081 |
| 23:34:24 | 72.26 | 18.26 | 4.08 | 1.7632 | 0.4863 | 0.7968 | 2.7972 | 1.9257 |
| 23:35:24 | 76.52 | 19.29 | 4.64 | 1.1198 | 0.3260 | 0.4884 | 1.3605 | 3.2047 |
| 23:36:24 | 78.22 | 19.62 | 4.09 | 1.0373 | 0.4087 | 0.9637 | 10.0000 | 0.8828 |
| 23:37:24 | 77.07 | 19.36 | 3.77 | 2.6418 | 1.4393 | 1.4376 | 10.0000 | 10.0000 |
| 23:38:24 | 73.57 | 18.68 | 3.81 | 10.0000 | 1.5150 | 1.7552 | 10.0000 | 10.0000 |
| 23:39:24 | 75.68 | 19.09 | 3.28 | 3.4429 | 1.9392 | 1.9351 | 10.0000 | 10.0000 |
| 23:40:24 | 77.19 | 19.45 | 3.78 | 10.0000 | 1.9256 | 1.9278 | 10.0000 | 10.0000 |
| 23:41:24 | 73.51 | 18.56 | 3.87 | 2.5452 | 1.4398 | 1.9917 | 10.0000 | 10.0000 |

### Violation summary and sample verification

- `/healthz` (SLO 0.3731s): **9/9 windows exceeded** (102x HTTP 200, 6x HTTP 503).
- `/version` (SLO 0.3311s): **8/9 windows exceeded** (108x HTTP 200, 0 errors; only window 3 met target at 0.3260s).
- `/v1/stats` (SLO 0.3425s): **9/9 windows exceeded** (108x HTTP 200, 0 errors).
- `/v1/shards/npm/zod/3` (SLO 0.4155s): **9/9 windows exceeded** (79x HTTP 200, 29x HTTP 429).
- `/v1/verification/jobs?peerId=ed25519:0123456789abcdef&limit=10` (SLO 0.5771s): **9/9 windows exceeded** (67x HTTP 200, 41x HTTP 503).

Independent recomputation of all 45 route/window p95 values from the raw samples matched recorded values with **zero mismatches**. Total p95 recomputations across all three historical and current JSONL files (95 values total) have zero mismatches.

### Context and host observations

- Host CPU steal sustained between **72.22% and 78.22%** across all nine windows.
- Guest active CPU was **18.26%–19.62%**, sitting at the 20% sustainable baseline for `small_3_0`.
- Server logs during the measurement showed active governor shed events (`reason=host-cpu-steal builder=paused farm_ingest=2`) and DB admission pool contention (`class=probe cause=pool_busy`, `admission_refused`), explaining the probe 503s on `/healthz` and `/v1/verification/jobs`.
- `/version`, which makes zero database queries, also degraded up to 1.9392s p95, confirming that CPU scheduling starvation and credit exhaustion affect all routes.

### Completion condition and cost reporting

- All-five-route SLO acceptance condition: **NOT MET** (exceeded on all five routes).
- Current host resource cost: AWS Lightsail Linux `small_3_0` (2 vCPU, 2 GB RAM, 60 GB SSD) at **USD 12/month** (existing paid resource).
- Recorded paid upgrade option: AWS Lightsail Linux `large_3_0` (2 vCPU, 8 GB RAM, public IPv4) at **USD 44/month**, payee **AWS**, **recurring**, **no physical host operation needed** (nominal difference USD 32/month). Note: performance improvement is unproven and not purchased.
- **Issue #545 disposition: BLOCKED**. Per Owner decision (comments 104 and 105) and Issue #545 completion conditions, the measurement raw data is recorded, no code/configuration changes are made, and the result is escalated to Luna/Owner.
