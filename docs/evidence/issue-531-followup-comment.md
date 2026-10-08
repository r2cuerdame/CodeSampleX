## #531 protected SSH follow-up (2026-10-06)

Read-only five-second `/proc/stat` deltas, spaced 35/25 seconds apart; no load probe or host mutation. All three samples were on the existing `v0.2.4 / a9fe2483` deployment. The original route alert times below are historical, **not coincident** with these samples.

| Sample UTC | Steal | Load 1m | Original p95 violation timestamps UTC (#526 healthz / #527 stats / #528 shard / #529 version) |
| --- | ---: | ---: | --- |
| 13:31:15 | 50.89% | 4.26 | Sep 28 09:58:45 / Sep 28 09:58:45 / Sep 30 09:51:56 / Oct 1 10:19:52 |
| 13:31:50 | 76.23% | 3.26 | Sep 28 09:58:45 / Sep 28 09:58:45 / Sep 30 09:51:56 / Oct 1 10:19:52 |
| 13:32:15 | 77.73% | 2.70 | Sep 28 09:58:45 / Sep 28 09:58:45 / Sep 30 09:51:56 / Oct 1 10:19:52 |

The latest GitHub Actions [20-round SLO run 37450108173](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37450108173), measured 10:37:04 UTC (about three hours earlier), still violated every route: healthz 10.0000/.3731 s, stats 6.3655/.3425 s, shard 10.0000/.4155 s, version 3.2664/.3311 s (p95/target). It is not a same-window correlation measurement.

Server container memory was **94.90%, 94.94%, 95.92%** of 768 MiB. Anonymous RSS was 733,768→745,484 KiB, versus only ~10.9 MB cgroup file cache; swap was 73,956–81,324 KiB. The sole substantial process was `csx-server`; Docker restart count and cgroup OOM-kill count were 0. `GOMEMLIMIT=600MiB`. The existing runtime metrics endpoint returned 401 without an admin credential, so live Go heap and GC limiter state remain unmeasured. Historical #485 identifies unbounded in-process caches as a plausible cause; current cache contribution is not quantified. These observations establish sustained host steal and near-limit process memory, **not** that host capacity is the only cause. No RED→GREEN fix, QA PASS, memory ≤80%, or p95 recovery can be claimed. Full sanitized evidence is on the pushed #531 branch at `docs/evidence/issue-531-slo-triage.md`.
