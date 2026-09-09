# Canonical offline production migration (#174)

This operation runs inside the existing production-deploy.yml, covered by its
GitHub concurrency group, host deploy lock, predeploy snapshots and exact
rollback. Do not create a separate production workflow, run the prestage
fallback, or run the old builder between successful backfill and target serve.

## Two immutable identities

Dispatch the existing workflow from main. Its immutable github.sha is the
**operational SHA**: reviewed deploy scripts and supervisor code. Eligibility
requires successful canonical main CI for that SHA independently of the released
**payload SHA** in commit_sha. The payload still requires main ancestry,
same-target CI, Release/Farm success, and its GitHub tracking issue.

The deploy job checks out these identities into separate operations and payload
directories. deployment-source.ps1 rejects a different HEAD, non-root source
path, or dirty/untracked files in either checkout before remote work. No
caller-supplied operations ref exists. Docker build context, image labels,
release version, Compose/Caddy files, dist and migration identity come from the
clean payload. Recovery code comes from the clean operations checkout.
Artifacts record both SHAs; observers continue to resolve the payload SHA.

The first reviewed offline contract supports 0036_builder_projections.sql,
ledger count 37 and its four exact builder indexes. Later schemas require a
reviewed update to this contract. Unsupported schemas fail before migration;
they must not silently skip the safety boundary. Ordinary direct deploy.ps1
use retains its existing path; the production wrapper always requires this
supervised path and its final acceptance callback.

## Host ownership and bounds

The existing controller snapshots and stages the payload, then submits a
transient csx-migration-<32-hex-owner>.service on the host. The service uses
systemd Type=exec, RuntimeMaxSec=migration budget + 1080 and ExecStopPost
recovery. A separate TimeoutStopSec=480 covers up to 90 seconds of database
cleanup plus two 180-second artifact rollback budgets. Command timeouts kill
the entire process group, including shell descendants.

The optional dispatch offline_migration_timeout_seconds is an integer in
60 to 1800 seconds (default 1200). It bounds offline migrate, not the existing
short serve-health gates. Choose it from recorded database scale, resource
settings and capacity measurements. A fast DevHotel fixture is not a production
duration guarantee when CPU burst capacity is exhausted. PostgreSQL background
pool settings can reset statement timeouts; the host deadline and explicit
backend cleanup are authoritative.

Before migration, the supervisor proves old/target image identity, rejects other
possible builder/helper containers and unresolved invalid/not-ready indexes or
index DDL, stops the old server, and requires all database clients to drain.
Only then does it capture source-count invariants.

The helper runs csx-server migrate from the same target image. Its canonical
Compose DSN is read only in memory; an existing URI application_name is replaced
with the unique deploy owner. The override travels through the Docker client's
process environment and is checked in the helper environment. Neither the DSN
nor an environment dump enters argv, files, journal or artifacts. Unsupported
keyword DSNs fail closed instead of falling back to PGAPPNAME. Evidence records
observed application/PID/backend-start/query-hash identities, migration duration,
exact valid/ready index definitions, ledger, projection readiness/repair marker
and before/after source totals.
After every quiet migration, including a completed-backfill retry, it re-arms
builderRepairRequired=true on exactly the current stats_daily row and verifies
that barrier before activation. A restored old builder may have erased an older
marker; zero stale source hashes alone do not prove the required full repair.

Docker stop is not database cleanup. Recovery cancels exact application,
database user, PID and backend-start tuples, escalates to bounded termination,
and proves no surviving owned helper/backend/index DDL. A failed candidate
server is also stopped; its sessions are identified by the verified container
image, network address and lifetime, then by exact backend tuples. Unknown
ownership or remaining clients blocks rollback and retains the lock.

## Acceptance and rollback

All server/stack/Caddy activation and the complete privacy-safe log smoke finish in
Host.activate before candidate-ready. The later controller performs only read
smokes/final collection and an owner-scoped ACK. A controller returning after
lease expiry cannot recreate the failed candidate over the host rollback.

candidate-ready means only that offline migration and target startup passed.
It does not mean deployment success. The host keeps a 600-second acknowledgement
lease while the controller runs the existing web, privacy, identity, invariant
and final production-evidence checks. The outer production collector now runs
as a captured final-acceptance callback before ACK. A failed check or lost
controller therefore enters host recovery. No health/identity gate runs after
ACK; only result serialization remains.

Only an ACK matching owner, operations SHA, target SHA and image digest can
produce committed/controllerSmoke=acknowledged. Independent post-deploy builder
convergence and real user-flow evidence remain separate acceptance requirements
and must not be reported as completed by migration alone.

After failure, ExecStopPost first proves database cleanup, restores the previous
dist generation, then restores server config/env/image and Caddy. Dist is
restored before recreating the old server because bind mounts pin the old
directory generation. A previously stopped server is recreated with compose up
--no-start --no-deps; it never briefly executes an old builder. No schema/data
reversal or invalid-index DROP is hidden in rollback.

The controller releases its exact lock only after a terminal unit and verified
commit or rollback. On SSH loss the host still finishes or rolls back; the lock
and owner-scoped state are retained for inspection. Do not delete or adopt them
until the canonical owner checks terminal service state, exact artifact recovery
and zero residual helper/backend/DDL. Failed cleanup/rollback blocks deployment
and must never trigger a concurrent retry.

## Verification and release evidence

Run go test ./deploy/lightsail -count=1 before review. It invokes the Python
behavioral tests and real clean-Git/PowerShell source-identity checks. Direct
invocation is python -B deploy/lightsail/offline_migration_test.py -v. Linux CI
additionally runs a real subprocess-descendant timeout test. Parse all PowerShell
files and validate the existing workflow with actionlint.

Before production dispatch, record DevHotel room/session, exact operational and
payload/build identities, resource configuration, commands and PASS/FAIL
evidence. Managed acceptance must cover successful migration/activation/final
ACK, explicit DSN application-name precedence, helper timeout with surviving
PostgreSQL work, candidate failure, and controller disconnect before and after
candidate-ready. Verify dist/config/env/image/Caddy restoration and zero
residual helper/backend/DDL, plus restoration of an originally stopped container
without starting its builder. Web acceptance uses the DevHotel web room and
Playwright for core screens/flows, console/network failures and responsive
viewports. Sleep the room when verification ends.

production-deploy-evidence.json records operationalSha, targetSha and nested
offlineMigration. Its companion production-deploy-evidence.json.migration.json
and the host /opt/codesamplex/deploy/.migration-<owner>/evidence.json preserve
lease phase and recovery facts. Record the canonical run URL plus concise Korean
task/evidence summary in GitHub #174. Do not close the issue until live /version,
/healthz, /ko/, /samples, real pgx AppendRows, OTel alpine-musl and DB-pressure
evidence prove recovery.
