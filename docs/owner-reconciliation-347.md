# Owner reconciliation and host stability (#347 / #174)

## Canonical committed-owner reconciliation

`Production reconciliation` is a separate, manually dispatched workflow. It
never runs a deployment, migration, rollback, container restart or retained host
script. The original failed `Production deploy` run stays failed. The only
accepted source is a canonical main dispatch whose exact job attempt passed
eligibility, failed at controller verification, and retained committed host
acceptance inside the original rollout time window.

Once public health has recovered, use the reviewed main workflow:

```sh
gh workflow run production-reconciliation.yml --ref main \
  -f source_run_id=34608440406 -f source_run_attempt=1 \
  -f source_artifact_id=10267836798 --repo r2cuerdame/CodeSampleX
```

This command is **on hold while the public health failures below continue**.
No previous SHA, target, owner or image can be supplied to replace the original
evidence. The workflow obtains them from the authenticated original artifact.
The source attempt and artifact are explicit, immutable references; later
reruns of the original deployment cannot substitute a different attempt or
invalidate the historical receipt used by a successful reconciliation.
It requires green canonical CI for its own operational source and shares the
normal production concurrency group and pinned SSH identity/host-key policy.

The two stages are:

1. Verify public health and retained owner/config/evidence bytes, including the
   original target, previous SHA/image, operational revision, exact nanosecond
   start string, reviewed migration ledger and index definitions. On the host,
   require an inactive supervisor with no processes, no owned helper or index
   DDL, unchanged container identity with no restart/OOM, and fresh loopback and
   TLS proxy health/version/representative and DB-backed route smoke.
2. Upload an immutable preparation artifact, then read it back through GitHub
   and verify its ZIP SHA256 before a second complete live check. Under the
   existing host command flock, write and sync an owner receipt and atomically
   rename `.deploy-lock` to `.deploy-reconciled-<original-owner>`. The owner file
   and migration evidence remain retained. No lock is deleted or adopted.

Supervisor and helper/database cleanup checks are repeated after smoke, before
release. Retained owner, config and evidence paths must keep their filesystem
identity and bytes throughout verification. Trusted owner and write-permission
checks reject a writable or replaced ownership namespace; the existing 0775
deploy directory is accepted only when its primary group is proved private to
the trusted owner. Reconciliation does not change host permissions.

A lost response or final artifact upload can be retried. Missing `.deploy-lock`
is accepted only with the exact owner archive, original source binding and
authenticated historical preparation, plus fresh unchanged live identity.
Every retry re-establishes receipt and directory durability. A new owner,
abort marker, extra file, symlink, hardlink, partial receipt, changed process
start, expired source artifact or contradictory evidence fails closed. A
partial receipt is deliberately retained for inspection, not overwritten.

Only a successful reconciliation run with its attempt-specific final artifact
is accepted by `Post-deploy observation`. It validates the original failed
run and receipt/preparation chain, preserves the original production run number
for ordering, and observes the original process start. It does not manufacture
a new activation or claim builder convergence. The failed original run itself
remains ineligible. Observation can also be repeated using the successful
reconciliation run ID as `deploy_run_id`.

Prepared/final reconciliation artifacts are retained for 90 days; the original
deployment artifact keeps its existing retention policy. Verification requires
all referenced artifacts to remain available and rejects an expired or deleted
dependency. Preserve those GitHub IDs/digests and the host archive when handling
any later manual recovery. This workflow cannot reconcile a replacement process
with a different `serverStartedAt`.

## Full-builder pause audit and live capacity hold

Audit source: canonical main `7f2ab9a6838bc6a5cb9bc6faf95b6ba22bd5cd0a`
and active v0.1.158 target `4b08ed5093740268f50682011e918dc8d3744f35`.
The relevant builder, startup configuration and pool files are identical at
these revisions. GitHub [#347](https://github.com/r2cuerdame/CodeSampleX/issues/347)
and [#174](https://github.com/r2cuerdame/CodeSampleX/issues/174) remain the incident
record; host acceptance is not evidence of sustained user-route recovery.

There is **no supported runtime builder-only pause control** in the active
image. [`StartBuilder`](../cmd/csx-server/mux.go) starts a goroutine inside the
serving process and retains no separate cancellation handle. The
[`admin routes`](../internal/admin/admin.go) expose no builder pause API.
[`Configuration`](../internal/serverstore/config.go) reads snapshot interval,
pass timeout and pool policy only at startup. A zero snapshot interval falls
back to five minutes; a zero pass timeout removes the ceiling. The documented
timeout override requires recreating the server service.

The [`builder loop`](../internal/compatibility/builder.go) starts immediately,
then permits five failure retries at roughly 1/2/4/8/16 seconds before deferring
for its normal interval. Increasing that interval cannot stop an active pass.
A completion stamp older than 24 hours correctly requires a full repair;
shortening the pass timeout cannot create a safe incremental baseline.
Completed output chunks are retryable, but only successful completion advances
the watermark. Never manufacture a fresh stats stamp or clear a repair marker.

The HTTP server and builder share a process/cgroup, so container pause, SIGSTOP
or CPU throttling would also affect user traffic. PostgreSQL connections are
reused by the [`shared pool`](../internal/serverstore/pool.go), and builder,
ingest and authoring share the background query class. There is no
builder-specific backend identity for an operator to cancel safely. Guessed
backend cancellation would also trigger the existing retry loop. Pool reserves,
database-backed health checks and security settings remain required.

A future bounded pause needs an independently cancellable builder context,
acknowledgement only after its active database work returns, and an admission
gate covering normal passes and retries until a fixed maximum expiry. It must
preserve source data, completed watermarks and required full repairs. That
feature would need deployment; it cannot be added to this already-active process
during the present health/ownership hold.

### Fresh read-only evidence, 2026-09-11 UTC

An unchanged Playwright/Edge panel, with certificate validation and the documented
origin mapping, made sequential requests at 15:40:55–15:41:45. `/version` returned
the exact active target above. The other results demonstrate continuing failure:

| Route | HTTP | TTFB seconds |
| --- | ---: | ---: |
| `/healthz` | 503, `database unavailable` | 3.745 |
| `/` | 200 | 7.288 |
| `/samples` | 503 | 6.924 |
| `/ko/` | 200 | 4.676 |
| pgx v5.10.0 detail | 503 | 0.596 |
| OTel v1.45.0 detail | 503 | 4.594 |
| representative sample | 503 | 5.281 |
| `/v1/stats` | 500 | 4.605 |

At 15:41:02, host `vmstat` samples recorded CPU steal of 82% and 80%; CPU PSI
`some avg10` was 75.78%. The target container remained running with start
`2026-09-11T14:15:58.802547Z`, image
`sha256:9440fdf7bd104daa3fe400db3b3524cfda77fd947e9747f219742b81d952cca5`,
restart count zero and OOM false. The builder had full-pass starts at 14:16:03
and 14:29:00, one deadline failure at 14:28:54.320523791, and no observed completion.
The cumulative event parser at 15:43:14 recorded 9,964 pool-busy events, 27 query
timeouts, 150,693 admission refusals and 336 deferred-lane refusals since process
start. These are counter values, not counts of matching log lines.

At 15:42:19 the original owner lock remained present and unfenced, with retained
host evidence `phase=committed`, `conclusion=success`, and unchanged target/start.
At 15:46:14 the original migration unit returned `ActiveState=inactive`,
`SubState=dead`, `MainPID=0`, `ControlPID=0`, exit zero. A nonexistent unit returns
the same four properties and exit zero on this host; existence cannot be inferred
from that command's success alone.

Read-only AWS queries confirmed `csx-prod-1`, `small_3_0`, 2 vCPU / 2 GiB / 60 GiB,
in `ap-northeast-2`. Twelve five-minute measurements at 14:42–15:37 show average
CPU utilization 19.998329–20.002731% and burst capacity 0.011049–0.011110%.
Together with live steal and failing routes, these establish a continuing
capacity blocker under the current no-deployment/no-restart constraints.

A later independent Playwright panel at 16:19:32–16:19:39 returned HTTP 200 on
all nine routes with the same exact target. Health TTFB was 0.294 seconds;
samples was 1.702 seconds, pgx 1.504 seconds, OTel 0.328 seconds and stats 0.329
seconds. Stats still reported `generatedAt=2026-09-09T17:32:31Z`. This is a
fresh improvement in route health and can justify attempting the canonical
fail-closed reconciliation after green CI and fresh host checks. It does not
establish sustained recovery or a completed builder pass. The latest canonical
workflow result and incident comments determine the operational hold; a single
healthy diagnostic does not replace them.

The bounded host recheck at 16:20:31–16:20:48 likewise returned HTTP 200 on
three loopback health requests. The original owner, raw evidence SHA256,
image/start and zero restart/OOM identity were unchanged; the supervisor was
collected/inactive with both PIDs zero. CPU steal remained 77–81% and CPU PSI
avg10 80.70%. Memory was 769,552,384 of 805,306,368 bytes, with 2,590 cumulative
`memory.max` events and zero OOM events. Logs still showed two full-pass starts,
one failure and no completion, with the latest start at 14:29:00. These residual
pressure and convergence findings remain incident evidence even if owner
reconciliation passes. The public and host JSON SHA256s are respectively
`3ac3a649cdc685ab55cc2dfd995db085c1b491ae3bee41ea15b20e75aba86fc3` and
`92a4506a5e9b62229fa17802005d9964320f976549f57b0574bdde5e253c27f9`.

### Cost decision if capacity recovery is required

Regional `get-bundles` prices and the official
[AWS baseline table](https://docs.aws.amazon.com/lightsail/latest/userguide/baseline-cpu-performance.html)
were checked on 2026-09-11. Aggregate CPU below is the arithmetic product of
vCPU count and baseline, not measured application throughput.

| Linux public-IPv4 bundle | USD/month | Monthly delta | vCPU / GiB | Baseline per vCPU | Aggregate baseline |
| --- | ---: | ---: | --- | ---: | ---: |
| `small_3_0`, current | 12 | 0 | 2 / 2 | 20% | 0.4 vCPU |
| `medium_3_0` | 24 | +12 | 2 / 4 | 20% | 0.4 vCPU |
| `large_3_0` | 44 | +32 | 2 / 8 | 30% | 0.6 vCPU |
| `xlarge_3_0` | 84 | +72 | 4 / 16 | 40% | 1.6 vCPU |

The 4-GiB class increases memory but does not increase sustained CPU allowance.
The 8-GiB class is the smallest listed general-purpose increase in CPU baseline;
its allowance is only 50% above the current host. No listed class is proved
sufficient by the throttled utilization measurement. See
[AWS prices](https://aws.amazon.com/lightsail/pricing/) and
[bundle specifications](https://docs.aws.amazon.com/lightsail/latest/userguide/amazon-lightsail-bundles.html).

The required manual decision is a recurring budget/class plus temporary
overlap, snapshot and transfer costs, coupled to a reviewed stateful cutover.
[AWS upsizing](https://docs.aws.amazon.com/lightsail/latest/userguide/how-to-create-larger-instance-from-snapshot-using-console.html)
creates a new instance from a snapshot. The plan must fence both databases'
writers/builders, prove restored ledger/data/blob consistency, keep exactly one
authoritative writer during IP cutover, and account for writes received before
any rollback. A live snapshot followed by an IP move is insufficient. A new
container start also changes this deployment's exact `serverStartedAt`, so owner
disposition and provenance must be settled explicitly before cutover.

No restart, lock removal, SQL mutation, snapshot, resize or new paid resource was
performed. The source audit and fresh failure prove why the existing controls
cannot deliver a safe immediate builder pause; they do not prove that a full
pass can never finish or that a capacity change alone guarantees recovery.

Focused existing builder retry/cancellation/ceiling/resume/repair-clock and
configuration contracts passed 27 cases/subtests with zero failures or skips.
Raw read-only evidence and collector sources are retained in the audit
worktree's `.tmp/builder-pause-audit/`, with the public results and cost decision
carried into the linked GitHub incident record.
