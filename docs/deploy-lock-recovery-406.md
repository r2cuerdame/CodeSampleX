# Pre-Activation Retained Deploy Lock Recovery (#406)

## Canonical retained-lock recovery

`Production lock recovery` (`.github/workflows/production-lock-recovery.yml`) is a separate, manually dispatched workflow for recovering authenticated retained deploy locks.

Unlike `Production reconciliation` (which handles committed deployments where migrations ran and the target container was activated on the host), this recovery workflow is strictly conservative. It accepts the original pre-activation failure class where:
1. Target never became deployed or served (`targetSha != previousProductionSha`).
2. Live container, served revision, image digest, and health remain identical to previous production evidence (`dea13af9c0d5dd4ed1ef06b55f46f123be1c0069`, `sha256:d9960286827f50a3892a755ae0b23eca5153a60087c966a28f4e15bccbd5ce34`).
3. `offlineMigration` is null (no migration was ever attempted or committed).
4. `serverStartedAt` is empty (the new container was never started).
5. Retained evidence has `conclusion=failure`, `failureClass=rollback-critical`, `rollback=unverified`.

As extended for #433, it also accepts one exact pre-migration class: authenticated top-level and host evidence must show `controller-unresolved`, `rollback-failed`, failed quiescence, no migration or activation phase, successful helper/server-backend cleanup, and only `rollback-server.sh` failing. Recovery additionally requires the retained owner to equal the host artifact owner and a current read-only migration ledger to equal the recorded preflight ledger. The exact #433 evidence and dispatch are recorded in `docs/evidence/issue-433/DEPLOY_RECOVERY.md`.

### Manual Dispatch

Once CI on canonical main is green:

```sh
gh workflow run production-deploy-lock-recovery.yml --ref main \
  -f source_run_id=34831402445 -f source_run_attempt=1 \
  -f source_artifact_id=10342780896 --repo r2cuerdame/CodeSampleX
```

### Host terminal phase: which retained lock is this?

A retained lock after a host-owned offline migration carries the host's own
terminal `phase` in the run artifact, and the classes are not interchangeable.
`rolled-back-degraded` in particular means the exact previous server and proxy
**were** restored and the site is normally serving the known-good revision,
while a database client outside this deployment's ownership outlived cleanup.
The lock is retained on purpose: the deployment is not a proved-clean state.
`unownedClientsAtCleanup` in the same artifact is the reconciliation input - it
carries PID, `backend_start`, application name, user, client address and a
privacy-safe query hash, never query text. Identify that client, confirm the
restored revision from `/version`, then release through the dispatch below
rather than by hand. The full phase table and the reasoning behind the
controller predicate are in
[operations.md](operations.md#reading-a-rolled-back-degraded-host-outcome).

### Verification and Safety Invariants

The recovery workflow enforces strict fail-closed criteria:
- **Failed Run Authentication**: Authenticates that run `34831402445` was a canonical main `workflow_dispatch` of `.github/workflows/production-deploy.yml` that passed eligibility and failed at `Deploy and verify`.
- **Exact Artifact Provenance**: Authenticates immutable artifact `10342780896` (`production-evidence-34831402445`), verifies its ZIP SHA256 digest, and proves that it contains **only** `production-deploy-evidence.json` with null migration, empty `serverStartedAt`, and matching previous production identity.
- **Canonical Operational CI**: Verifies that operational `GITHUB_SHA` is on canonical `main` and has a successful run of `.github/workflows/ci.yml`. Verifies that target and operational SHAs are ancestors of main.
- **Live State Verification**: Probes live public health (`https://codesamplex.dev/healthz`), inspects the live container and image (`codesamplex-server-1`), and validates server loopback and Caddy TLS proxy routes, verifying that no mutation occurred and that served revision equals `previousProductionSha`.
- **No Supervisor or Mutation Process**: Under host command flock (`flock -w 5 ~/.csx-deploy-command.lock`), verifies that `csx-migration-<owner>.service` is inactive/dead with zero PIDs, no active `csx-migration-*.service` exists, no helper containers or migration DDL are present in PostgreSQL, and no deploy mutation processes (`deploy.ps1`, `offline-migration`, etc.) are running.
- **Lock Directory & Owner File Integrity**: Verifies `/opt/codesamplex/.deploy-lock` is a real directory with non-world-writable permissions, containing exactly one entry named `owner`. Verifies `owner` is a non-symlink regular file (`st_nlink == 1`, size <= 1024) containing 32 lowercase hex characters.
- **Atomic Archival and Release**: Under the host command flock, writes an exclusive recovery receipt `recovery.json` inside the lock directory, fsyncs the receipt and directory, and atomically renames `/opt/codesamplex/.deploy-lock` to `/opt/codesamplex/.deploy-lock.recovered-<source-run>-<owner>`. Fsyncs `/opt/codesamplex`. Verifies `.deploy-lock` is gone and the archive directory exists.
