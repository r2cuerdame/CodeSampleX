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
