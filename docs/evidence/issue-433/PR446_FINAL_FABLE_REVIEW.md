# PR #446 final independent review (Claude Fable 5.1, high effort)

- Reviewed head: `ad59fb9d2e61740a80bb4ba933d87b5b5857b7cd` (branch head of PR #446, r2cuerdame/CodeSampleX)
- Base: `origin/main` = `9116765a834bca962418bcb19e2913f274778c47`
- PR commits: `56ae216`, `7a9d1e1`, `983917a`, `ad59fb9` (12 files, +817/-49)
- Reviewer: independent worker in worktree `csx-433-pr446-final-fable`; the earlier
  `PR446_FABLE_MERGE_REVIEW.md` verdict was not read before this conclusion was written.
- Date: 2026-09-16

## Verdict: HOLD

The three merge-blocking safety invariants **pass** and every shipped constant is
mutually consistent (host, controller, wrapper, workflow, docs table). The hold is
narrow and needs no production-code change: commit `ad59fb9` ships a quantitative
claim in its code comment, in `docs/issue-174-deploy-critical-path.md` and in a new
test that is contradicted by the same simulation harness it cites, once that
harness is put into the evidence state production actually reaches (F1, F2). The
docs tell operators that 240 s is "above every completable outcome" and that the
enclosing deadline "can no longer be the thing that decides recovery"; on the
lightest realistic recovery path the 240 s envelope is exactly what decides at
8 s or more per round trip, including at the 10.28 s singleton the claim is
derived from. Correcting the derivation and the simulation state (docs, comment,
test) converts this to PASS; the constants may stay as shipped.

## Safety invariants (all verified from source at the reviewed SHA)

| Invariant | Result | Evidence |
|---|---|---|
| Generic timeouts remain blocking | PASS | `execute_phase` raises on any deadline (`offline-migration.py:246-262`); `finalize` only obtains `cleanup_complete` from a phase that returned, so a timeout never reaches the rollback scripts. `strict_unowned=False` changes only the "unowned clients remain" branch (`:552-560`); every other refusal in `_cleanup_helper` still raises. Tests: `test_recovery_cleanup_timeout_remains_blocking_without_proof`, `test_recovery_cleanup_stays_bounded_and_still_blocks_rollback`, and the retired/superseded budget sub-tests all assert no `sh rollback-*.sh` call after a refusal. |
| Unowned clients are never signalled | PASS | `backend_signal` (`:564-575`) accepts only rows whose `applicationName` equals the owned helper name, or server rows that are already in `rollbackServerBackends`, which are populated solely by the network-address + `backend_start >= container StartedAt` + `usename='csx'` query (`:836-861`). The degraded path only records `unownedClientsAtCleanup` and returns `False`. The new late-server-backend block in `_cleanup_helper` reuses the same identity. Tests: `test_unowned_client_does_not_suppress_recovery_rollback` (asserts no signal for pid 42), `test_surviving_unowned_client_cannot_pass_empty_owned_cleanup`, `test_quiescence_still_refuses_a_foreign_database_client`. |
| Rollback remains exact and bounded | PASS | `rollback-server.sh` / `rollback-caddy.sh` are unchanged and keep their 90 s / 45 s budgets, each run in its own `execute_phase` with the prior (absent) deadline restored first, so cleanup spend cannot consume them (`test_exact_restoration_reserves_are_independent_of_cleanup_spend`). Rollback runs only after the cleanup phase returned (pass or proved-degraded). |
| Controller fails closed on a degraded outcome | PASS | `deploy.ps1:971` requires `phase -eq "rolled-back"` **and** `cleanup -eq "pass"`; `rolled-back-degraded` therefore throws, sets `retainDeployLock`, `rollback=unknown-host-outcome`, `failureClass=controller-unresolved`. `Resolve-CSXOfflineMigrationOutcome` accepts the phase only as *owned* evidence (`offline-migration.ps1:45-46`); `host_commit_test.go` covers it. Docs (`operations.md`, `deploy-lock-recovery-406.md`) describe exactly this. |
| Idempotent finalizer | PASS | `finalize` short-circuits on `committed`, `rolled-back`, `rolled-back-degraded` (`:867`). |

## Budget arithmetic (verified)

Structural wait floor inside `recoveryCleanup` (read from source, also pinned by
`test_cleanup_budget_covers_the_quantified_round_trip_minimum`):

| Component | Seconds |
|---|---:|
| `docker stop --time 10` cap, server (`stop_server_for_rollback`) | 20 |
| `docker stop --time 10` cap, helper (`_cleanup_helper`) | 20 |
| cancel grace windows, 3 x 5 s | 15 |
| terminate grace windows, 3 x 10 s | 30 |
| **Structural floor** | **85** |

Reserves and enclosing ceilings:

| Quantity | Value | Check |
|---|---:|---|
| `RECOVERY_CLEANUP_BUDGET_SECONDS` | 240 | 85 + 15 x 10.28 = 239.2, rounded up (the *15* is the disputed part, see F1) |
| `ROLLBACK_RESERVE_SECONDS` | 135 | 90 + 45, unchanged |
| `RECOVERY_BUDGET_SECONDS` | 375 | 240 + 135 |
| `RECOVERY_STOP_ALLOWANCE_SECONDS` = `TimeoutStopSec` | 480 | 375 + 105 margin (test requires >= 60) |
| `Wait-CSXMigrationTerminal` deadline | 480 | >= TimeoutStopSec |
| `Set-DeployPhase host-recovery` | 510 | >= 480 + 30 (two bounded 15 s evidence reads) |
| `Set-DeployPhase offline-migration` | M + 480 | >= TimeoutStopSec; equals `RuntimeMaxSec` = M + 480 |
| `deploy-production.ps1` ceilings | M+480 / 510 | match `deploy.ps1` and `offline-migration.ps1` |

Workflow serial failure ceiling (`production_deploy_workflow_test.go`, matches the docs list):
180 + 360 + 30 + 60 + (M+480) + 180 + 510 + 20 + 60 + 2x30 = **M + 1940 s**.
Step ceiling ceil(M/60) + 34 min >= M + 2040 s, so runner margin >= 100 s.
Table check: M=60 -> 35/38 min, M=61 -> 36/39, M=1200 -> 54/57, M=1800 -> 64/67. All correct.

Every wrapper still outlasts the finalizer by the same ~30 s structural margin the
previous 240/270 design had; the relationship is unchanged, only shifted by +240.

## Findings by severity

### F1 (Medium, HOLD) - the "quantified minimum" derivation omits the helper-cleanup half of the path

`ad59fb9` justifies 240 as `85 + 15 x 10.28`. The 15 is a constant in the test
(`QUANTIFIED_ROUND_TRIPS = 15`), not read from the code the way the 85 is, and it
does not describe the path. Measured with the PR's own `pressured_host` harness
(scratch instrumentation counting every spending call):

| Configuration (finalize from `phase=migrating`) | cost/round trip | recoveryCleanup | helperCleanup | outcome |
|---|---:|---:|---:|---|
| shipped fake (quiescence unset, helper+backend present) | 10.28 | 223.08 fail | 90.0 fail | refused after 18 round trips; helperCleanup 90 s decided |
| shipped fake | 6.3 | 178.6 pass | 89.3 pass | rolled-back (max completable ~179 s, as documented) |
| shipped fake | 6.4 | 180.4 fail | 90.0 fail | refused by **helperCleanup's 90 s sub-budget**, not the 5/10 s grace windows |
| realistic state (`quiescence=pass`), helper+backend present | 4.0 | 180.25 pass | 88.25 pass | rolled-back |
| realistic state, helper+backend present | 4.5 | 191.0 fail | 90.0 fail | refused by helperCleanup 90 s |
| realistic, helper already exited, no owned backend | 7.0 | 230.0 pass | 84.0 pass | rolled-back |
| realistic, helper already exited, no owned backend | 8.0 | **240.0 fail** | 76.0 fail | **refused by the 240 s envelope** |
| realistic, helper already exited, no owned backend | 10.28 | **240.0 fail** | 34.96 fail | **refused by the 240 s envelope** |

Consequences for the shipped text:

- `offline-migration.py:29-52` and `docs/issue-174-deploy-critical-path.md:88-111`
  say the path "pays at least 15 such round trips" and that 240 "is above every
  completable outcome: the enclosing deadline can no longer be the thing that
  decides recovery". Helper cleanup alone pays 12 or more round trips at its
  lightest (60 s at 5 s/trip in the fake), so the path pays well over 15, and on
  the lightest realistic path the 240 s envelope is the decider from 8 s/trip
  upward, including at the 10.28 s singleton the number was derived from.
- The "roughly 6.4 seconds" threshold is real in the shipped fake, but its cause is
  the nested `helperCleanup` 90 s budget (unchanged by this PR), not "the nested
  5-second cancel and 10-second terminate windows refuse on their own".
- Outcome in every refusing row is still fail-closed (phase stays `migrating`, no
  rollback script runs, lock retained), so this is a documentation/test-fidelity
  defect, not a safety defect. It is HOLD-grade because the commit's stated purpose
  is to make the budget "quantified, not an estimate", and operators will read the
  false version in `issue-174`.

Fix (docs/comment/test only): state the true round-trip count per sub-phase, name
helperCleanup 90 s as the binding nested budget, and drop or re-scope the "above
every completable outcome" sentence. If the author wants the sentence to be true,
`helperCleanup` (90) and the envelope would both need re-derivation and the 480 s
stop allowance re-checked; that is a design choice, not required for merge.

### F2 (Medium, HOLD with F1) - the pressured simulation runs from an unreachable evidence state

`pressured_host` saves `phase="migrating"` without `quiescence="pass"`. In
production `run()` cannot reach `migrating` without `stop_builders` having saved
`quiescence="pass"` first, so the simulation skips the new late-server-backend
block (5 s + 10 s grace, 4 or more round trips) and the final unowned-client check
inside `_cleanup_helper`. All four `ad59fb9` characterisation numbers (50/150/172/
~179) are for this unreachable state; with `quiescence="pass"` the same fake
completes only up to ~4 s/trip. Fix: set `quiescence="pass"` and
`originalServerNetwork` in `pressured_host` (as the other finalize tests do) and
re-freeze the numbers; add the helper-absent variant, which is the common
recovery shape.

### F3 (Low) - `migrationLedgerAfter` query now runs inside the migration budget

`_migrate` (`:479-484`) adds a ~10 s psql round trip after the helper exits, still
under the `migration` phase deadline. A migration that finishes in the last few
seconds of its SQL budget can now fail the phase post-check ("migration deadline
exceeded") after the schema moved, forcing a rollback of the previous image onto
a forward-migrated database. That outcome class already existed (verification
failure) and the ledger evidence it buys is valuable; note it as a residual.

### F4 (Low) - Windows-only Go test failures are environmental and outside the PR

Unfiltered `go test ./deploy/lightsail/` fails `TestTheRemoteRunnerIsFailClosed`
and `TestCanceledGenerationCannotRunALateRemoteCommand` on this workstation
(`flock unavailable`, Windows `TIMEOUT` shadowing `timeout`). Neither test nor its
fixtures is touched by the PR; Linux CI exercises them. Not attributable to #446.

### Reviewed and found correct (no finding)

- Loop restructure (`expired` computed before the observation) gives exactly one
  post-deadline observation and is covered by the `*_observes_again_after_one_slow_poll`
  and `*_still_refuses_a_surviving_backend` tests.
- `remember_server_backends` dedupe by `(pid, backendStart)` replacing the stored
  row fixes the previous "unrecorded rollback server backend" refusal when
  `queryStart`/`queryHash` changed between observations; the membership check in
  `backend_signal` is unchanged.
- `TimeoutStopSec` applies per systemd stop state, so `ExecStopPost` gets its own
  480 s; the controller's terminal wait starts after the runtime cap fires, so it
  outlasts the finalizer.
- The workflow YAML change is limited to the two comment lines and the `+ 34`.

## Recovery/rollback safety summary

Host: cleanup budget 240; on timeout or any proof failure the finalizer raises
before `rolling-back`, the lock stays held, no restoration is attempted. On proof
(pass or degraded-by-foreign-client) the exact previous server and proxy are
restored from independent 90 s / 45 s reserves; a degraded restore is recorded as
`rolled-back-degraded` and the controller retains the lock for reconciliation from
`unownedClientsAtCleanup` (no query text). Controller: every wrapper is at least
the host allowance it observes, enforced by
`test_recovery_budgets_fit_every_enclosing_stop_and_controller_ceiling` and
`TestProductionCriticalPathHasAnExplicitRollbackReserve`. Rollback is strictly
better than main's 60 s envelope, which sat below the 85 s structural floor.

## Test evidence (run at the reviewed SHA on this workstation)

| Command | Result |
|---|---|
| `python -m pytest deploy/lightsail/offline_migration_test.py -q` | 88 passed, 1 skipped (POSIX process groups), 35 subtests passed |
| `go test ./scripts/ ./deploy/lightsail/ -run 'TestMigrationBudget|TestProductionCriticalPath|TestHostCommit|Migration|Recovery|Rollback' -count=1` | ok, ok |
| `go test ./scripts/ ./deploy/lightsail/ -count=1` (unfiltered) | scripts ok; deploy/lightsail fails only the two Windows-environmental tests in F4 |
| Scratch probes of `pressured_host` (not committed) | table in F1 |

## Residual risks after the fix

- Under sustained 8 s or more per round trip the finalizer refuses and the site
  stays on a stopped server until an operator reconciles from the retained lock;
  the PR bounds that tail but does not remove it, and helperCleanup 90 s is the
  tighter nested limit on the realistic path.
- The ~30 s margin between controller waits and host stop allowance is unchanged
  from the previous design and remains small under SSH latency.
- Before quiescence passes (preflight/quiescence failure), the finalizer performs
  no foreign-client check at rollback time; pre-existing, unchanged.
