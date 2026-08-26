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

## Clean-room proposal workspaces

`csx sample propose` and MCP `propose_public_sample` build the same thing
through one function, `samples.NewProposalWorkspace`. They used to be two
implementations and only the CLI one wrote the scaffold, which is how the MCP
tool came to promise a `csx.json` it never created.

A workspace lives at `<CSX_HOME>/samples/work/sample-*` and always holds three
files before any caller learns it exists: `spec.json` (the sanitized brief),
`PROMPT.md` (the generation instructions), and `csx.json` (the manifest
scaffold, with `case.contract` empty for the agent to fill). `PROMPT.md` tells
the agent that `csx.json` already exists and must not be recreated from
memory, so the file existing is a precondition of the instruction, not a
convenience.

**Atomic creation.** Files are written into a `.staging-*` directory and the
directory is renamed into its `sample-*` name only after every file is on disk
and `samples.VerifyProposalWorkspace` accepts it. No observer can see a
`sample-*` directory in a half-built state, and a failure leaves no `sample-*`
directory at all.

**Fail-closed.** A workspace that cannot be built is an error, never a path.
The MCP tool re-verifies the workspace before rendering its success text and
reports failures with `isError` plus a `structuredContent.error` code:
`invalid_packages` for purls the caller got wrong, `scaffold_failed` for a
workspace this machine could not create. The two need opposite reactions, so
they are never merged into one message.

**Retry and cleanup policy.** An identical proposal — same sanitized spec —
whose workspace still holds only the untouched scaffold is handed back rather
than duplicated, and `SaveProposal` upserts on workdir, so retrying a proposal
adds nothing. Once any other file appears, the workspace belongs to whoever
wrote it and a repeat proposal gets a fresh one. Each proposal also sweeps
`sample-*` directories that contain no files at all and are older than an
hour; that is debris from before atomic creation, and `os.Remove` refuses a
directory that is not empty, so nothing with content in it can be lost.

See [schema.md](schema.md) for field semantics,
[execution-context.md](execution-context.md) for environment bucketing, and
[operations.md](operations.md) for migration and rollout checks.
