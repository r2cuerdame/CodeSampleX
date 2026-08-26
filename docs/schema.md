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
The legacy v2 fingerprint hashes stage, structured termination, error code,
and this summary. Actual-stage evidence uses v3, which additionally hashes the
actual failing toolchain. `outerCommand` and `outerStage` are lineage evidence
but are intentionally excluded from the fingerprint, so the same `tsc`
diagnostic reached through `go test` and `npm test` remains one family.
Package/version scope and exact environment are stored beside the fingerprint
so the same failure can have multiple environment variants without creating
unbounded cluster identities.

Observation-batch v1 remains byte-for-byte frozen and carries the legacy stage
vocabulary and v2 failure fingerprint. Observation-batch v2 adds actual-stage
vocabulary and the lineage fields above; new clients emit v2 and new servers
accept both versions. Deploy the accepting server before releasing the v2
client. An older server explicitly refuses v2, and the client restores every
refused aggregate to its durable pending queue instead of silently
down-converting or losing classified evidence.

Classified failure rows also carry `actualToolchain`, `stageEvidence`, and an
optional `evidenceGap`. `stageEvidence` is one of structured termination,
resolve diagnostic, compiler diagnostic, test-runner diagnostic, build
aggregate, or unclassified diagnostic. A build aggregate without a compiler
diagnostic records `PROJECT_COMPILE` plus `diagnostic-missing`; it does not use
the aggregate text as a normalized cause. Unclassified output records
`UNKNOWN` plus `stage-unknown` instead of inheriting the outer command stage.

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
- `ObservationBatch` v1/v2 is the versioned public upload contract.
- PostgreSQL `evidence_agg` stores delta-merged environment rows, including the
  sorted set of every bounded outer command that produced one aggregate.
- `failure_clusters` stores cross-version cluster identity, environment
  variants, quality breakdown, diagnostic-candidate state, and failure lineage.
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

### One symbol, two spellings, one count

The same symbol reaches the server twice over: anonymous evidence carries the
scanner's qualified name (`github.com/google/uuid.Parse`), a signed receipt
carries the author's bare one (`Parse`). Both become live snapshot targets,
and `symbolSpellings` makes `EvidenceForTarget` answer either target with the
same rows on purpose, so a page asking about one spelling sees the evidence
filed under the other.

A snapshot is built per target, and reading a row twice under two targets
costs nothing there. A failure cluster is built per **package**, from every
target's rows summed into one bucket — and there the shared rows arrive once
per spelling. `Builder.evidenceForPackages` therefore admits a row by its
`evidence_agg` identity (`purl, symbol, env_hash, stage, result, error_fp`)
and ignores the second arrival. Without that, production carried a cluster of
520 observations for 260 observed failures, and because an incremental pass
only reads the targets it is rebuilding, the doubling appeared and vanished
with the pass shape — which is how a full rebuild after a deploy's restart
moved the cluster ledger while no evidence had been gained or lost.

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
