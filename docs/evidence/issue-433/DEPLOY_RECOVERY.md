# Issue 433 production deploy recovery

Evidence updated at `2026-09-15T16:48:00Z`. This lane made no production,
database, GitHub deployment, or lock mutation. GitHub Actions metadata/logs,
artifacts, the issue's recorded read-only host inspection, and bounded public
HTTP GETs were the only remote inputs.

## Decision

The retained lock from production deploy run
[`34986436864`](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34986436864)
remains retained. The first recovery implementation reached canonical `main`
at `8c0e9fd`, but its two dispatches failed closed before lock mutation: one on
a transient public 503 and one because the host predicate treated any historic
container restart as current unhealthiness. A further dispatch is safe only
after this bounded-current-health hardening reaches canonical `main` and its
canonical CI succeeds.

The fixed `Production lock recovery` workflow can release this lock without
running a migration, changing a container, or modifying application data. It
first authenticates the exact failed run and artifact, then under the existing
host command flock it must prove all of the following or refuse:

- the recorded host supervisor is terminal, no migration supervisor/helper or
  deploy mutation process is active, and no index DDL is active;
- the live migration ledger is still exactly
  `0041_anonymous_credential_adoption.sql` with 42 entries;
- the live container and image are the known-good previous production image
  `sha256:463550da296fc56b9906fedeec019e3096ce2de2f657771839773621ddc5136c`,
  configured and serving revision
  `8e822f11766b0ebb23a0b756c85f6be5e0d07247`, with stable container identity,
  a stable restart count during verification, Docker `healthy` state backed by
  the latest three consecutive passing healthchecks, loopback health/version,
  and local TLS proxy health;
- `/opt/codesamplex/.deploy-lock` is a safe real directory containing only the
  exact host-artifact owner `bf50886be1e147f4b38eafa7ae9d110b` in its
  32-lowercase-hex owner file (or its matching recovery receipt).

Only after every proof passes does the workflow fsync a receipt and atomically
rename the lock directory to its run-and-owner-scoped recovery archive.

## Exact recovery prerequisite and action

Prerequisite: merge this implementation through the normal PR path, wait for a
successful canonical `main` CI run for that operational SHA, and keep the
`codesamplex-production` environment free of any deploy/reconciliation job.
Then dispatch exactly:

```sh
gh workflow run production-lock-recovery.yml --ref main \
  -f source_run_id=34986436864 -f source_run_attempt=1 \
  -f source_artifact_id=10404296751 --repo r2cuerdame/CodeSampleX
```

Do not manually delete the lock, run SQL, restart containers, or retry a
production deploy before that recovery workflow returns a successful retained
artifact. A refusal means the production state did not satisfy the proof and
the lock must remain retained for investigation.

## Recovery attempts after the first fix

| Recovery run | Result |
|---|---|
| [`34994950690`](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34994950690) | Failed before SSH with `HTTP Error 503: Service Unavailable` from the single public `/healthz` precheck. The new precheck is bounded to six observations and requires three consecutive `200`/`ok` responses, so an isolated flap is tolerated while persistent failure still refuses. |
| [`34995395995`](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34995395995) | Passed source and artifact gates, reached the fresh streamed host verifier, and refused `container-not-healthy`. The exact predicate combined `Running == true`, `OOMKilled == false`, and `RestartCount == 0`; the issue's subsequent read-only host inspection recorded the exact rollback container running, Docker `healthy`, its latest five healthchecks passing, start time `2026-09-15T15:18:41Z`, and historical restart count 8. The equality on the cumulative restart counter caused this refusal; it was not evidence of a current health flap. |

The hardening keeps the restart count as explicit recovery evidence and requires
it, the container ID, and `StartedAt` to remain unchanged across the loopback and
proxy checks. It allows at most six Docker observations, five seconds apart, to
find the current `healthy` state plus three consecutive passing health-log
entries. Wrong image/revision, non-running or OOM state, persistent insufficient
health evidence, owner mismatch, ledger drift, active helper, or active
deploy/supervisor evidence still refuses before archival.

## Run and artifact evidence

| Evidence | Result |
|---|---|
| Run 34981234895, artifact 10402415065 | Artifact ZIP digest `sha256:a54f5e76e0d93bb60b2e94ab7f53e1c83e12006cb12406704fe6b4a0de423f6e`. Quiescence failed after 36.885 s. One exact server-network backend (`172.18.0.2/32`, empty application name, PID 400704, backend start `14:21:33Z`, query start `14:24:19Z`, privacy-safe query hash `30ae449e1a2205810c65bad8f56080de`) was captured and terminated during recovery. Server and Caddy rollback passed; health was `ok`; served revision remained `8e822f1` and the schema ledger remained 0041/42. |
| Run 34986436864, artifact 10404296751 | Artifact ZIP digest `sha256:22e8c2ca86f8021fd43f2e15799a03a1c30144f1ac1c3aa4c9800f4bceed4677`. Quiescence failed after 44.794 s before any migration phase. Helper and server-backend cleanup passed; Caddy rollback passed; `rollback-server.sh` failed after 65.524 s; the host terminal state was `rollback-failed`, so the controller correctly recorded `unknown-host-outcome` and retained the lock. |

Both runs targeted operational/payload SHA
`8e4f4fc4c660d3176658faf3a1d0f058d091974f`; both started with previous SHA
`8e822f11766b0ebb23a0b756c85f6be5e0d07247`. The retry host record contains no
`migrationStartedAt`, `migrationCompletedAt`, `migrationLedger`, migration phase,
server activation, or served target revision. Its preflight ledger is 0041/42,
`backends=[]`, cleanup is `pass`, and its sole rollback failure is
`rollback-server.sh`. These facts prove that this is a pre-migration recovery
class, not a committed-migration reconciliation case.

## Root cause in the deployment scripts

1. `offline-migration.py` stopped `codesamplex-server-1`, then passively waited
   only 30 seconds for every PostgreSQL client to disappear. Under the measured
   production CPU/I/O and pool pressure, a query owned by the stopped server
   outlived that window. The script already had safe network-address,
   container-start-time, PID, and backend-start-time ownership checks plus
   cancel/terminate escalation, but invoked them only after quiescence had
   failed as part of rollback cleanup.
2. That avoidable failure forced restoration of the previous server. The
   host-owned rollback script allowed only 45 seconds for the restored server's
   reserved `/healthz` endpoint. The retry's 65.524-second rollback-server phase
   is consistent with container/config restoration followed by exhaustion of
   that exact health window; the script then classified the entire rollback as
   failed even though later public evidence shows the previous revision serving.
   The retained artifact intentionally suppresses subprocess stderr, so the
   specific final shell assertion is an inference from the phase duration and
   script bounds, not a quoted host error.
3. The controller behaved correctly after the lost proof: it did not race a
   second rollback, recorded `controller-unresolved`, and retained the lock.
   The repository recovery workflow then had a coverage gap: its provenance
   validator rejected every artifact containing host migration evidence, even
   when that evidence proved migration never began.

The initial narrow fix reuses the existing exact server-backend ownership/termination
mechanism during quiescence, while continuing to fail closed on any foreign DB
client. It raises only the restored-server health window from 45 to 60 seconds,
still inside the existing 90-second rollback phase. Finally, it adds one exact
recovery class for a terminal pre-migration/rollback-server-proof failure and
requires a current read-only ledger equality check before atomic lock archival.
The follow-up changes only the health sampling gates: historic restarts are
diagnostic rather than automatically unhealthy, while current Docker and public
health now require bounded consecutive evidence.

## Live P0 baseline and post-change acceptance plan

One request per route between the P0 dispatch update at
`2026-09-15T15:28:29Z` and the evidence freeze returned:

| Route | HTTP | TTFB | Total |
|---|---:|---:|---:|
| `/healthz` | 200 | 1.707 s | 1.707 s |
| `/version` | 200 | 1.003 s | 1.003 s |
| `/dependencies` | 200 | 1.247 s | 1.415 s |
| `/samples` | 200 | 4.615 s | 4.742 s |
| `/compatibility` | 200 | 3.116 s | 3.440 s |
| `/golang/github.com/jackc/pgx/v5` | 503 | 0.929 s | 1.072 s |

`/version` returned v0.1.189 at exact revision `8e822f1`. This confirms the P0:
the released v0.1.191 package-page admission/backoff repair is not live.

After lock recovery, repeat `/healthz` and `/version` once; both must still show
healthy v0.1.189 because recovery is not a deployment. After a separately
authorized release/deploy of the merged fix, require exact target SHA from
`/version`, then run three bounded sequential GETs each for `/healthz`,
`/dependencies`, `/samples`, `/compatibility`, and the pgx page. Require no 5xx,
no container restart/OOM evidence, and a successful authenticated Playwright
smoke of the admin data pages. The normal independent post-deploy observation
must then validate pool pressure, latency, and builder convergence; this lane
does not weaken those gates.

## Local verification

All tests are local fakes/contract checks; none connect to production:

```text
python deploy/lightsail/offline_migration_test.py
  62 tests, 1 skipped, PASS
python deploy/lightsail/recover_deploy_lock_test.py
  39 tests, PASS
go test ./deploy/lightsail \
  -run 'TestOfflineMigrationRecovery|TestPreactivationDeployLockRecovery' -count=1
  PASS
go test ./scripts -count=1
  PASS (8.998 s)
go test ./scripts ./deploy/lightsail
  scripts PASS (8.387 s); overall FAIL only in the pre-existing Windows
  failclosed fixture because flock is unavailable and Windows timeout.exe
  rejects the POSIX timeout arguments
```

All listed test commands were run through the CSX observed-command wrapper. The full
Go failure is the same platform limitation already recorded in
`OPS_BASELINE.md`; canonical Linux main CI is the required merge prerequisite.
