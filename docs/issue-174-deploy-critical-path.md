# Production deployment critical path (#174)

This continues merged #259/#261/#260 through the existing canonical production
workflow. The in-flight run [34311753137](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34311753137)
was observed to SUCCESS before these changes. It took 33m58s, including
1561.121s of SQL migration. Its actual controller had a 70m step / 73m job cap;
the earlier 27m description no longer matched current main.

The immutable operations/payload separation, canonical main CI, release/farm,
GitHub issue provenance and pinned SSH identity gates remain required.
This change updates deployment orchestration only, without changing the released
payload, migration SQL or web UI.

## Minimal acceptance on the host

The host-owned systemd unit supervises the whole offline operation. It stops
builders, runs the existing migration with its own deadline, cleans the exact
helper/backends, and verifies the ledger, required indexes and repair barrier.
Full-corpus scans are not part of activation.

Startup, direct /healthz, exact process/image /version, installed release
generation and two proxy requests (/features plus exact /version) share one
180-second host deadline. Direct readiness gets at most 45 seconds within that
budget. Proxy readiness is also bounded within it. Compose recreates only the
server and proxy, with --no-deps after DB readiness has already been proved.
There is no second broad stack recreation or duplicate Caddy reload.

The host durably commits immediately after its own minimal acceptance. The
controller reads that exact evidence; it no longer repeats health/smoke checks
over several SSH trips or holds the service for a 600-second ACK lease. A lost
controller response or termination after durable commit cannot roll back a
validated candidate. A transport failure while the host is still working only
observes terminal evidence within the recovery budget. The controller never
stops healthy pending work; the host retains its own SQL/startup deadlines.
Unverifiable evidence retains the deployment lock for an
owner instead of inventing PASS or racing a rollback.

Rollback applies to actual migration/data-integrity, startup or exact identity/
representative availability failures before commit. Broad route, privacy,
latency, 503, builder pressure and convergence checks belong to the existing
post-deploy observation workflow. An observation failure never automatically
rolls back. Deployment PASS does not close the ongoing #174 incident.

## Measured caps and independent recovery

[Empirical p50/p95/max and sample identities](evidence/issue-174-deploy-budgets.md)
justify the changed bounds. There are only two instrumented runs; singleton
migration/recovery percentiles are descriptive, not reliable population tails.
The timed-out 1200s migration is explicitly excluded from successful percentiles.

Controller caps in seconds: preparation 180, staging 240, config promotion 30,
migration setup 60, offline migration M+240, activation/acceptance 180, host recovery 270, failure
fence 20, cleanup 60, and two 30-second identity probes. The worst serial
canonical failure path is M+1340 seconds. ceil(M/60)+24 minutes adds at least
100 seconds of runner margin; the job adds three minutes for checkout/evidence.
The workflow validates M=60..1800 before production credential access.

| SQL budget | Deploy and verify cap | Job cap |
|---|---:|---:|
| 60s | 25m | 28m |
| 1200s (default) | 44m | 47m |
| 1800s (maximum) | 54m | 57m |

These are failure ceilings, never fixed waits. The successful observed SQL took
26m1s; imposing a 27m total cap on that same migration would repeat a timeout.
The user-visible non-migration startup/acceptance boundary is always three
minutes and does not inherit the SQL budget.

Host phase budgets are preflight 60, quiescence 60, migration M, helper cleanup
90, migration verification 30, and activation 180. Every child command clips to
the enclosing deadline. Host systemd runtime M+480 has its own separate 240s
stop allowance. Recovery reserves cleanup 60, server restore 90 and Caddy
restore 45 seconds; the controller reserves 270 seconds for recovery/evidence.
Exact snapshots and backend ownership checks remain mandatory.

Both controller and host phase timings are retained in the production artifact,
including success/failure and actual elapsed time for each host phase.
Readiness and proxyReadiness are nested within activation; do not sum them twice.

## Validation and deployment scope

Run focused go test -count=1 ./deploy/lightsail ./scripts ./internal/deploygate
with PowerShell 7 and POSIX tools, actionlint on both workflows, PowerShell AST
and shell syntax checks. Behavioral tests cover shared deadlines, process-tree
cleanup, wrong health/identity/content, migration failures, malformed evidence,
lost controller responses and post-commit termination.

This is backend deployment orchestration; no artificial DevHotel browser room
is required by DEVHOTEL_PREDEPLOY_V1. No production deployment is dispatched by
this follow-up. A future payload/web change must pass its required exact-build
DevHotel acceptance before the canonical production workflow is dispatched.
