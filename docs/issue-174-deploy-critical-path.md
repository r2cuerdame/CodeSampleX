# Production deployment critical path (#174)

This change refactors only the canonical `production-deploy.yml` →
`deploy-production.ps1` → `deploy.ps1` → `post-deploy-observation.yml` path.
It does not deploy, dispatch a workflow, merge during incident recovery, or
introduce an emergency workflow. GitHub issue #174 remains the incident record.

## Historical evidence and minimal split

[The timing audit](issue-174-deploy-timing-audit.md) records eight actual GitHub
deployments, their immutable targets, step timestamps, artifacts and failure
classification. Two rollouts restored the prior server because the broad
`/v1/wanted` probe returned 503 after direct health and eight other routes passed.
Target `/version` had not yet been measured in those runs; their potential
viability is supported, but exact-target health is not proved retrospectively.
Two unhealthy-container activation failures remain rollback-critical. Two
initial evidence failures never activated a build and must not be reported as
successful rollback merely because the old server is still healthy.

| Remains in activation / immediate rollback scope | Runs in independent observation |
| --- | --- |
| Immutable target/previous SHA and previous image; pinned SSH identity | Broad nine-route public surface, content type and canonical marker checks |
| Exact image/config/env/release-directory rollback snapshots | Privacy-safe Caddy sandbox and live synthetic-marker log validation |
| Signed release asset integrity, image/config activation and migration/startup | Unauthenticated admin route state and activity schema/key checks |
| Direct `/healthz`, migration ledger version, image label and exact `/version` | Corpus-wide source/derived invariants and failure-evidence quality |
| Two representative proxy requests: `/features` canonical content and `/version` exact SHA | Builder convergence, pressure, cold/symbol latency and evidence-quality diagnostics |

Signed installer asset verification stays before promotion: serving the wrong
or unverified executable is a security/integrity failure. Caddy syntax validation
also remains before activation. The separate manually requested `ConfigureAdmin`
operation retains its bounded authenticated credential check before committing
DPAPI state; the canonical automated workflow does not request that operation.
Deployments no longer purge historical access logs. Any needed irreversible
privacy cleanup belongs to the incident owner, outside this transaction.

## Exact configured ceilings

These are command/wait wall-clock budgets, not predictions of typical runtime.
Local scheduling, artifact upload and queue/environment approval time are not
included in the script sums; GitHub additionally enforces step/job timeouts.

| Boundary | Before (baseline d9f98a1e; deployment code unchanged from 085374b) | After |
| --- | --- | --- |
| Standalone deploy success commands/waits | No finite overall script ceiling | Preparation 600s + staging 300s + activation 240s + cleanup 60s = **1,200s** |
| Canonical wrapper success | No finite overall script ceiling | One identity probe 30s + 1,200s = **1,230s (20m30s)** |
| Worst failed activation plus recovery/evidence | No separate rollback reserve | 30s + 600s + 300s + 240s + rollback 300s + cleanup 60s + failure identity 30s = **1,560s (26m)** |
| `Deploy and verify` step | Only enclosing job timeout | **27 minutes**; retains margin beyond script recovery budget |
| Rollout job / eligibility job | 30m / 10m | **Unchanged: 30m / 10m** (40m combined, excluding queue/approval) |
| Broad public-route request/sleep envelope in transaction | 9 × (5 × 25s + 4 × 2s) = **1,197s (19m57s)** | **0s**; moved to observation |
| Representative proxy request envelope | Part of broad nine-route probe | 2 × 10s = **20s**, no retries |
| Dynamic privacy probes in transaction | 40s live + 40s sandbox request/sleep envelopes, plus unbounded Docker/log overhead | **0s**; moved to observation |

The 80-minute builder wait was already removed from the deployment transaction
before this branch; this change does not claim that improvement a second time.
The observer remains independent: extended checks have a 600s remote budget plus
15s transport allowance; builder polling has an actual 4,800s wall-clock window;
the final detailed sample has a 180s budget plus 15s transport allowance. Observer
failure never extends the production concurrency group or invokes deployment.

Each deployment phase and each external command prints elapsed time and its
ceiling. The JSON artifact records phase timings and configured budgets. A
failure before activation has a separate bounded 20s fencing phase; it cannot
reach the worst-case activation+rollback sum above. Cleanup and rollback receive
independent reserves when a success phase expires. Server/Caddy restoration has
separate 170s/80s command budgets within the 300s reserve, leaving time for fencing
and error handling. Cleanup reserves a 15s mandatory lock-release command before
optional remote image cleanup.

## Failure policy

Activation/startup, target identity, migration version, representative serving,
and critical asset-integrity failures retain exact snapshot restoration. The
wrapper consumes identity evidence produced inside that rollback boundary;
there is no fallible optional postcommit collection that can overturn success.

Remote commands serialize under a bounded host-side `flock`. A persistent
per-invocation cancellation marker is checked inside the lock, so a delayed
normal command cannot execute after recovery fences its generation. Promotion
intent and remote journals cover lost acknowledgements. Unknown SSH/timeout or
Docker-daemon completion is not treated as proved failure completion: retain the
deployment lock, report rollback unverified, and require the primary owner to
reconcile the live container before restoring it. Unverified recovery similarly
retains the lock. The fence proves shell serialization, not daemon completion.

Observation defaults to `incident-only`, `rollbackRequested=false`, including
latency, 503, convergence delay, restart/drift, unavailable telemetry, source
counter decreases and derived-ledger imbalance. Current code permits a rollback
**request**, never an automatic rollback, only for a positively detected unique
synthetic privacy marker in the live log, with unchanged exact deployment
SHA/image/start identity before and after the probe. Missing logs, failed reads,
generic severity labels, old unsafe fields and unavailable identity cannot
upgrade an incident. The request is evidence for the primary incident owner.

Source continuity is compared against an authenticated prior deployment artifact
or its successful observation artifact when available. Counts collected only after activation cannot prove a missing
pre-activation baseline; unavailable/invalid baselines explicitly report
`not-assessed`. No source-loss or incident-recovery PASS may be inferred from that
status. Migration-specific offline/quiescence/reconciliation acceptance contracts
remain separate release/deploy eligibility requirements; this refactor does not
waive the existing migration HOLD or manufacture migration acceptance evidence.

## Validation and owner recommendation

Local validation on 2026-09-09: **222 test cases PASS, zero skipped**, executed
through CSX with the official hash-verified portable PowerShell 7.6.6 runtime.
Both workflow files pass actionlint; PowerShell AST, POSIX shell syntax and
`git diff --check` pass. Shellcheck/pyflakes were unavailable locally and were
disabled in actionlint; the separate shell syntax checks remain recorded.

Run the deployment/workflow/eligibility Go tests with PowerShell 7 and POSIX shell
tools available, `actionlint` on both canonical workflows, PowerShell AST parsing,
and shell syntax validation. Runtime fixtures exercise process timeout/nonzero
exit, exhausted phase and independent rollback reserve, exact image/env/config
restoration, wrong served SHA, representative request failures, and observation
classification including positive privacy evidence and unavailable telemetry.
Windows transport fixtures substitute unavailable `flock`; Linux CI must exercise
the real host locking utility before merge.

This is backend deployment orchestration, with no web UI/APK payload change and
no deployment performed. No artificial DevHotel browser room is needed for these
script/contract tests. The eventual deployable application target still requires
its applicable DevHotel acceptance evidence under `DEVHOTEL_PREDEPLOY_V1`; an
unavailable/failed required room blocks that deployment. No Orca fallback is used.

**Recommendation:** the primary incident owner should review and merge this
focused PR after CI passes and their production recovery work is stable. Do not
dispatch this refactor while that owner is changing production. Rebase/review any
overlapping canonical-pipeline changes first, then apply the usual exact merged
target CI, Release/farm, migration, DevHotel and GitHub tracking gates. Use only the
existing production workflow. Deployment PASS does not close the sustained
performance/convergence incident in #174.
