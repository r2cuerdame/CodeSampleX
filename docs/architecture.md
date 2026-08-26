# Architecture

This document is the current component map. `goal.md` records the initial
product plan; code, schemas, and this document take precedence where the
implementation has evolved.

## Evidence path

`csx run` and MCP `run_observed_command` execute through the same runner. A
failed executable command yields a structured termination (`exit`, `signal`,
`timeout`, or `process-start-failed`) and a bounded local stdout/stderr tail.
Before sanitization, the failure-stage analyzer separates the outer workflow
intent from actual compiler, resolver, and test-runner diagnostics. One outer
execution can emit multiple independent failure events; aggregate markers such
as `[build failed]` prove a build stage but never become an invented root
diagnostic. The public lineage is bounded to a known outer tool/subcommand,
actual toolchain, stage-decision evidence, and an explicit Evidence gap.
The sanitizer removes secrets and volatile coordinates, creates a short public
error summary, and hashes the actual stage + actual toolchain + termination +
error. The outer command is excluded from fingerprint identity so equivalent
nested failures reached through different wrappers remain one family. Raw logs
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

## Public URLs and the search surface

A published sample answers at two addresses and they are one page.

`/samples/sha256:<digest>` is its **identity**. It is what the HTTP API and
the CLI hand out, what external links point at, and what a content address
means, so it keeps answering `200` and it is never redirected — a redirect
would land in front of every one of those callers.

`/{ecosystem}/{name}/{version}/samples/{slug}` is its **canonical** URL, and
the readable one. `internal/web/serpcopy.go` derives the slug from the
sample's subject and eight hex characters of its own content address, which
makes it a pure function of the sample: nothing published later can change
it, and two samples for the same release cannot collide. The digest URL
names this one in `rel=canonical`, and — because hreflang describes the
canonical page's locale cluster and not the disavowing page's — the digest
URL emits no `hreflang` links of its own. The sitemap and every internal
sample link use the canonical URL, so the address a crawler is given is the
address the page declares.

A sample whose manifest names no routable release (no version, or an
ecosystem the explorer does not route) has no readable URL. It keeps the
content address as its canonical and says so; nothing is advertised that the
router cannot resolve back. `semanticSampleHref` proves that by splitting
every URL it emits back apart with the router's own splitter before
returning it.

**The copy is bounded by what was established.** The title says "Verified
sample" only when a receipt records a contract that passed — the same rule
`levelBadge` applies to the badge — and says "Code sample" otherwise. The
description quotes the first contract line only on the verified side: on a
sample nothing ran for, the contract lines are a plan, and quoting them
would read as a result. The environment it names is the RECEIPT's, never the
author's declared one.

**Why the copy is derived and not taken from the goal.** Most goals in the
corpus are the line `csx sample-worker next` prints for an authoring agent
to start from — "verify pkg:npm/browserslist@4.28.7". Agents are expected to
replace it and often do not, and a published sample is immutable, so the
whole existing corpus carries it. Titling pages with it put an internal
package URL, twice, in front of every search that reached them.
`internal/web/testdata/production-samples-2026-08-27.json` is 24 real
published samples across eight ecosystems, nine of them carrying that goal;
`serpcorpus_test.go` renders the search surface of every one.

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
