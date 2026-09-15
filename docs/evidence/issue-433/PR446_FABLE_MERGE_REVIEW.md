# PR #446 — independent merge review (issue #433 P0)

Frozen 2026-09-16. Reviewed head `7a9d1e1bc2e4422348e1531dfc3460351adb223e`
(`fix(deploy): restore server after foreign-client refusal`), merge base
`9116765a834bca962418bcb19e2913f274778c47`. Read against the frozen Opus audit
`c30e6c2` (`docs/evidence/issue-433/DEPLOY_SAFETY_AUDIT.md`) in an isolated
Orca worktree. No edits to the PR, no push, no comment, no merge, no deploy.

Unqualified line references are to `deploy/lightsail/offline-migration.py` at
the reviewed head.

## Verdict: PASS

The PR removes the outage class the audit named P0-1 without widening any
termination authority, and every blocking hazard the audit said must stay
blocking still blocks. Residuals are listed in section 3; none of them makes
the change less safe than `origin/main`, and one of them (P0-2, the recovery
budget) is a pre-existing defect that this PR leaves for a follow-up rather
than introduces.

## 1. Evidence run here

| check | result |
|---|---|
| `python -B deploy/lightsail/offline_migration_test.py` (via csx observed command) | 77 tests, 1 skipped, PASS |
| `go test ./deploy/lightsail -run 'TestOfflineMigrationRecovery\|TestHostCommitEvidenceAndLostControllerResponse\|TestUnknownRemoteOutcomeAndFailedRecoveryKeepTheTransactionLocked' -count=1` (via csx observed command) | PASS |
| PR CI at head | `Test` in progress at review time; `Windows` skipped (pull_request skips it by design) |

Diff scope: `offline-migration.py` (+84/-20), `offline_migration_test.py`
(+169), `offline-migration.ps1` (1 line), `host_commit_test.go` (+10).
`deploy.ps1`, `backend_signal`, `owned_dsn`, `rollback-server.sh` are untouched.

## 2. Checklist

### 2.1 Unrelated DB clients are never signalled — HOLDS

- `backend_signal` (`:522-534`) is byte-identical: one statement, conjunction
  of `pid`, `backend_start`, `application_name`, `datname=current_database()`,
  `usename='csx'`, `backend_type='client backend'`. Non-server rows still
  require `applicationName == self.application`; server rows still require
  membership in `rollbackServerBackends`.
- `rollbackServerBackends` is still populated only by
  `remember_server_backends` (`:793-823`) from an inspected container's
  addresses plus `backend_start >= StartedAt`. The dedupe change replaces the
  stored row in place by `(pid, backendStart)`, so the row handed to
  `backend_signal` is the object just stored and the membership check still
  passes; it does not admit any new row shape.
- The new block in `_cleanup_helper` (`:491-507`) reuses exactly that path
  against `originalServerNetwork`, gated on `quiescence == "pass"` (so it never
  runs when the server was never stopped) and skipped when the recorded
  address list is empty.
- The advisory branch (`:511-518`) only records and returns; it issues no
  signal. Pinned by `test_unowned_client_does_not_suppress_recovery_rollback`
  (asserts no `_backend` SQL mentions `pid=42`) and by the strengthened
  `test_surviving_unowned_client_cannot_pass_empty_owned_cleanup`.
- Who could occupy the released original-server IP: a repo grep finds only two
  TCP clients of `db` as `csx` — `csx-server` and the owned helper. Every
  operational script (`backup.sh`, `adoption-check.sh`, the evidence
  collectors) uses `docker compose exec -T db psql|pg_dump`, which connects
  over the unix socket inside the `db` container and therefore has
  `client_addr IS NULL`, unmatched by `client_addr=ANY(...)`. A helper backend
  on the recycled IP is either matched by application name and removed at
  `:466-481` (its survival raises), or is a zombie of a container already
  proved removed at `:484-485`. Under either reading of the audit's open
  `172.18.0.2` question the backend terminated by the new block belongs to this
  deploy.

### 2.2 Forward activation remains strict — HOLDS

`run()` (`:741-747`) calls `cleanup_helper()` with the default
`strict_unowned=True`; a remaining unclassified client still raises
`"unowned database clients remain after helper cleanup"` before
`migrationVerification` and `activate`. `stop_builders` `:401` is unchanged
and still refuses any client after the server stop. Pinned by
`test_surviving_unowned_client_cannot_pass_empty_owned_cleanup`.

### 2.3 Recovery restores the previous server only after owned hazards are proved absent — HOLDS

Order inside `_cleanup_helper` when called from `finalize` with
`strict_unowned=False`:

1. owned helper container stopped (`:457-463`), owned backends cancelled then
   terminated, survival raises (`:466-481`) — blocking;
2. helper removed and its absence re-proved, survival raises (`:482-485`) — blocking;
3. original-server-network backends cancelled then terminated, survival raises
   (`:491-507`) — blocking;
4. `pg_stat_progress_create_index` must be zero (`:509-510`) — blocking;
5. only then the unclassified-client observation becomes advisory
   (`:511-518`).

Every exception from steps 1–4 propagates out of
`execute_phase("recoveryCleanup", 60, ...)` at `:837` and rollback is never
reached, exactly as before. Existing tests
`test_foreign_helper_is_never_stopped_or_removed`,
`test_surviving_backend_blocks_rollback` and
`test_surviving_index_ddl_blocks_rollback` are unchanged and pass.

### 2.4 Degraded state retains the controller lock — HOLDS

- Host writes `cleanup="degraded"` (`:517`), `conclusion="failure"` (`:838`)
  and `phase="rolled-back-degraded"` (`:849-850`).
- `offline-migration.ps1:40-41` now accepts the phase as an owned outcome so
  `Resolve-CSXOfflineMigrationOutcome` returns it instead of throwing; either
  path retains the lock, this one just makes the evidence readable.
- `deploy.ps1:971` is unchanged and requires `phase -eq "rolled-back" -and
  cleanup -eq "pass"`; the degraded phase therefore throws
  `"host cleanup and exact rollback were not proved"`, sets
  `retainDeployLock`, records `rollback=unknown-host-outcome` and
  `failureClass=controller-unresolved`, and runs no controller rollback.
  `Set-CSXHostDeploymentEvidence` still requires `committed`, so degraded can
  never be read as activation. The Go harness pins the resolver half; the
  controller half is proved by the unchanged source, not by an executed test
  (see 3.3).

### 2.5 Generic timeouts stay blocking — HOLDS

`finalize` has no `try/except` around `recoveryCleanup`; a
`"host operation deadline exceeded"` from any command, including one raised
inside the advisory tail, skips rollback. Pinned by the new
`test_recovery_cleanup_timeout_remains_blocking_without_proof`. This is the
conservative choice the task asked for; its cost is recorded as residual 3.1.

### 2.6 Zero-grace loop fix is genuinely bounded — HOLDS

All four terminate-wait loops (`:394`, `:475`, `:501`, `:784`) now compute
`expired` before the poll and raise only when a poll that began after the
deadline still finds the backend. Each poll is itself clamped by
`command()` to the enclosing phase deadline (`:248-251`), so the loop cannot
outlive the phase; at most one additional observation happens after expiry.
Traced `test_terminate_grace_observes_again_after_one_slow_poll`: deadline 16,
first post-terminate poll advances the clock to 17 and returns the row, the
second poll (expired=True) returns empty and the loop exits with exactly two
post-terminate observations. On the pre-PR loop the same trace raises. The
cancel-grace loops (`while rows and now < deadline`) are untouched and never
raise.

### 2.7 Evidence contains no raw query or secrets — HOLDS

New evidence keys: `unownedClientsAtCleanup` (rows from `clients()`, which
selects `md5(query)` and never `query`), `migrationLedgerAfter`
(`version`/`count` only), and the reshaped `rollbackServerBackends` (same
columns as before). Exception strings remain fixed literals; `main()` still
emits only `str(exc)` for `RuntimeError`. Pinned by
`assertNotIn("query", ...)` in the strict-path test. `applicationName` is
client-controlled text, but that exposure pre-dates this PR in
`backends`/`lastBackendObservation` and is the same posture.

### 2.8 Tests cover the exact regressions — HOLDS, with gaps noted in 3.3

| regression | test |
|---|---|
| foreign client (unix-socket `pg_dump`) no longer suppresses recovery rollback; recorded, never signalled; phase/cleanup degraded | `test_unowned_client_does_not_suppress_recovery_rollback` |
| late original-server backend with empty `application_name` from the recorded address is cancelled then terminated in helper cleanup | `test_helper_cleanup_terminates_late_original_server_backend` |
| forward deploy still refuses, still records, still does not signal | `test_surviving_unowned_client_cannot_pass_empty_owned_cleanup` |
| one slow poll after terminate does not become "survived termination" | `test_terminate_grace_observes_again_after_one_slow_poll` |
| generic recovery timeout still blocks rollback | `test_recovery_cleanup_timeout_remains_blocking_without_proof` |
| finalizer idempotent on the new terminal phase | `test_finalizer_is_idempotent_after_commit_or_rollback` |
| post-migration ledger persisted before cleanup refusal | `test_post_migration_ledger_is_recorded_before_cleanup_refusal` |
| `rollbackServerBackends` dedupes by identity | `test_rollback_server_backends_dedupe_by_backend_identity` |
| resolver accepts the degraded phase as owned terminal evidence | `host_commit_test.go:138-147` |

## 3. Residuals (non-blocking, recommended follow-ups)

### 3.1 Audit P0-2 (recovery budget) is not addressed and grows slightly

`recoveryCleanup` remains 60 s and still clamps the nested 90 s
`helperCleanup` to it (`:229`). The recovery path now issues at least 15 psql
round trips before rollback (audit counted 13; the new server-network block
adds at least 2 more polls plus one per signal). At the measured 10.28 s per
query under 78–81 % steal that is roughly 150 s against 60 s, and by 2.5 a
timeout still skips rollback. So on the host pressure that produced run
`35019545402`, a deploy that fails after the server stop can still leave the
site down with the lock retained. The PR body states this behaviour; the
task's checklist requires it. It should be tracked as the next #433 item
(budget, or a bounded-attempt rollback after the hazard proofs have been
persisted). The forward `helperCleanup` phase carries the same growth (about
10 round trips in 90 s) and is now marginal under the same pressure.

### 3.2 Lock-recovery documentation does not enumerate the new class

`docs/deploy-lock-recovery-406.md` and `DEPLOY_RECOVERY.md` do not mention
`rolled-back-degraded`. An operator who sees it has the site up and the lock
held, with `unownedClientsAtCleanup` as the reconciliation input, and no
written procedure. One paragraph in the recovery doc would close it.

### 3.3 Test gaps relative to the audit's list

- No slow-poll test for the quiescence loop in `stop_builders` (`:394`), the
  site behind the 36.9 s / 44.8 s quiescence failures; the reshaping is
  identical to the tested site, so this is coverage, not correctness.
- No slow-poll plus true-survivor test (audit item 11); the fail-closed path
  is covered only under the fast fake clock by
  `test_surviving_backend_blocks_rollback`.
- The controller's lock retention on `rolled-back-degraded` (`deploy.ps1:971`)
  is proved by the unchanged source and by `failclosed_test.go`'s source-shape
  assertions, not by an executed PowerShell test with the degraded evidence.

### 3.4 Audit R4 (stable `application_name` for the server DSN) is deliberately out of scope

The fix is correct without it (the previous server never carries it), which
the audit required. It remains worth shipping separately so
`unownedClientsAtCleanup` becomes human-classifiable.

## 4. What must not change when this merges

`backend_signal`'s conjunction, the `rollbackServerBackends` precondition,
`owned_dsn` validation, the `deploy.ps1:971` predicate, and the absence of any
controller-side rollback. This PR changes none of them.
