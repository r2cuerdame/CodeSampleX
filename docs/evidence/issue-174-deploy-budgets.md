# Empirical deployment timing audit (#174)

Run [34311753137](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34311753137) completed SUCCESS without intervention. Deploy and verify took 2,038 s (33m58s), ending 2026-09-09 05:13:07Z. Operational SHA `1bb8cf3d5b8983cf0257c0893192931eba1646b6`; served target `e6bc85b2299ba8097fb48e32c079db4ef69a7930`.

Seconds below use Type 7 linear interpolation. Successful completed phases from the successful run and preceding failure [34309433387](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34309433387) are included; whole-run failure does not invalidate a completed preparation phase. These tiny samples describe observations, not stable population p95. Singleton p50/p95/max coincide.

| Completed phase | n | p50 | p95 | Measured max | Proposed cap |
|---|---:|---:|---:|---:|---:|
| Preparation | 2 | 41.892 | 42.187 | 42.220 | 180 |
| Staging | 2 | 189.519 | 190.388 | 190.485 | 240 |
| Config promotion (`activation`) | 2 | 8.880 | 9.094 | 9.118 | 30 |
| Offline migration lifecycle | 1 | 1704.797 | 1704.797 | 1704.797 | M + 300 |
| SQL migration itself | 1 | 1561.121 | 1561.121 | 1561.121 | M = 1800 |
| Old controller activation/smoke | 1 | 73.046 | 73.046 | 73.046 | 180 shared lifecycle |
| Controller host recovery | 1 | 65.532 | 65.532 | 65.532 | 270 |
| Cleanup | 2 | 8.626 | 8.630 | 8.630 | 60 |

The failed SQL sample, 1,199.031 s under a 1,200 s budget, is censored and excluded from successful percentiles. Its host lifecycle was 1,261.916 s. Controller rollback was only 0.001 s because recovery delegated to the host; treating that as actual rollback duration would be incorrect.

The successful host timestamps show: launch to quiescence 19.362 s; SQL 1,561.121 s; SQL completion to cleanup 7.604 s; cleanup to candidate ready 71.282 s; candidate ready to controller acknowledgement 72.319 s. Post-SQL through acknowledgement was 151.205 s. Host recovery after cleanup took 36.644 s in the failed run. A shared 180 s activation budget fits the old path before removing duplicate health checks and the controller acknowledgement round trip.

The proposed serial failure-path envelope is `M + 1340` seconds: identity 60 + preparation 180 + staging 240 + config 30 + offline lifecycle M+300 + activation 180 + host recovery 270 + failure fence 20 + cleanup 60. A step cap `ceil(M/60)+24` minutes leaves 100 s runner margin; job cap +3 minutes. This is an engineering bound requiring code-level enforcement, not an empirical percentile. Staging has the tightest relative allowance: 49.515 s / 26% above observed max. Lowering SQL below the successful 1,561 s would repeat the prior timeout.

Legacy successful runs 34092352074 / 34189442073 / 34205073983 had different boundaries and inline migration: startup to smoke 69.443 / 54.403 / 69.206 s; broad smoke to exact identity 1,327.749 / 112.660 / 152.413 s. They are excluded from modern phase aggregates. The 22m8s broad-smoke interval is evidence for moving broad data checks to observation. No instrumented no-op migration sample was available.

Machine-readable samples, exclusions and source links are retained in
[issue-174-deploy-phase-metrics.json](issue-174-deploy-phase-metrics.json).
Raw logs/artifacts were downloaded read-only into the job's .tmp/174-phase-metrics.
This audit did not mutate or cancel production. These proposed caps are
implemented in this follow-up; the new controller has not been deployed by this task.
