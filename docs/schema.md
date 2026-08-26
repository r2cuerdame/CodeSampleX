# Data schema

The machine-readable wire schemas live under `schemas/`. This document defines
the cross-store semantics that are easy to lose when reading one table alone.

## Failure evidence

Every new executable failure should carry one structured termination:

| `terminationKind` | Required detail |
| --- | --- |
| `exit` | `exitCode`, including zero when explicitly recorded |
| `signal` | normalized signal name |
| `timeout` | `timeoutMillis` when known |
| `process-start-failed` | no conventional exit code |

`errorSummary` is a redacted, normalized, 512-byte maximum summary. Source,
paths, project names, private packages, secrets, and raw logs are prohibited.
The v2 fingerprint hashes stage, structured termination, error code, and this
summary. Package/version scope and exact environment are stored beside the
fingerprint so the same failure can have multiple environment variants without
creating unbounded cluster identities.

`evidenceQuality` has four values:

- `complete`: structured termination and normalized error summary
- `partial`: exactly one of those is present
- `missing`: neither is present; no failure reason is inferred
- `legacy-evidence-incomplete`: a row written before this contract

The last value is storage/read compatibility only and is never emitted by a
new producer. PASS rows carry no failure fields. Quality counts in a snapshot
must sum to its total FAIL count.

## Persistence

- SQLite `observations` is the durable client queue.
- `ObservationBatch` is the public upload contract.
- PostgreSQL `evidence_agg` stores delta-merged environment rows.
- `failure_clusters` stores cross-version cluster identity, environment
  variants, quality breakdown, and diagnostic-candidate state.
- Verification receipt v2 stores secret-safe `stageFailures`; `logsDigest`
  covers the local-only full logs.

Legacy rows are never backfilled with guessed causes. Re-verification produces
a separate modern observation or receipt.

## `failure_clusters` is derived, and 0024 preserved what it had

Clusters are materialized from `evidence_agg`; the builder upserts and never
deletes. Migration `0024_failure_evidence.sql` added the failure-evidence
columns and deliberately did **not** clear the table: emptying a production
table is a destructive operation and does not belong in an unattended additive
migration.

So the rows written before this contract are still there. The current builder
never writes their keys again — a hash produced before termination and
normalized error were recorded is provenance, not a failure identity, so
`missing` and `legacy-evidence-incomplete` evidence collapses into one
explicit evidence-gap row with an empty `error_fp`. The preserved rows are
historical material sitting beside the live ones.

**A legacy failure is never promoted to a modern fingerprint.** Nothing
recomputes a v2 fingerprint from a row that never carried termination or a
normalized error, and no count moves from an Evidence gap to a fingerprinted
cluster on its own. A modern cluster exists only where a producer that emits
structured termination recorded a new failure. Until such a client is
released, `evidence_quality` on every FAIL row stays
`legacy-evidence-incomplete` and the modern cluster count is legitimately
zero — that is the contract reporting the truth, not a defect.

### Two reads, two questions

`IsCurrentFailureCluster` and `CurrentFailureClusterPredicateSQL` in
`internal/serverstore` are one rule in two spellings, and every reader must
pick the question it is asking:

| Read | Serves | Used by |
| --- | --- | --- |
| `ListFailureClusters` | clusters the builder currently writes | symbol/version pages, Farm FINDING work, the deploy ledger |
| `ListFailureClustersIncludingPreserved` | every recorded cluster | exact failure-fingerprint search |

Search needs the preserved rows and cannot be narrowed to current ones. Every
released client fingerprints a failure as `v1|stage|code|template`, so every
fingerprint on file lives on a preserved row, while a rebuilt evidence-gap row
carries no fingerprint at all and can never match anything. Serving only
current clusters to exact matching turns the feature off until v2 fingerprints
accumulate.

Counting the preserved rows is the other half of the same mistake: it counts
the same failures twice. Production went from 17,737 to 35,488 cluster
observations on the 0024 rollout while the FAIL total never moved.
