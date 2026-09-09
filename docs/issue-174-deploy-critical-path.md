# Production deployment critical path (#174)

The recovered implementation from failed job `job_01M21Y9TSST425ZY39GWE7X4V8`
is commit `3279c6b0b2a8eb353fc52150550f3020b6a77140`, already published as PR #259.
There were no tracked dirty changes; its temporary logs/tools remain untouched.
The separate failed recovery lane's 11 files were copied with matching SHA256
before/after the copy. Its existing source split and host migration supervisor
were integrated without modifying that original dirty worktree.

The canonical workflow now checks out two immutable repositories: `operations`
from the workflow's main SHA and `payload` from the eligible release SHA. Both
must be clean and match their recorded identities. Target CI, release/farm,
controller main CI, tracking issue and pinned SSH identity remain required.
This lets the repaired controller deploy existing release v0.1.151 / e6bc85b2.

Rollback covers activation/startup, exact process/image revision, migration
ledger version/count, direct health and two representative HTTP requests only.
The existing migration command applies migration 0036 and its transactional
backfill while builders are quiescent. A host-owned systemd unit holds the lease
until the controller acknowledges minimal final acceptance. Its independent
finalizer cleans the exact helper/backend identities and restores snapshots on
failure or controller loss. No prestage or ad-hoc production workflow is added.

Full-table source/hash/projection audits, privacy, broad routes, latency, 503 and
convergence checks belong to independent observation. Observation never invokes
rollback; positive privacy evidence may only request an owner decision. Observer
code uses the authenticated deployment controller SHA and payload migration
version from deployment evidence. Missing diagnostics cannot overturn deploy PASS.

Deployment PASS does not close the ongoing performance/convergence issue #174.

## Finite execution and recovery budgets

Preparation 600s, staging 300s, initial activation 240s, actual offline migration
budget + 300s lifecycle allowance, final activation/smoke 240s, host recovery
540s, cleanup 60s and two identity probes of 30s. Migration defaults to 1200s and
accepts 60–1800s. At the maximum, all failure reserves sum to 4140s (69m), below
the 70m script-step cap and 73m job cap. The larger ceiling covers actual migration
and cleanup only; every phase completes immediately when done. No fixed wait or
extended observation was added. Non-host rollback retains its independent 300s.
Evidence records controller/payload identity, migration duration, phases, cleanup
and exact rollback results. Unresolved host recovery retains the deployment lock.

## Validation

Run `go test -count=1 ./deploy/lightsail ./scripts ./internal/deploygate` with
PowerShell 7 and POSIX shell tools, actionlint on both workflows, PowerShell AST
and shell syntax checks. Host fixtures test timeout, process-tree cleanup,
exact backend ownership, ACK failure, rollback ordering and immutable source
separation. Canonical Linux CI exercises real process groups and flock.

This PR changes backend deployment orchestration only. The existing exact payload
DevHotel acceptance remains applicable; no artificial browser room is created.
Only the existing production-deploy workflow may publish after CI and review.
