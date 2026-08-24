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
- `ObservationBatch` is the public upload contract.
- PostgreSQL `evidence_agg` stores delta-merged environment rows.
- `failure_clusters` stores cross-version cluster identity, environment
  variants, quality breakdown, diagnostic-candidate state, and failure lineage.
- Verification receipt v2 stores secret-safe `stageFailures`; `logsDigest`
  covers the local-only full logs.

Legacy rows are never backfilled with guessed causes. Re-verification produces
a separate modern observation or receipt.
