# CodeSampleX #174 historical deployment audit

Read-only GitHub Actions job metadata, rollout logs, and production evidence artifacts downloaded 2026-09-09. Times are UTC; duration is the actual `Deploy and verify` step, including any rollback/evidence tail. Target SHA is artifact `targetSha`, not workflow `headSha`.

| Run / rollout job | Target | Started -> completed (UTC) | Duration | Observed trigger / classification |
| --- | --- | --- | --- | --- |
| [34300110261](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34300110261/job/102305095286) | e6bc85b2299ba8097fb48e32c079db4ef69a7930 | Sep 9 01:40:45 -> 01:58:39 | 1,074s (17m54) | Compose dependency server unhealthy at activation. Genuine startup-critical; server/Caddy exact rollback logged, artifact rollback=succeeded. |
| [34299667021](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34299667021/job/102303770148) | e6bc85b2299ba8097fb48e32c079db4ef69a7930 | Sep 9 01:33:53 -> 01:34:29 | 36s | Initial production evidence probe exited 8. No activation/rollback action logged; artifact rollback=unverified. Underlying probe cause not exposed. |
| [34243084351](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34243084351/job/102118131813) | 28e0397fb68e34e0cd1433f51d3027040dfc34a6 | Sep 8 15:11:33 -> 15:25:17 | 824s (13m44) | Compose dependency server unhealthy; exact server rollback also failed. Genuine startup-critical; Caddy rollback logged, artifact rollback=unverified, health=unknown. |
| [34245438397](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34245438397/job/102126224214) | 28e0397fb68e34e0cd1433f51d3027040dfc34a6 | Sep 8 15:33:18 -> 15:35:51 | 153s (2m33) | Initial production evidence probe exited 4. No activation/rollback action logged. Catch observed healthy previous SHA and labeled rollback=succeeded; this is not proof a rollback occurred. |
| [34205073983](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34205073983/job/101992650938) | 3b6bb9292488d6e9fc2b62ff0db1d13e177ebc61 | Sep 8 08:33:15 -> 08:46:44 | 809s (13m29) | Success. healthz=ok 08:41:59.273; exact /version logged 08:44:20.576. Artifact builderFresh=false accepted (builder observation already split). |
| [34127737153](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34127737153/job/101760599749) | 9a0b0e2ad8f08a5eae26a5c401dbb91bb3376a9d | Sep 7 13:31:19 -> 13:44:02 | 763s (12m43) | healthz=ok 13:39:11.251; 8 public routes passed, /v1/wanted HTTP503 alone failed broad smoke. Exact server rollback logged 13:41:13.098 and Caddy 13:41:28.532. Observation-only trigger by proposed policy. |
| [34110369910](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34110369910/job/101705076270) | f306e09dd3d7a4aea286d3efacee739b7c0686a3 | Sep 7 10:14:39 -> 10:27:16 | 757s (12m37) | healthz=ok 10:22:36.707; 8 public routes passed, /v1/wanted HTTP503 alone failed broad smoke. Exact server rollback logged 10:24:52.149 and Caddy 10:25:07.743. Observation-only trigger by proposed policy. |
| [34092352074](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34092352074/job/101648411594) | bca971273034d57fa5bc8be794e762fa03b7dbd3 | Sep 7 06:47:23 -> 07:18:03 | 1,840s (30m40) | Success. healthz=ok 06:55:19.907; activity smoke 06:56:24.456; identity/invariants only logged 07:17:17.094. Historical script still had builder wait here. |

The two /v1/wanted rollback cases prove a healthy, serving candidate was rolled back for a broad-route 503. They do NOT prove target `/version` success: exact identity was checked after broad smoke, and the evidence artifact only captured the restored previous SHA. The health of an exact-target process must therefore be described as unproven, not assumed.

The 1,252.638s silent interval in run 34092352074 corresponds in source ordering to exact identity read, builder-freshness wait, then invariant query. Most of it plausibly belongs to builder convergence, but the logs do not time those subphases separately. Builder wait was already moved to observation by baseline 085374b; do not count that as a new removal.

Baseline bounds at 085374b63720359bf1d712bc7123facfe56eaeca:

| Bound | Exact configured number | Limitations |
| --- | --- | --- |
| Production rollout job | 30 minutes / 1,800s | Job-level limit includes setup/artifacts; not a script transaction deadline. |
| Standalone deploy.ps1 / wrapper | No finite overall ceiling | `WaitForExit()` is unbounded; SSH ConnectTimeout=20 only bounds connection establishment, not remote execution. Build, scp, Docker, DB and wget calls have no consistent execution deadline. |
| Broad public smoke | 9 x (5 x 25s curl + 4 x 2s retry sleep) = 1,197s (19m57) | Requests only retry HTTP503; excludes command/SSH overhead. |
| Active privacy-safe log probes | 3 x 10s curl + up to 10 x 1s sleep = 40s | Docker/log validation and SSH overhead unbounded. Preflight additionally has 10 x (3s wget +1s sleep) =40s request/sleep envelope. |
| Direct health polling | 24 x 5s =120s sleeps | wget/SSH execution unbounded, so this is not a 120s wall-clock cap. |
| Optional authenticated admin smoke | 10 x (15s HttpClient +2s sleep) =170s | Only ConfigureAdmin; not in canonical automated invocation. |
| Historical builder wait at bca971 | 2,400 marker probes +2,399 x2s =4,798s sleeps (comment calls budget80m) | Each marker SSH/DB call unbounded; already absent from baseline. |

Source links: [baseline deploy.ps1](https://github.com/r2cuerdame/CodeSampleX/blob/085374b63720359bf1d712bc7123facfe56eaeca/deploy/lightsail/deploy.ps1), [baseline wrapper](https://github.com/r2cuerdame/CodeSampleX/blob/085374b63720359bf1d712bc7123facfe56eaeca/deploy/lightsail/deploy-production.ps1), [baseline workflow](https://github.com/r2cuerdame/CodeSampleX/blob/085374b63720359bf1d712bc7123facfe56eaeca/.github/workflows/production-deploy.yml), [historical builder wait](https://github.com/r2cuerdame/CodeSampleX/blob/bca971273034d57fa5bc8be794e762fa03b7dbd3/deploy/lightsail/deploy.ps1).

No production host was contacted, workflow dispatched, issue commented, or deployment performed. Local evidence: each RUN.json has full job metadata, RUN.log has rollout logs, RUN-artifacts/production-evidence-RUN/production-deploy-evidence.json has the original artifact.
