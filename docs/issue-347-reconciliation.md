# Reconcile an already committed deployment without mutation

`production-reconcile.yml` authenticates one failed canonical deployment and
checks its retained host acceptance against the still-running exact process.
It never calls deployment, activates an image, runs migrations, rolls back,
restarts a service, adopts ownership, or removes the owner lock. It uses the
production concurrency group and pinned SSH identity.

This is deliberately an evidence path. The existing lock remains a deployment
fence even after successful reconciliation. Releasing it is a separate operation
requiring an owner-checked durable acknowledgment/receipt protocol; an absent
lock, a manual deletion, or a rewritten failed workflow is not such a receipt.
This change makes no claim to have implemented that release protocol.

## Preconditions and evidence

Run the workflow only from canonical `main` with successful CI for that exact
operational revision. Inputs are the original failed Production deploy run ID
and its exact retained 32-character owner. The original target, previous
revision, image identities, release, migration/count and activation timestamp
come from authenticated GitHub artifacts, not operator-supplied replacements.

The original run must be a completed failed `production-deploy.yml` dispatch
on this repository's main branch, with a successful eligibility job for its
attempt. Its unique unexpired artifact must contain both the controller JSON
and the raw host JSON. The helper checks the artifact digest, safe archive
members, controller-unresolved outcome, host commit, all acceptance fields and
exact timestamp. The first supported reconciliation is ledger 0037/count 38;
other histories fail closed for separate review.

The read-only host collector executes current reviewed source streamed in
memory, never retained executable code. It verifies:

- Exact owner lock, original configuration, raw evidence SHA-256, accepted
  target/previous/image/release/ledger and unchanged activation timestamp.
- Terminal supervisor, no running finalizer or cgroup members, before and after.
- Live container/configured/image/served identity, zero restarts, no OOM or pause.
- Read-only PostgreSQL ledger/index definitions and validity, no retained helper,
  owned backend or index DDL left over from migration.
- Fresh loopback/proxy health and smoke, unchanged live identity, and unchanged
  retained file bytes and metadata after all probes.

Retained lock/state/files reject shared or writable ownership. The existing
`deploy/` parent may use its canonical `0775` mode only when NSS proves its
owner's primary group is private to that owner/root; foreign/shared groups and
world write fail closed. No host permission change is performed.

The controller also checks public HTTPS health and exact release/revision before
and after the host collector, keeping certificate validation. Every transport,
SQL query and collector has a finite deadline. Failed command output is reduced
to a fixed stage; raw application responses, environment and SSH stderr are not
published as failure text. A failed check produces failed evidence and leaves
the lock and service intact.

## Canonical observation

The workflow uploads `production-evidence-RUN_ID`, retaining the original raw
artifact and its hashes. A successful new run contains schema 3 acceptance
linked to the original failed run. The old run remains failed.

The normal post-deploy observer accepts either successful Production deploy or
successful Production reconciliation. For reconciliation it downloads and
validates the original failed artifact again, then matches the new acceptance
to that original chain. Supersession and baseline selection retain the original
deployment's run number because run numbers from different workflows cannot be
compared. The observer checks the original exact server start and never restarts
the process to create a new observation window. All existing convergence,
latency, health and incident classification checks remain in force.
Expired or deleted original artifacts intentionally prevent later acceptance;
the new record cannot substitute for missing original provenance.

For the known committed owner, once this implementation is merged and exact
main CI passes:

```sh
gh workflow run production-reconcile.yml --repo r2cuerdame/CodeSampleX --ref main \
  -f deploy_run_id=34608440406 -f owner=50e9613a28324ebca91b351f021eed0e
```

This is a bounded read-only reconciliation, not permission to retry production
deployment. Failed health blocks successful reconciliation. Successful
reconciliation is not stability acceptance or permission to release the lock.
Keep #347/#174 open until their remaining acceptance criteria are actually met.

See [the live builder control audit](issue-174-live-load-control-audit.md) for the
current no-pause finding, measured host exhaustion and the user cost/cutover
decision required for a capacity change.
