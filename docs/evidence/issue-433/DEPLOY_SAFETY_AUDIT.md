# Issue #433 — independent P0 deployment safety audit

Frozen 2026-09-16. Baseline `origin/main` = `9116765a834bca962418bcb19e2913f274778c47`
(`fix(admin): serve farm sections through bounded caches`), read in an isolated
Orca worktree.

**Scope and method.** Source-only audit of the canonical production deploy,
rollback and offline-migration path, read against the recorded facts of failed
run `35019545402`: *offline migration passed; cleanup rejected an unowned
PostgreSQL backend with empty `application_name` and client `172.18.0.2`; the
server then remained stopped.* No production access, no code edits, no
deployment, no PR. Corroborating in-repo artefacts:
`docs/evidence/issue-433/DEPLOY_RECOVERY.md` (runs `34981234895`,
`34986436864`, `34994950690`, `34995395995`, `35001530015`) and
`deploy/lightsail/rollback-server.sh:62`, which already names #433.

Every claim below is labelled **PROVED** (follows from the source at the cited
line), **MEASURED** (a number recorded in a repo artefact) or **INFERRED**
(consistent with the facts, not settled by them).

Unqualified line references are to `deploy/lightsail/offline-migration.py`.

---

## 1. Conclusion first

The unowned-backend rejection was the *trigger*. It was not what took the site
down and left it down.

The outage is caused by one structural defect:

> **In `finalize()`, the rollback that ends the outage is executed only if a
> preceding cleanup phase completes without raising. Any exception in that
> phase — including one that says nothing about whether restoring the previous
> container is safe — skips `rollback-server.sh` entirely and leaves
> `codesamplex-server-1` stopped.** (`:786-787`)

The deployment stops the production server itself (`:380`), so the supervisor
*owns* an outage from that moment. The only thing in the whole system that ends
it is `rollback-server.sh`, and the controller is explicitly forbidden from
running a second one (`deploy.ps1:982-984`, "Never race a second controller
rollback against its finalizer"). Gating that single recovery action behind a
precondition an unrelated third party can fail is what converts a failed deploy
into an unbounded outage plus a retained lock.

There are **two independent triggers** that reach the same skipped rollback, and
both were live in run `35019545402`. Fixing only the first leaves the second.

---

## 2. The failure chain, step by step (PROVED)

| # | Step | Line |
|---|---|---|
| 1 | `stop_builders()` stops `codesamplex-server-1`. The outage begins and is owned by the supervisor. | `:380` |
| 2 | Quiescence passes: zero client backends remain, `quiescence="pass"` is written durably. | `:398-401` |
| 3 | `_migrate()` runs the owned helper to completion; `phase="migrating"`, schema moves. | `:406-437` |
| 4 | `_cleanup_helper()` proves the owned helper container is gone, owned backends are gone, and `pg_stat_progress_create_index = 0`. | `:469-475` |
| 5 | Its **last** statement then rejects the run on *any* remaining client backend: `if self.evidence.get("quiescence") == "pass" and self.clients(): raise RuntimeError("unowned database clients remain after helper cleanup")`. | `:476-477` |
| 6 | `main()` records `conclusion="failure"` and exits 1. systemd `ExecStopPost` invokes `finalize`. | `:801-835`, `offline-migration.ps1:111` |
| 7 | `finalize()` runs `execute_phase("recoveryCleanup", 60, cleanup)` where `cleanup = stop_server_for_rollback(); cleanup_helper()`. | `:783-786` |
| 8 | `cleanup_helper()` re-runs the **same** check as step 5 and raises again. | `:476-477` |
| 9 | The exception propagates out of `finalize()`. `self.save(phase="rolling-back")` and the `rollback-server.sh` / `rollback-caddy.sh` loop at `:787-797` are **never reached**. | `:786-787` |
| 10 | Controller sees a non-`rolled-back` host phase, records `rollback="unknown-host-outcome"`, `failureClass="controller-unresolved"`, retains the lock, and deliberately runs no rollback of its own. | `deploy.ps1:971-983` |

**Server remained stopped. Lock retained. Next deploy blocked.** That is the
observed outcome, reproduced from source.

This is not accidental. `offline_migration_test.py:225-236`
(`test_surviving_backend_blocks_rollback`, `test_surviving_index_ddl_blocks_rollback`)
asserts `self.assertFalse(any(k == "command" ...))` — the current tests *require*
that a cleanup failure suppresses rollback. The safety argument holds for the two
cases those tests name (an owned helper backend still alive, or index DDL in
flight); it does not hold for the third case the same code path lumps in with
them.

---

## 3. Finding P0-1 — the unowned-client guard has no ownership predicate at all

`clients()` (`:293-302`):

```python
where = " AND application_name='" + self.application + "' AND usename='csx'" if owned_only else ""
... FROM pg_stat_activity WHERE datname=current_database()
    AND backend_type='client backend' AND pid<>pg_backend_pid()  + where
```

With `owned_only=False` the predicate is *every client backend in the `csx`
database except the supervisor's own psql session*. **PROVED:** no
`application_name`, no `client_addr`, no `backend_start`, no `usename`, no
`state` — the check that failed the deploy inspected none of the identity this
file goes to considerable trouble to collect everywhere else.

Two consequences:

**(a) It cannot distinguish "the server I stopped" from "a stranger."** The
supervisor already knows the stopped server's exact network identity: it records
`originalServerNetwork` (addresses + container `StartedAt`) at `:377-379`, and
`stop_server_for_rollback()` re-uses precisely that to classify and kill server
backends at `:716-746`. `_cleanup_helper` has that evidence in hand at `:476` and
consults none of it.

**(b) It is a trap the supervisor is not permitted to escape.** The guard fires
on rows that `backend_signal()` is *structurally incapable* of acting on:

| Row shape | Seen by `clients()` | Matchable by `remember_server_backends()` (`:748-772`) | Signallable by `backend_signal()` (`:480-492`) |
|---|---|---|---|
| `usename <> 'csx'` | yes | no (`AND usename='csx'`) | no (`AND usename='csx'` hardcoded) |
| `client_addr IS NULL` (unix socket) | yes | no (`client_addr=ANY(...)`) | n/a |
| address outside the recorded container set | yes | no | n/a |
| `backend_start <` container `StartedAt` | yes | no (`backend_start>=since`) | n/a |

For every one of those the loop cannot converge by any action the supervisor is
allowed to take, so the deploy fails, recovery fails, and rollback is skipped —
permanently, not after a timeout.

**Real, scheduled collisions already in this repo (PROVED).** `deploy/backup.sh:13`
runs `docker compose exec -T db pg_dump -U csx -d csx` nightly at 03:15 UTC;
`deploy/adoption-check.sh:20`, `deploy/lightsail/collect-production-evidence.sh`,
`collect-post-deploy-observation.sh` and `collect-extended-observation.sh` all run
`docker compose exec -T db psql -U csx -d csx`. Each produces a client backend in
`datname='csx'`. **A nightly backup overlapping a deploy window is sufficient, on
its own, to take production down and keep it down.**

---

## 4. Finding P0-2 — the recovery budget cannot be met on the host this happens on

Second, independent trigger for the same skipped rollback.

- **PROVED:** `execute_phase` clamps a nested budget to the enclosing one —
  `self.operation_deadline = min(previous, started + seconds)` (`:229`). So
  `cleanup_helper`'s nominal 90 s inside `recoveryCleanup`'s 60 s is **60 s
  total**, shared with `stop_server_for_rollback`.
- **PROVED:** `command()` clamps every subprocess to the remaining deadline and
  raises `"host operation deadline exceeded"` at <= 0 (`:249-251`).
- **PROVED:** the recovery path issues **>= 13** `docker compose exec ... psql`
  round trips before rollback: `stop_server_for_rollback` >= 7 (one
  `remember_server_backends` per poll plus one `backend_signal` per backend, at
  `:731-745`), `_cleanup_helper` >= 6 (`:456-476`).
- **MEASURED** (`DEPLOY_RECOVERY.md`, run `35001530015`): one such query took
  **10.28 s** on the production host while CPU steal held at **78-81 %**.

13 x 10.28 s ~= **134 s against a 60 s budget.** Under the exact pressure #433
occurs in, `recoveryCleanup` times out before rollback is reached — and a
timeout, like the unowned-client rejection, skips rollback at `:786-787`.

---

## 5. Finding P0-3 — zero-grace termination deadlines

**PROVED.** The escalation waits check the deadline *after* the poll that
consumes it:

```python
deadline = time.monotonic() + 10
while self.clients(True):                     # this call itself costs ~10.28 s
    if time.monotonic() >= deadline:          # already expired
        raise RuntimeError("owned PostgreSQL backend survived termination")
    time.sleep(0.25)
```

`:464-467`; same shape at `:393-396` (quiescence) and `:741-744` (rollback
cleanup). On the starved host the first poll alone exhausts the budget, so the
supervisor can raise "survived termination" **without ever re-polling after the
terminate** — the grace period is effectively zero. This is consistent with the
36.885 s and 44.794 s quiescence failures recorded for runs `34981234895` and
`34986436864` in `DEPLOY_RECOVERY.md`.

---

## 6. Finding S-1 — the ownership model rests on a field production never sets

**PROVED:** `deploy/docker-compose.yml:35`

```yaml
CSX_DSN: postgres://csx:${POSTGRES_PASSWORD:-csx-local-dev}@db:5432/csx?sslmode=disable
```

No `application_name`. The Go side never adds one either: `serverstore.Open` ->
`pgx.ParseConfig(dsn)` -> `pgx.ConnectConfig(cfg)` with no `RuntimeParams`
mutation (`internal/serverstore/pg.go:47`, `internal/serverstore/pool.go:653,669`;
a repo-wide grep finds `ApplicationName`/`RuntimeParams` only in tests). pgx
sends no fallback application name, so **every production csx-server backend
appears in `pg_stat_activity` with `application_name = ''`.**

This is directly corroborated by `DEPLOY_RECOVERY.md`, which records for run
`34981234895` "one exact **server-network** backend (`172.18.0.2/32`, **empty
application name**, PID 400704 ...)". The `FakeHost` harness already models it:
`offline_migration_test.py:123-127` returns `"applicationName": ""` for the
server backend.

So the empty `application_name` in run `35019545402` is **not** the signature of
a foreign intruder — it is the signature of *this stack's own Go server*, and the
guard has no way to say so. Everything else in the file works around this gap
with network identity; `clients()` does not.

**Identity of `172.18.0.2` — INFERRED, not settled.** The repo's own artefact
calls that address the **server network** address. During the cleanup window the
old server is stopped (Docker releases a stopped container's IP), and the only
container joining that network is the owned helper — whose `application_name` is
verified non-empty at `:419-420`. The two readings the source supports are
(i) the helper occupying the address freed by the stopped server, observed in a
window where its startup parameters were not yet applied, or (ii) a csx-server
backend from the original server's address that `remember_server_backends()`
could not match on `usename`/`backend_start`. The fields that would settle it are
already captured in `evidence.json`: `lastBackendObservation`, `backends`,
`rollbackServerBackends`, `originalServerNetwork.addresses` / `.startedAt`, and
the row's `backendStart` / `queryStart` / `queryHash`. **The correction below is
deliberately correct under either reading** — that is the point of making
recovery independent of classification.

---

## 7. Finding S-2 — a cleanup failure erases the record that the schema moved

**PROVED.** `migrationLedger` is written only by `verify_migration()` (`:599`),
which `run()` reaches only *after* `cleanup_helper()` (`:699-705`, cleanup at `:702`). In run
`35019545402` the migration was applied and then cleanup failed, so the durable
evidence carries `migrationLedgerBefore` (preflight) and no post-migration
ledger. An operator — or the lock-recovery workflow, whose pre-migration class
turns on exactly this question (`DEPLOY_RECOVERY.md`, "Exact recovery
prerequisite and action") — cannot read from the artefact whether the schema head
moved. It is recoverable here only because `migrationStartedAt` /
`migrationCompletedAt` and `phase="migrating"` are set, which is indirect.

## 8. Finding S-3 — `rollbackServerBackends` grows without bound

**PROVED.** `remember_backends()` dedupes on `(pid, backendStart)` (`:306-308`).
`remember_server_backends()` dedupes on whole-dict equality (`:769-771`), and the
dict includes `queryStart` and `queryHash`, which change between polls. Every
poll of a live backend therefore appends a new entry, and the whole list is
rewritten to `evidence.json` on each `save()`. On a slow host this inflates the
artefact the controller parses. Low severity; it is an inconsistency with the
sibling function, not a safety bug.

## 9. What is already correct — and must not be weakened

**PROVED, and I found no way to break it.** The guarantee that *an unrelated DB
client is never killed* currently holds:

- `backend_signal()` (`:480-492`) signals a single backend only under a
  conjunction of `pid` **and** `backend_start` **and** `application_name` **and**
  `datname=current_database()` **and** `usename='csx'` **and**
  `backend_type='client backend'`, evaluated inside one statement — so a PID
  recycled between observation and signal cannot be hit.
- The non-server path additionally requires `applicationName == self.application`
  (the owner-scoped `csx-migrate-<owner>`).
- The server path additionally requires the row to have been previously recorded
  in `rollbackServerBackends`, which `remember_server_backends()` only populates
  from `client_addr` values read out of an **inspected, image-verified container
  identity** plus `backend_start >=` that container's `StartedAt` (`:748-766`) —
  never from operator input or from the backend's own claims.
- `owned_dsn()` (`:165-176`) refuses keyword DSNs, newlines and fragments, and
  strips every inherited `application_name` before appending its own; `_migrate`
  then re-reads the container's env and refuses if the override did not apply
  (`:419-420`).

**Any fix must leave all of this exactly as it is.** Widening termination to
cover "unowned" backends would be the wrong correction: it trades a recoverable
outage for the possibility of killing a stranger's transaction, and it cannot
work anyway (section 3(b) — those rows are unsignallable by construction).

---

## 10. Recommended smallest safe correction

Five changes, ordered by how much of the P0 each removes. **R1 alone ends the
outage class.** None of them kills anything new.

### R1 — Rollback must be gated on what rollback actually needs (P0-1, P0-2)

Split the terminal check in `_cleanup_helper` into *blocking* and *advisory*, and
make the distinction explicit at the call site rather than by exception:

```python
def cleanup_helper(self, strict_unowned=True):
    return self.execute_phase("helperCleanup", 90,
                              lambda: self._cleanup_helper(strict_unowned))
```

At `:476-477`:

```python
if self.evidence.get("quiescence") == "pass":
    remaining = self.clients()
    if remaining:
        self.save(unownedClientsAtCleanup=remaining)   # same privacy shape: queryHash, never raw query
        if strict_unowned:
            raise RuntimeError("unowned database clients remain after helper cleanup")
```

- `run()` keeps `strict_unowned=True`: a deploy still refuses to **commit** with
  an unclassified client present. Fail-closed forward is preserved.
- `finalize()` passes `strict_unowned=False`: an unclassified client is
  **recorded, never killed, and never blocks restoring the previous server.**

The safety argument holds because by `:476` the supervisor has *already proved*
that the three things which make rollback unsafe are absent: the owned helper
container is gone (`:469-472`), the owned backends are gone (`:464-467`), and no
index DDL is in progress (`:474-475`). A foreign client does not make starting
the previous image unsafe — the previous image ran beside arbitrary clients every
day before this deploy began.

### R2 — Recovery must attempt rollback even when recovery cleanup fails (P0-1, P0-2)

At `:786`, distinguish a blocking cleanup failure from a non-blocking one:

```python
try:
    self.execute_phase("recoveryCleanup", 60, cleanup)
except Exception as exc:
    if blocking(exc):        # owned helper/backends alive, or index DDL active
        raise
    self.save(recoveryCleanupFailure=safe_reason(exc))   # fixed strings only
self.save(phase="rolling-back", conclusion="failure")
```

`blocking(exc)` is a closed set of the fixed messages already raised in this file
— `"owned helper survived cleanup"`, `"owned PostgreSQL backend survived
termination"` / `"server PostgreSQL backend survived termination"`, and `"index
DDL remains after helper cleanup"`. Everything else — a deadline overrun, a
transient docker/psql failure, an unclassifiable client — records and proceeds to
rollback.

Add a third terminal phase so the outcome stays honest and machine-readable:

| phase | meaning |
|---|---|
| `rolled-back` | server restored **and** cleanup fully proved (today's meaning, unchanged) |
| `rolled-back-degraded` | **server restored**, cleanup not fully proved — lock retained, human reconciles |
| `rollback-failed` | server **not** restored (today's meaning, unchanged) |

`deploy.ps1:971` currently requires `phase -eq "rolled-back" -and cleanup -eq "pass"`
to call rollback succeeded; leave that exactly as is, so `rolled-back-degraded`
still retains the lock and still reports `unknown-host-outcome`. **The site comes
back; the deploy still fails closed; nothing is auto-cleared.** The lock-recovery
workflow in `docs/deploy-lock-recovery-406.md` gains one new, explicitly
enumerated class rather than an ambiguous one.

### R3 — Remove the zero-grace deadline (P0-3)

Evaluate the deadline *before* the poll that consumes it, so a poll already in
flight when the budget expires still counts, and at least one poll always happens
strictly after the terminate:

```python
deadline = time.monotonic() + 10
while True:
    expired = time.monotonic() >= deadline
    if not self.clients(True):
        break
    if expired:
        raise RuntimeError("owned PostgreSQL backend survived termination")
    time.sleep(0.25)
```

Apply the same shape at `:393-396`, `:464-467` and `:741-744`. This does not
extend any wall-clock budget; it only stops the supervisor from concluding
"survived termination" on evidence it never actually gathered.

### R4 — Give the production server DSN a stable identity (S-1)

`deploy/docker-compose.yml:35`:

```yaml
CSX_DSN: postgres://csx:${POSTGRES_PASSWORD:-csx-local-dev}@db:5432/csx?sslmode=disable&application_name=csx-server
```

Then `clients()` can *classify* rather than merely count: `csx-server` (the
service this deploy stopped and owns), `csx-migrate-<owner>` (owned helper),
`csx-ops-<owner>` (the supervisor's own psql), `psql` / `pg_dump` (host
maintenance), `''` (genuinely unknown). It makes the evidence in
`unownedClientsAtCleanup` readable by a human at 3 a.m.

**Two caveats that must be stated in the change itself, or this is a trap:**

1. It takes effect only from the deploy *after* the one that ships it — the
   server already running when a deploy starts still has the old, anonymous DSN,
   and `rollback-server.sh:34-38` restores `docker-compose.yml.rollback-predeploy`.
   So the guard must **never require** a non-empty `application_name`; R1 and R2
   must be correct without it. They are.
2. It is an env change on the `server` service only. `_activate` recreates
   `server` (`:665`), so it does reach the process; the `db` service is untouched,
   consistent with the offline-deploy invariant that production deploys never
   recreate `db`.

### R5 — Two cheap hygiene fixes

- **S-2:** record the post-migration ledger immediately after `_migrate()`
  returns, before `cleanup_helper()`, so any later failure carries the true schema
  head in `evidence.json`. One extra `self.query` in the phase with the most
  generous budget.
- **S-3:** dedupe `remember_server_backends()` on `(pid, backendStart)`, the way
  `remember_backends()` already does (`:306-308`).

### Explicit non-goals

- **Do not** widen termination to unowned backends. Section 9 explains why the
  current narrowness is the good part, and section 3(b) shows it would not work
  regardless.
- **Do not** relax `backend_signal`'s conjunction, the `rollbackServerBackends`
  precondition, or `owned_dsn`'s DSN validation.
- **Do not** make the controller run its own rollback. `deploy.ps1:982-984` is
  right to refuse to race the finalizer; the finalizer is what must be fixed.

---

## 11. Regression tests

All of these fit the existing `FakeHost` harness in
`deploy/lightsail/offline_migration_test.py` (no Docker, no DB, no production).
`FakeHost.clients()` at `:110-114` currently ignores `owned_only` and must be
taught to honour it for the new cases.

### A. The P0 itself — rollback survives an unowned client (new)

1. `test_unowned_client_does_not_suppress_recovery_rollback`
   Arrange `helper_present=False`, `backend_present=False`,
   `evidence["quiescence"]="pass"`, and
   `clients = lambda owned_only=False: [] if owned_only else [{"pid": 42, "applicationName": "", "clientAddress": "172.18.0.2", "userName": "csx", "backendStart": ..., "queryStart": ..., "queryHash": "0"*32}]`.
   Call `finalize()`. Assert `["rollback-server.sh", "rollback-caddy.sh"]` ran in
   order, `phase == "rolled-back-degraded"`, and
   `evidence["unownedClientsAtCleanup"]` holds the row.
   *This is the exact shape of run `35019545402` and fails on today's code.*

2. `test_unowned_client_is_never_signalled`
   Same arrangement. Assert **no** `pg_cancel_backend` / `pg_terminate_backend`
   SQL mentions `pid=42`, in either `run()` or `finalize()`. Guards R1 against
   being "fixed" by killing the stranger.

3. `test_unowned_client_still_refuses_to_commit_a_deploy`
   Same arrangement, call `cleanup_helper()` directly (strict path). Assert it
   still raises `"unowned database clients"` and that `activate()` is never
   reached. This is `test_surviving_unowned_client_cannot_pass_empty_owned_cleanup`
   (`:509`) retained, now pinned to the strict path only.

4. `test_owned_backend_survival_still_blocks_rollback`
   `terminate_clears=False`. Assert `finalize()` raises and **no** `sh` command
   ran. (`test_surviving_backend_blocks_rollback`, `:225` — must keep passing
   unchanged; R1/R2 must not widen it.)

5. `test_active_index_ddl_still_blocks_rollback`
   `progress=1`. Assert no `sh` command ran.
   (`test_surviving_index_ddl_blocks_rollback`, `:232` — must keep passing.)

6. `test_foreign_helper_still_blocks_rollback`
   `foreign_helper=True`. Assert no `docker stop`/`rm` and no `sh` command.
   (`:218` — must keep passing.)

### B. Budget exhaustion must not suppress rollback (P0-2)

7. `test_recovery_cleanup_timeout_still_attempts_rollback`
   Drive `migration.time.monotonic` so the fake clock advances ~10 s per `query()`
   call (mirroring the **measured** 10.28 s at 78-81 % steal), making
   `recoveryCleanup` exceed 60 s. Assert both rollback scripts still ran and
   `phase == "rolled-back-degraded"`.

8. `test_nested_helper_cleanup_cannot_exceed_the_recovery_envelope`
   Assert the observed deadline for `helperCleanup` inside `recoveryCleanup` is
   the 60 s recovery deadline, not 90 s — pinning the `min()` clamp at `:229` as
   intended behaviour so a later "fix" does not silently extend the systemd stop
   budget past 240 s.

### C. Zero-grace deadlines (P0-3)

9. `test_terminate_grace_survives_one_slow_poll`
   Make the first `clients(True)` after terminate cost more than the whole 10 s
   budget and the second return `[]`. Assert `_cleanup_helper` completes and does
   **not** raise `"survived termination"`. Fails on today's `:464-467`.

10. `test_quiescence_grace_survives_one_slow_poll`
    Same, for `stop_builders()` at `:393-396`. This is the direct regression for
    the 36.885 s / 44.794 s quiescence failures of runs `34981234895` /
    `34986436864`.

11. `test_terminate_still_fails_closed_when_backend_truly_survives`
    Slow polls **and** `terminate_clears=False`. Assert it still raises. R3 must
    not become "wait forever".

### D. Ownership and never-kill-a-stranger (unchanged contracts, now pinned)

12. `test_backend_signal_requires_full_identity_conjunction`
    Assert every emitted signal statement contains all of `pid=`,
    `backend_start='...'::timestamptz`, `application_name='...'`,
    `datname=current_database()`, `usename='csx'`, `backend_type='client backend'`.
    (Extends `test_docker_stop_does_not_count_as_database_cleanup`, `:199`.)

13. `test_server_backend_signal_requires_a_recorded_container_identity`
    Hand `backend_signal(row, terminate=True, server=True)` a row absent from
    `rollbackServerBackends`. Assert `"unrecorded rollback server backend"`.

14. `test_unix_socket_and_foreign_role_clients_are_recorded_not_signalled`
    Two rows: `clientAddress=None, applicationName="pg_dump", userName="csx"` and
    `clientAddress="172.18.0.9", userName="postgres"`. Assert neither is
    signalled, both are recorded, and recovery rollback still runs. This is the
    `backup.sh` / `adoption-check.sh` collision from section 3, made executable.

15. `test_nightly_backup_overlapping_a_deploy_does_not_prevent_recovery`
    End-to-end via `finalize()` with a `pg_dump` row present throughout. Assert
    the server is restored.

### E. Identity and evidence (S-1, S-2, S-3)

16. `test_production_dsn_declares_a_stable_application_name`
    Parse `deploy/docker-compose.yml`, assert `server.environment.CSX_DSN` carries
    `application_name=csx-server`. (Sibling in `deploy/lightsail/*_test.go`
    alongside the existing compose-shape guards.)

17. `test_supervisor_classifies_without_requiring_an_application_name`
    Every row has `applicationName == ""`. Assert R1/R2 behaviour is unchanged —
    proving the fix does not depend on R4 having shipped, which matters because
    the *previous* server never has it.

18. `test_post_migration_ledger_is_recorded_before_helper_cleanup`
    Force `_cleanup_helper` to raise; assert `evidence["migrationLedger"]` (or an
    equivalent post-migration observation) is already present, so an operator can
    tell the schema moved.

19. `test_rollback_server_backends_dedupe_on_pid_and_backend_start`
    Poll the same backend three times with changing `queryStart`/`queryHash`.
    Assert `len(evidence["rollbackServerBackends"]) == 1`.

### F. Privacy (existing posture, must not regress)

20. `test_recorded_unowned_clients_never_contain_raw_query_text`
    Assert the new `unownedClientsAtCleanup` rows carry `queryHash` and no `query`
    key, matching `clients()` at `:293-302` and the "privacy-safe query hash"
    already published in `DEPLOY_RECOVERY.md`.

---

## 12. Residual risk after these changes

- **Accepted.** A deploy that fails with an unclassified client present still
  fails and still retains the lock. Only the outage ends automatically.
- **Accepted.** Rollback restores the previous binary over the *already applied*
  schema. That is today's design (`rollback-server.sh` restores the image, never
  the schema), it is safe for the additive index/column migrations listed in
  `REVIEWED_MIGRATIONS`, and R5 makes it visible in the evidence instead of
  implicit.
- **Not addressed here.** The underlying host pressure (78-81 % CPU steal, a
  10.28 s psql round trip) is the reason every budget in this file is marginal.
  R2 and R3 make the supervisor survive it; they do not remove it.
- **Open question for the owner, requiring run artefacts rather than source.**
  Which process held `172.18.0.2` in run `35019545402` (section 6). The
  recommendation is correct either way, but the answer determines whether an
  *additional* narrow ownership rule would be worth adding later.
