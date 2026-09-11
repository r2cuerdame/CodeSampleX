# Observer evidence after reconciliation (#174 / #347)

This change is an observation-controller repair. It does not deliver a server
image, start a builder, change SQL timeouts, clear errors, or restart production.
Stability acceptance remains unproved until a canonical run collects the required
evidence. A settled pass cannot retrospectively supply active-builder samples.

## Diagnosed failure

The canonical record is [#347's bounded database-log correlation](https://github.com/r2cuerdame/CodeSampleX/issues/347#issuecomment-5638014878)
and [#174's observer artifact audit](https://github.com/r2cuerdame/CodeSampleX/issues/174#issuecomment-5638023964).
Reconciliation [34625408879](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34625408879)
preserved v0.1.158 / `4b08ed5093740268f50682011e918dc8d3744f35`, image
`sha256:9440fdf7bd104daa3fe400db3b3524cfda77fd947e9747f219742b81d952cca5`,
and server start `2026-09-11T14:15:58.802547Z`.

Observer [34625507629](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34625507629)
ran 17:04:18–17:05:11 UTC. Its first cheap sample had fresh stats, a completed
builder and one retained error. The controller supplied the original server
start as `CSX_OBSERVE_SINCE`; `Get-StateAnomalies` treated the entire lifetime
builder error count as an error during this observation. The loop consequently
stopped after the first sample, with zero of five required active rounds.

Its terminal SQL computed `SUM(observation_count) FILTER (WHERE result='FAIL')`
over all `evidence_agg`, plus a current-cluster sum and an unbalanced-row count
over `failure_clusters`. Each candidate row also expanded `evidence_breakdown`
twice with `jsonb_each`. There was no row bound. The unchanged 20-second
statement timeout and the exact query marker were recorded in the bounded
historical DB-log receipt at 17:05:11.027 UTC, matching the terminal collector's
26.121-second failure. This follow-up did not rerun that production query.

## Evidence contract

The observation start is recorded on the production host before extended
probes and bound to verified before/after identity, independently of the
immutable server-start horizon. Controller/host clock skew cannot move this
boundary. A successful timestamped log read with a
fixed remote end separates historical builder errors from errors at or after
the observation start. Missing/invalid boundaries, log failures and malformed
matching records fail closed. Lifetime totals remain in evidence. A decrease
in retained counts during observation is also an anomaly.

Pressure retains its original lifetime fields. Additional observation-window
line counts and maximum wait use the same final timestamped log read, after
latency probes. The zero-timeout, zero-pool-busy and three-second maximum-wait
gates apply to this explicitly measured window. Rate-limited lifetime counters
are not relabeled as exact window event counts. Unavailable measurements cannot
be interpreted as zero pressure. This establishes retained-log evidence, not
an independent proof that logging could never omit an event.

The settled predicate remains `(FAIL > 0 implies current-cluster total > 0)`
and `unbalanced current rows = 0`. A complete, positive, balanced current ledger
proves both without a source census. Therefore that branch leaves the source
FAIL total null. An empty current ledger requires complete source proof.

Fixed caps precede filtering and aggregation: 250,000 cluster rows plus one
overflow sentinel; the empty-ledger branch allows 10,000 source rows plus one
sentinel. Current/historical cluster selection retains the server predicate.
Only four fixed JSON keys are checked, including types, nonnegative values,
extra keys and exact balance. Compressed or over-4,096-byte JSON fails the
collection budget before expansion. A cap overflow is `budget-exceeded`, not
a sampled success. The 20-second SQL deadline and outer bounds are unchanged.
Null totals are unmeasured; query exit code and elapsed seconds are retained.

The extended exact census likewise caps source, cluster and sample inputs at
250,000 rows each and rejects incomplete scope. A budget failure preserves
public/health/privacy evidence and emits a fixed diagnostic, without SQL stderr.
It remains an acceptance failure. No cached or estimated count replaces an
exact census.

Settled evidence is collected independently when a terminal sample remains
fresh and complete throughout collection. It never adds an active round or
sets `converged`. Five genuine latency rounds, canonical content, the original
TTFB bound and a completed lifecycle remain mandatory. The existing bounded
80-minute poll loop can wait for naturally occurring passes. If those passes
are too short or absent, it reports insufficient evidence and stays failed;
it neither starts a pass nor fabricates a pass duration.

## Delivery and safe continuation

The follow-up audit preserves the original local run in
[`observer-stability-validation-174.json`](evidence/observer-stability-validation-174.json).
Its 14 failed test events represent 13 leaf cases plus one failed parent:
five recovery-output cases, seven updater fixture cases, and one registry case.
All implicated source is unchanged by this repair. Canonical main
`6d692796` passed [Linux and Windows CI 34624425367](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34624425367);
the original failures pass unchanged in the RDC execution context.

Independent review found one actual patch regression: successful `docker logs`
reads discarded stderr, where the server emits pressure records. The correction
restores status-checked stderr capture. The new successful-stderr fixture fails
before that correction and passes afterward, proving retained lifetime counters
and maximum wait remain exact. Failed log reads still remain unmeasured.

The [follow-up validation record](evidence/observer-followup-validation-174.json)
contains every original failure and its final result, exact test commands,
pass/fail/skip counts, log hashes, and tested source hashes. It also records the
Windows Chrome de-elevation/stdio A/B proof and test-only adapter, the local
Chrome hang and working headless-browser setup, and the bounded Go-version
probe rerun. Repository test assertions and deadlines were not relaxed.
PostgreSQL-backed observer and PowerShell 7 contracts run without skips on both
platforms; Linux full testing requires PostgreSQL and browser availability.
Windows full testing follows canonical Windows CI's database-free environment.

| Local validation | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: |
| Windows observer/deploy contracts | 376 | 0 | 0 |
| Linux observer/deploy contracts | 374 | 0 | 0 |
| Windows full suite | 3,666 | 0 | 193 |
| Linux full suite | 3,817 | 0 | 24 |

Counts include subtests. Both full suites use `-count=1`; `go vet ./...`
passes on both platforms. Exact skip names and the failed intermediate runs
remain in the validation record rather than being counted as passing coverage.

The workflow authenticates deployment provenance exactly as before, but checks
out its own immutable `github.workflow_sha` for observation. Observer controller,
deployment controller and deployed target must independently be ancestors of
canonical main. Both controller SHAs are recorded. This allows a merged
observation-only correction to run without redeployment or another reconciliation.

After the reviewed change and relevant full CI are green and merged, the sole
intended production action is the existing read-only observer dispatch:

```sh
gh workflow run post-deploy-observation.yml --ref main \
  -f deploy_run_id=34625408879 --repo r2cuerdame/CodeSampleX
```

Authenticate its provenance, controller SHA and artifact, retain all failures,
and publish the exact result on both issues. If active evidence is absent, keep
both issues open and wait for natural work. If a collection budget is exceeded,
retain the explicit cap and measured failure; do not raise it or buy capacity
without a separately reviewed cost/measurement decision. A live deployment is
not required to deliver these scripts.
