# Issue #415 Result: Deploy proxy readiness rejects healthy 2-second responses under production load

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/415
- Branch: `issue/415-deploy-proxy-readiness-rejects-healthy-2`
- Milestone: v0.1.194
- Related: r2cuerdame/CodeSampleX-Farm#127, #404, #402, #416 (merged recovery), #417 (superseded)

## What was wrong

Production deploy run
[34842032504](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34842032504)
(target `362ee40d`, v0.1.184) passed preflight, quiescence, migration,
helperCleanup, migrationVerification, readiness, exact served revision and
release identity, then `proxyReadiness` ran its full 60-second budget
(`outcome: failure`, `elapsedSeconds: 60.0`, no `proxyHealth`) and the host
rolled back. Every probe curl was capped at `--max-time 2` with a 3-second
outer command limit, while five loopback TLS `/healthz` probes taken on the
host after rollback returned HTTP 200 in 1.597-2.006 s with Caddy, the server
and the DB each near 310% CPU.

## What this branch delivers

Behaviour is the one #416 merged twenty minutes after that run (`df21be9`,
2026-09-14T12:31Z): 5-second curl cap, 3-second connect timeout, 6-second
outer command limit, 1-second retry pause, 60-second phase. This branch:

1. Names those limits in `deploy/lightsail/offline-migration.py`
   (`PROXY_READINESS_BUDGET_SECONDS`, `PROXY_PROBE_CONNECT_TIMEOUT_SECONDS`,
   `PROXY_PROBE_MAX_TIME_SECONDS`, `PROXY_PROBE_COMMAND_SECONDS`,
   `PROXY_PROBE_RETRY_PAUSE_SECONDS`) with the measurement that justifies
   them recorded beside the other host budgets. No runtime change.
2. Adds three regression tests to `deploy/lightsail/offline_migration_test.py`
   that model curl honouring `--max-time` (exit 28):
   - `test_proxy_probe_limits_admit_the_measured_healthy_production_latency`:
     the slowest measured healthy response (2.006 s) passes on the first
     attempt with the exact argv (`--noproxy *`, `--connect-timeout 3`,
     `--max-time 5`, `--resolve codesamplex.dev:443:127.0.0.1`, `-sS`,
     `https://codesamplex.dev/healthz`), a 6-second outer limit, a
     60-second phase budget, and no process-group kill.
   - `test_proxy_slower_than_its_curl_cap_exhausts_the_phase_in_ten_bounded_attempts`:
     a proxy that never answers inside the cap ends the phase at exactly 60 s
     after ten probes (t = 0, 6, ..., 54); the eleventh is refused before it
     spawns; no `proxyHealth`, no commit, no representative request, phase
     and activation both `failure`, operation deadline restored.
   - `test_hung_proxy_probe_is_killed_at_the_outer_command_limit_and_retried`:
     a curl that ignores its cap is killed with its process group at the
     outer limit, retried, and the phase still ends at 60 s (nine attempts;
     the last one clamped to the remaining 4 s), raising
     `proxy health deadline exceeded`.
   The pre-existing tests keep pinning the exact HTTP 200 + `ok` body check,
   TLS/loopback routing and fail-closed rollback.

Mutation check: with `PROXY_PROBE_MAX_TIME_SECONDS = 2` all three new tests
fail (the 2.006 s probe is refused, attempts fall to 3-second spacing, and
the hung-probe schedule shifts); restoring 5 turns them green.

## Verification (this workstation, Windows 11, 2026-09-17)

| Check | Result |
| --- | --- |
| `python -B deploy/lightsail/offline_migration_test.py` | PASS — 94 tests, 1 skipped (91 before this branch) |
| `go test -count=1 -run TestOfflineMigrationRecovery ./deploy/lightsail` via `run_observed_command` | PASS |
| `go test -count=1 ./deploy/lightsail` via `run_observed_command` | PASS (90.1 s; this is the Windows CI job's package run) |
| `go vet ./deploy/lightsail` | PASS |

Windows CI runs on push to `main`, not on pull requests
(`.github/workflows/ci.yml`, cost decision); the package run above is that
job's command on the canonical Windows reproduction machine.

## Production proof (deploys only through Production deploy)

Every Production deploy since `df21be9` carried the 5-second cap in its
operational SHA and passed `proxyReadiness`; the phase timings below come from
each run's `production-evidence-<run>` artifact
(`production-deploy-evidence.json.migration.json`):

| Deploy run | Operational SHA | Release | proxyReadiness | elapsed s |
| --- | --- | --- | --- | ---: |
| 34842032504 (evidence run, 2 s cap) | `362ee40d` | v0.1.184 | failure | 60.000 |
| 34845884143 | `8318b442` | v0.1.184 | pass | 4.009 |
| 34868698772 | `63ba4654` | v0.1.186 | pass | 1.606 |
| 34953128122 | `8e822f11` | v0.1.189 | pass | 2.277 |
| 35006177462 | `10044923` | v0.1.192 | pass | 3.944 |
| 35012410926 | `71f3b433` | v0.1.193 | pass | 2.407 |
| 35033295563 | `0c620cf9` | v0.1.194 | pass | 1.266 |
| 35051578435 | `c1bc6205` | v0.1.195 | pass | 1.993 |
| 35080182177 | `e8dbf06e` | v0.1.196 | pass | 1.999 |
| 35144143729 | `6c105956` | v0.1.197 | pass | 3.681 |

Five of the nine passing phases took longer than the retired 2-second cap;
none approached the 60-second budget. The v0.1.184 recovery deploy
(34845884143, `phase: committed`, `conclusion: success`, `proxyHealth: ok`,
`rollback: not-needed`) is the direct recovery for CodeSampleX-Farm#127.

Independent post-deploy observation for the latest successful deploy
(run [35144846635](https://github.com/r2cuerdame/CodeSampleX/actions/runs/35144846635)
for deploy 35144143729, observer SHA = deployment SHA `6c10595`, tracking
issue #455): exact target SHA `ca6480e9` and image digest observed,
classification `incident-only` (known #174/#433 host pressure: peak server
CPU 343.84%, 11 pool-busy observations, builder not yet converged),
`rollbackRequested: false`, restart events 0, OOM events 0, recommended action
"do not automatically roll back a healthy exact-SHA server". The proxy
readiness change is not implicated in that classification.

The eight Production deploy failures on 2026-09-17 (runs 35214192104 through
35219599662, target `2fcce190`) all stop in preparation with
`remote script failed (73) another deploy owns /opt/codesamplex/.deploy-lock`
before the host supervisor starts; they never reach proxyReadiness and are
the lock-recovery work of `578b915`/`76e16a3`, not this issue.

## Deployment impact

Source-only: constants and tests. The production host already runs the
probe limits this branch pins (every deploy since v0.1.184 recovery). Merging
needs no dedicated deploy; the next batched Production deploy ships it with
the rest of `main`, and its `proxyReadiness` timing lands in that run's
evidence artifact like the rows above.
