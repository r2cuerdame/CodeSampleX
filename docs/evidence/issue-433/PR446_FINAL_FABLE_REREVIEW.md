# PR #446 final independent rereview (Claude Fable 5.1, high effort)

- Reviewed head: `58e64adcda6e17e7766bf86cc018b8c7250eecd8` (branch head of PR #446, r2cuerdame/CodeSampleX)
- Base: `origin/main` = `9116765a834bca962418bcb19e2913f274778c47`
- Prior review: `PR446_FINAL_FABLE_REVIEW.md` (commit `99ff0e2`) returned HOLD on
  `ad59fb9d2e61740a80bb4ba933d87b5b5857b7cd` for findings F1 and F2 (derivation and
  simulation state, docs/comment/test only). This rereview is that hold's remediation
  verification; the prior report was read for that purpose.
- Correction range reviewed in full: `ad59fb9..58e64ad` (one commit, `58e64ad`)
- Reviewer: independent worker in worktree `csx-433-pr446-fable-rereview`
- Date: 2026-09-16

## Verdict: PASS

The correction is confined to comments, documentation and tests. The Python source
is byte-different only in comment text: the abstract syntax tree of
`deploy/lightsail/offline-migration.py` at `ad59fb9` and at `58e64ad` is identical
(verified with `ast.dump`, attributes excluded, digests equal). No PowerShell,
workflow, Go or shell file changed in the range. Every quantitative claim the
corrected comment, docs and tests make was re-derived on this workstation with an
independent probe and matches to the decimal. The three merge-blocking safety
invariants and the budget arithmetic verified at `ad59fb9` therefore carry over
unchanged, and the two HOLD findings are resolved.

## Scope of the correction (`ad59fb9..58e64ad`)

| File | Change | Runtime effect |
|---|---|---|
| `deploy/lightsail/offline-migration.py` | Rewrote the header comment at lines 37-59: "quantified minimum, 85 + 15 x 10.28" replaced by "bounded envelope backed by the simulation"; names `helperCleanup` 90 as the tighter nested limit with the helper present and the 240 envelope as the decider with the helper gone; drops the "5-second cancel and 10-second terminate windows refuse on their own" claim | None (AST identical) |
| `deploy/lightsail/offline_migration_test.py` | `pressured_host` gains `helper_present` and saves `quiescence="pass"` and `originalServerNetwork`; `QUANTIFIED_ROUND_TRIPS` and `COMPLETABLE_ROUND_TRIP_SECONDS` replaced by per-shape completable costs (4.0 / 7.0); the derivation test now checks the envelope encloses the server-stop waits plus the nested helper budget instead of `85 + 15 x 10.28`; characterisation numbers re-frozen for both shapes; two new refusal tests (helper-present at 4.5 s, helper-absent at 8 s); the all-10.28 test now covers both shapes | Test only |
| `docs/issue-174-deploy-critical-path.md` | Lines 86-118 rewritten to match the comment above; "above every completable outcome" sentence removed | Docs only |

Files not touched by the correction, so identical to the state verified in the prior
report: `deploy.ps1`, `deploy-production.ps1`, `offline-migration.ps1`,
`production-deploy.yml`, `host_commit_test.go`, `deploy_budget_workflow_test.go`,
`production_deploy_workflow_test.go`, `operations.md`, `deploy-lock-recovery-406.md`.

## Acceptance checks

| Check | Result | Evidence |
|---|---|---|
| No runtime, PowerShell, workflow or control-flow change in the correction | PASS | `git diff --name-only ad59fb9..58e64ad` lists only the three files above; `ast.dump` of the Python source is identical before and after; zero `*.ps1`, `*.yml`, `*.go`, `*.sh` files in the range |
| `pressured_host` uses reachable quiescence evidence | PASS | Saves `phase="migrating", quiescence="pass", originalServerNetwork={"addresses": [...], "startedAt": ...}` (`offline_migration_test.py:1146-1148`). In production `run()` calls `stop_builders` (`offline-migration.py:788`), which saves `originalServerNetwork` from `server_network()` with exactly those keys (`:423-424`, `:801`) and `quiescence="pass"` (`:449`) before `_migrate` saves `phase="migrating"` (`:456`). Both `quiescence == "pass"` gates in `_cleanup_helper` (`:536`, `:556`) are now exercised |
| Helper-present measurements match the simulation | PASS | Probe: 0 s -> 50.0, 2 s -> 116.75, 4 s -> 180.25, all `rolled-back` with both rollback scripts; matches test and docs |
| Helper-absent measurements match the simulation | PASS | Probe: 0 s -> 25.0, 5 s -> 170.0, 7 s -> 230.0, all `rolled-back` with both rollback scripts; matches test and docs |
| 160 fails while 240 succeeds on the claimed completable cases | PASS | Probe at 4 s (present) and 7 s (absent): budget 240 passes at 180.25 / 230.0; budget 160 refuses at exactly 160.0 and budget 60 at exactly 60.0, no `sh rollback-*.sh` call, phase stays `migrating`. Pinned by `test_completable_round_trip_pressure_needs_more_than_the_superseded_budget` (4 subtests for the retired budgets, elapsed equals the budget) |
| helperCleanup 90 bound refuses with no rollback | PASS | Probe at 4.5 s, helper present: `helperCleanup` failure at 90.0 of 90, `recoveryCleanup` failure at 191.0 (< 240), clock 191 <= 480, no rollback script, phase `migrating`. Pinned by `test_helper_present_pressure_is_refused_by_the_nested_helper_cleanup_budget` |
| Outer 240 bound refuses with no rollback | PASS | Probe at 8 s, helper absent: `recoveryCleanup` failure at exactly 240.0, `helperCleanup` failure at 76.0 (< 90, so the envelope decided), clock 240 <= 480, no rollback script, phase `migrating`. Pinned by `test_helper_absent_pressure_is_refused_by_the_recovery_cleanup_envelope` |
| All-10.28 stays fail-closed | PASS | Probe at 10.28 s for both shapes: `recoveryCleanup` failure at exactly 240.0, `helperCleanup` at 34.96, clock 240 <= 480, no rollback script, phase `migrating`. Pinned by `test_recovery_cleanup_stays_bounded_and_still_blocks_rollback` (2 subtests) |
| Budget arithmetic intact | PASS | Constants unchanged: cleanup 240, server 90, caddy 45, reserve 135, recovery 375, stop allowance 480 (`offline-migration.py:64-73`); `offline-migration.ps1` deadline 480, `RuntimeMaxSec` M+480, `TimeoutStopSec` 480; `deploy.ps1:960` host-recovery 510; `deploy-production.ps1:112` M+480 / 510; workflow `+ 34` minutes. The prior report's tables (structural floor 85, M + 1940 s serial ceiling, >= 100 s runner margin) hold by identity since none of those files changed. The rewritten derivation test now pins: floor 85 = [20, 20] + [5, 10, 5, 10, 5, 10]; `helperCleanup` budget 90 read from `cleanup_helper`; server-stop waits 35 = 20 + 5 + 10; 240 > 85, 240 >= 35 + 90, 240 > 160, 60 < 85 < 160 |
| Three core invariants intact | PASS | No runtime change, so the source-level evidence in the prior report is unchanged: generic timeouts stay blocking (`execute_phase` raises; `finalize` only reaches the rollback scripts after the cleanup phase returned), unowned clients are never signalled (`backend_signal` identity check `:564-575`), rollback stays exact and bounded (independent 90 s / 45 s reserves with the prior deadline restored). Every refusing probe row above ends with no rollback script and phase `migrating`; every passing row runs exactly `rollback-server.sh`, `rollback-caddy.sh` |

## Independent probe (this workstation, scratch script, not committed)

The probe instantiates the shipped `SupervisorTests.pressured_host` and calls
`finalize()`; the clock advances only for simulated bounded host work.

| Shape | cost/trip | recoveryCleanup | helperCleanup | phase | rollback scripts |
|---|---:|---:|---:|---|---|
| present | 0.0 | 50.0 pass | 25.0 pass | rolled-back | server, caddy |
| present | 2.0 | 116.75 pass | 56.5 pass | rolled-back | server, caddy |
| present | 4.0 | 180.25 pass | 88.25 pass | rolled-back | server, caddy |
| present | 4.5 | 191.0 fail | **90.0 fail** | migrating | none |
| present | 7.0 | 236.0 fail | 90.0 fail | migrating | none |
| present | 8.0 | **240.0 fail** | 76.0 fail | migrating | none |
| present | 10.28 | **240.0 fail** | 34.96 fail | migrating | none |
| absent | 0.0 | 25.0 pass | 0.0 pass | rolled-back | server, caddy |
| absent | 5.0 | 170.0 pass | 60.0 pass | rolled-back | server, caddy |
| absent | 7.0 | 230.0 pass | 84.0 pass | rolled-back | server, caddy |
| absent | 8.0 | **240.0 fail** | 76.0 fail | migrating | none |
| absent | 10.28 | **240.0 fail** | 34.96 fail | migrating | none |
| present, budget 160 | 4.0 | 160.0 fail | 68.0 fail | migrating | none |
| present, budget 60 | 4.0 | 60.0 fail | (not reached) | migrating | none |
| absent, budget 160 | 7.0 | 160.0 fail | 14.0 fail | migrating | none |
| absent, budget 60 | 7.0 | 60.0 fail | (not reached) | migrating | none |

Every number the corrected comment, docs and tests state appears in this table.
The prior report's F1 table (realistic rows) is reproduced exactly.

## Resolution of the prior HOLD findings

- **F1 (derivation omitted helper-cleanup half)**: resolved. The `85 + 15 x 10.28`
  derivation, the "at least 15 round trips" sentence, the "above every completable
  outcome" sentence and the "5-second cancel and 10-second terminate windows refuse
  on their own" cause are all gone from comment, docs and test. `helperCleanup` 90 is
  now named as the tighter nested limit with the helper present, the 240 envelope as
  the decider with the helper gone, and 10.28 is stated as refused at 240 for both
  shapes. 240 is described as a bounded envelope, not a derived minimum.
- **F2 (simulation from an unreachable state)**: resolved. `pressured_host` now
  starts from the evidence `run()` actually produces before `migrating`, so the late
  original-server-backend window and the final unowned-client check inside
  `_cleanup_helper` are on the measured path, and the helper-absent shape is frozen
  alongside the helper-present one.
- **F3, F4 (Low)**: unchanged, informational, not merge-blocking, as before.

## Informational note (no finding)

The comment says that with the helper present `helperCleanup`'s 90 s budget "refuses
first (from about 4.5 seconds per round trip)". That is true from 4.5 s up to about
7 s; at 8 s or more per round trip the 240 envelope refuses first even with the helper
present (row: present, 8.0, recoveryCleanup 240.0, helperCleanup 76.0). The docs'
framing "refused by whichever nested limit is tighter" and the explicit 10.28 sentence
("neither shape completes and the envelope refuses at 240") already cover that region,
and the outcome is fail-closed in every case, so no text change is required.

## Test evidence (run at `58e64ad` on this workstation through CSX observed commands)

| Command | Result |
|---|---|
| `python -m pytest deploy/lightsail/offline_migration_test.py -q` | 90 passed, 1 skipped (POSIX process groups), 44 subtests passed (was 88 / 35 at `ad59fb9`: two new tests, nine new subtests) |
| `go test ./scripts/ ./deploy/lightsail/ -run 'TestMigrationBudget\|TestProductionCriticalPath\|TestHostCommit\|Migration\|Recovery\|Rollback\|Budget' -count=1` | ok (scripts), ok (deploy/lightsail) |
| Scratch probe of `pressured_host` for both shapes and the retired budgets | table above |

## Recovery/rollback safety summary

Unchanged from the prior report, now with documentation that matches the measured
behaviour. Host: the cleanup phase has a 240 s envelope, `helperCleanup` a nested
90 s budget; on any timeout or proof failure the finalizer raises before
`rolling-back`, the lock stays held and no restoration is attempted. On proof (pass or
degraded-by-foreign-client) the exact previous server and proxy are restored from
independent 90 s / 45 s reserves inside the 480 s stop allowance. Controller: every
wrapper is at least the host allowance it observes, and `rolled-back-degraded` fails
closed with the lock retained. The residual tail (sustained 8 s or more per round trip
refuses and leaves the site on a stopped server until an operator reconciles) is now
stated accurately in `issue-174` instead of being described as covered.
