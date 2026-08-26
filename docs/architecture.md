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
they never aggregate raw logs on a request. `failure_clusters` still holds the
rows written before structured failure evidence existed, so a reader picks
between the current clusters and every recorded one — the pages and the deploy
ledger take the first, exact fingerprint search takes the second
([schema.md](schema.md)).

Verifier resolve/compile/contract failures use the same sanitizer and carry
secret-safe `stageFailures` in the signed receipt. The full stage logs remain
local and only their digest is signed.

Repeated missing or legacy-incomplete evidence is retained as an Evidence gap
and marked as a diagnostic re-verification candidate. Those cluster rows enter
the existing Farm authoring work lane; a new verification appends evidence and
does not rewrite history.

## Code availability and compatibility

The web explorer answers two questions that used to share one mark, and they
do not share a key. Code availability is `package + version + symbol/API`
(`internal/web/cubecode.go`), built from the published samples the package page
already loads and taking no environment argument at all: a sample belongs to a
release and an API, and it does not stop existing because the reader switched
the OS filter to a platform the fleet never ran it on. Compatibility is that
key plus the environment bucket, and it is what the diamond and the cross
report. Both can be true at once — there is code here, this environment was
never verified — so the cell, the leaf record and the legend carry them as
separate marks.

`internal/web/cubenav.go` is the depth ladder over the same coordinates:
package → version → environment/tool → symbol/API → sample/evidence. Rungs
this package never recorded are left out, the rung the reader is standing on is
marked, and the bottom of the drill is stated rather than left to be inferred
from an empty grid. A drill-down affordance renders only where a next
coordinate exists, and an evidence action only where its destination does.

See [schema.md](schema.md) for field semantics,
[execution-context.md](execution-context.md) for environment bucketing, and
[operations.md](operations.md) for migration and rollout checks.
