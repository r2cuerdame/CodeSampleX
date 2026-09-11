# #174 live builder load-control audit — 2026-09-11

The running v0.1.158 target has no repository-supported operation that pauses only its full builder while preserving user traffic. The supported interval and pass-deadline settings are read at startup. Applying them requires recreating the server, and a longer interval does not delay the immediate first pass. No production pause, query cancellation, restart, deployment, configuration change, snapshot, or capacity purchase was performed for this audit.

This is an immediate recovery blocker under the task's no-restart/no-new-cost constraints. It does not prove that a future software change cannot reduce demand. It establishes that there is no safe existing live control to apply to the already-running target. A capacity/cutover decision remains with the user; another deployment also remains blocked by the separately reconciled ownership and actual health gates.

## Exact source examined

GitHub main at `7f2ab9a6` includes merged #365/#366. The four control-bearing files below are byte-identical between released target `4b08ed5093740268f50682011e918dc8d3744f35` and that main: `cmd/csx-server/mux.go`, `internal/compatibility/builder.go`, `internal/serverstore/config.go`, and `deploy/docker-compose.yml` (`git diff TARGET HEAD -- <paths>` produced no changes). Therefore this control audit applies to the active binary's source, not only unreleased main.

- [Builder startup, mux.go:156](https://github.com/r2cuerdame/CodeSampleX/blob/4b08ed5093740268f50682011e918dc8d3744f35/cmd/csx-server/mux.go#L156): one builder goroutine runs inside the HTTP server process. Only the shared context is handed to it; no independent pause/cancel handle is exported.
- [Startup configuration, config.go:90](https://github.com/r2cuerdame/CodeSampleX/blob/4b08ed5093740268f50682011e918dc8d3744f35/internal/serverstore/config.go#L90): `CSX_SNAPSHOT_INTERVAL` accepts only positive durations. Zero/negative/invalid values leave the five-minute default. `CSX_SNAPSHOT_PASS_TIMEOUT=0` removes the ceiling; it does not disable the builder.
- [Run loop, builder.go:88](https://github.com/r2cuerdame/CodeSampleX/blob/4b08ed5093740268f50682011e918dc8d3744f35/internal/compatibility/builder.go#L88): first pass starts immediately. Subsequent successful passes wait the normal interval; failures receive five retries at 1/2/4/8/16 seconds plus jitter before deferral for the normal interval.
- [Resume boundary, builder.go:218](https://github.com/r2cuerdame/CodeSampleX/blob/4b08ed5093740268f50682011e918dc8d3744f35/internal/compatibility/builder.go#L218): resumption requires a completed watermark within 24 hours. [Ceiling recovery, line 286](https://github.com/r2cuerdame/CodeSampleX/blob/4b08ed5093740268f50682011e918dc8d3744f35/internal/compatibility/builder.go#L286) postpones scheduled full repair, but cannot invent a safe incremental starting point on a cold/stale start.
- [Compose settings, docker-compose.yml:40](https://github.com/r2cuerdame/CodeSampleX/blob/4b08ed5093740268f50682011e918dc8d3744f35/deploy/docker-compose.yml#L40) forward startup environment values. [The runbook, operations.md:1292](https://github.com/r2cuerdame/CodeSampleX/blob/7f2ab9a6/docs/operations.md#L1292), explicitly requires server recreation to apply an override.
- [Admin routes, admin.go:163](https://github.com/r2cuerdame/CodeSampleX/blob/7f2ab9a6/internal/admin/admin.go#L163) expose no builder pause/resume operation. Searches of server, admin, HTTP API, configuration, deployment and builder source found no runtime builder signal, file flag, authenticated endpoint or separate builder service.

## Candidate controls and why they do not recover this live process

| Candidate | Actual behavior / disposition |
| --- | --- |
| Increase snapshot interval | Affects later passes after startup configuration is read; does not interrupt the current pass or prevent the immediate first pass. Requires recreation. |
| Shorten pass timeout | Requires recreation; expiration is an error, then fast retry. With the stale completed watermark it can repeat exhaustive work. It is not an operator pause. |
| Set either setting to zero | Interval falls back to five minutes; timeout becomes unbounded. Neither pauses. |
| Cancel a PostgreSQL query | No supported builder-owned live cancellation entry point. Ordinary query errors trigger the retry series, without a durable operator-selected defer deadline. Repeated cancellation can repeat costly work rather than bound it. |
| Pause/stop/CPU-limit the server process or container | The same process serves HTTP and health. This cannot isolate its builder goroutine and would stop or constrain user traffic too. |
| Disable the DB pool guard, relax health, publicness, or authentication | Does not create CPU capacity or a builder pause and would weaken existing protection. The strict registry check, reserved health capacity and fail-closed health behavior remain intact. |
| Restart/redeploy the same target | Changes the exact activation identity, can start another full pass, and cannot repair the failed controller's provenance. Not performed. |

Materialized writes occur in bounded batches; a completed-pass watermark advances only after successful final stats work ([builder.go:845](https://github.com/r2cuerdame/CodeSampleX/blob/4b08ed5093740268f50682011e918dc8d3744f35/internal/compatibility/builder.go#L845)). This permits normal failure recovery; it does not make destructive state edits or a forged fresh watermark safe. Preserve raw evidence, receipts, materialized output, repair generation, blobs and owner evidence. Do not falsify `generatedAt` to force the incremental branch.

## Fresh read-only evidence

At 15:41:42–15:42:09 UTC, a certificate-valid Playwright/Edge public panel using the already documented origin mapping returned exact v0.1.158 / `4b08ed50…`. Health happened to return 200 (2.308 s TTFB), but pgx and OpenTelemetry detail returned 503 (1.187 / 0.906 s), stats returned 500 (3.681 s), and the representative sample took 6.522 s. This is not stability acceptance. Raw local evidence: `.tmp/public-routes.json`.

At 15:42:20 UTC, the unchanged loopback health URL produced HTTP 200 (4 s), HTTP 503 (7 s), then a timeout (7 s). CPU steal was 78–79%; CPU pressure avg10 was 79.21%. Server memory was 596,996,096 / 805,306,368 bytes, OOM counters zero, and cgroup CPU `nr_throttled=0`. Raw local evidence: `.tmp/host-health-pressure.json`. These are actual resource/health failures, not a reason to weaken health validation.

AWS read-only APIs at 15:45:25 UTC confirmed `csx-prod-1` running `small_3_0`, 2 vCPU / 2 GiB / 60 GB, in `ap-northeast-2a`. Twelve five-minute samples in the requested 14:45:25–15:45:25 UTC window reported CPU utilization 19.998329–20.002731% and remaining burst capacity 0.011048967–0.011109543%. Together with high steal and no cgroup quota throttling, this supports continuing host CPU-burst exhaustion. [AWS describes baseline and burst behavior](https://aws.amazon.com/lightsail/faq/); the diagnosis is an inference from the measured combination, not the CPU-utilization number alone.

Local retained AWS response hashes:

| Evidence | SHA-256 |
| --- | --- |
| `.tmp/load-control-audit/CPUUtilization.json` | `113a14afd16e4dd47dde1bf6f0d3460900936d4fc4aef4e2f3ee0bb5340e8579` |
| `.tmp/load-control-audit/BurstCapacityPercentage.json` | `f9f49d41179ec6d46c2b1f00e508f2b07036d90b82cd2d05fecc13a0d879525a` |
| `.tmp/load-control-audit/bundles.json` | `af6dcebc9c8c93c69f402da1c6cdbfe2a64935bab34f046e8f709ca5617a54fe` |

## User cost/cutover decision required

`get-bundles --no-include-inactive` in Seoul at 15:45:25 UTC returned the following Linux/public-IPv4 bundles. These are current base monthly list prices, not an approved purchase or a claim that any size is sufficient; [AWS's bundle documentation](https://docs.aws.amazon.com/lightsail/latest/userguide/amazon-lightsail-bundles.html) also lists their specifications and monthly prices.

| Bundle | vCPU / RAM | Base USD/month | Increase over current |
| --- | --- | ---: | ---: |
| `small_3_0` (current) | 2 / 2 GiB | 12 | 0 |
| `medium_3_0` | 2 / 4 GiB | 24 | 12 |
| `large_3_0` | 2 / 8 GiB | 44 | 32 |
| `xlarge_3_0` | 4 / 16 GiB | 84 | 72 |

The required decision is whether to authorize a named capacity class and spending cap, including temporary overlap, snapshot/storage charges, and any cutover outage. RAM/vCPU counts alone do not establish sustained CPU headroom; validate the selected bundle's CPU behavior against measured demand before a purchase. A replacement-host plan must fence database writes, retain a restorable backup, prove the restored ledger/data/blob state, define a single-writer cutover and rollback boundary, and preserve exact deployment evidence before moving traffic. The generic snapshot/larger-instance/static-IP paragraph in the runbook is not that executable plan.

Until that decision and reviewed cutover plan exist, safe actions here are bounded read-only public/loopback probes, owner/identity evidence collection, AWS metric reads and code/CI work. No automated resource creation, snapshot, resize, billing change or host mutation is included in this audit. Continue reporting actual failed health and builder progress; do not claim a pause, recovery, successful reconciliation or normal observer acceptance from one transient 200.
