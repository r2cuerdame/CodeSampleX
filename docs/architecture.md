# Architecture

This document is the current component map. `goal.md` records the initial
product plan; code, schemas, and this document take precedence where the
implementation has evolved.

## Evidence path

`csx run` and MCP `run_observed_command` execute through the same runner. A
failed executable command yields a structured termination (`exit`, `signal`,
`timeout`, or `process-start-failed`) and a bounded local stdout/stderr tail.
The sanitizer removes secrets and volatile coordinates, creates a short public
error summary, and hashes the normalized stage + termination + error. Raw logs
stay local.

The local SQLite queue stores that public failure evidence. Community sync
sends it as an observation batch; PostgreSQL delta-merges it into
`evidence_agg`. Compatibility aggregation keeps the cluster fingerprint apart
from environment variants, then materializes snapshots and
`failure_clusters`. The API and web explorer read those materialized records;
they never aggregate raw logs on a request.

Verifier resolve/compile/contract failures use the same sanitizer and carry
secret-safe `stageFailures` in the signed receipt. The full stage logs remain
local and only their digest is signed.

Repeated missing or legacy-incomplete evidence is retained as an Evidence gap
and marked as a diagnostic re-verification candidate. Those cluster rows enter
the existing Farm authoring work lane; a new verification appends evidence and
does not rewrite history.

See [schema.md](schema.md) for field semantics,
[execution-context.md](execution-context.md) for environment bucketing, and
[operations.md](operations.md) for migration and rollout checks.
