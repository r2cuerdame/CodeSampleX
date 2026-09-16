# Architecture

This document is the current component map. `goal.md` records the initial
product plan; code, schemas, and this document take precedence where the
implementation has evolved.

## Runtime topology

Two Go binaries share the production PostgreSQL database, each with its own
connection pool and no shared in-process state between them:

* **csx-server** (`cmd/csx-server`) — Web/API. Public and admin HTTP, plus
  (until CSX-451's split is activated by #455) the compatibility Builder
  running as a goroutine of this same process, as it always has.
* **csx-builder** (`cmd/csx-builder`, CSX-451) — the compatibility
  aggregation pipeline (`internal/compatibility.Builder`) as a standalone
  process, so a Builder pass that overloads its own database pool, panics,
  or leaks cannot take Web/API down with it. Milestone v0.1.197 Runtime
  Isolation is why this exists: production held eight shared connections
  against a wedged Builder pass for roughly twenty hours on 2026-09-09
  because the two were one process with one pool.

Both binaries can run the aggregation pipeline; `CSX_BUILDER_MODE`
(`internal/serverstore.ServerConfig.BuilderMode`) selects which one is
active, and a PostgreSQL-backed leader lease (`internal/compatibility.Leader`,
`builder_lease` table) guarantees at most one of them runs a pass at a time
regardless of which is nominally "on" — see docs/operations.md "Builder
runtime topology" for the environment variables, the lease mechanics, and
the rollback procedure. Both paths are kept, rather than deleting the
in-process one, until #455's staged production rollout has proven the
standalone path under real load: the honest rollback for this stage is a
variable, not a revert.

**CodeSampleX-Farm (CSX-453)** is not a third process here — its GEN/VERIFY
work runs entirely outside this repo (coordinated at
[CodeSampleX-Farm#133](https://github.com/r2cuerdame/CodeSampleX-Farm/issues/133)),
and it reaches csx-server the same way any anonymous CLI/MCP client does:
public, unauthenticated HTTP (`/v1/evidence/batches`, `/v1/verifications`,
`/v1/verification/jobs`). A separate OS process would have needed Farm
pointed at a new listener for no isolation this repo's admission control
cannot already give it, so instead it is `serverstore.ClassFarmIngest`, a
fourth admission class alongside interactive/background/probe with its own
reserved connection floor and its own statement/wait ceiling
(`internal/serverstore/pool.go`, `CSX_DB_FARM_*` — see docs/operations.md
"Settings and rollback"). What CSX-451 solved with a process boundary,
CSX-453 solves with a resource-budget boundary, because the failure modes
differ: the Builder was unbounded compute inside one process; Farm ingest is
many short, already-idempotent transactions arriving over the public
listener every other client shares.

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

## Decision diagnostics

Search and observed-command diagnostics share `domain.DiagnosticTrace`. The
search engine creates the actual candidate counts, score contributions,
compatibility checks, rejections, gaps, and timing; MCP and CLI only add the
versions known at their boundary and render that object. Normal output has no
diagnostic object. An explicit tool option, global CLI `--debug`, or
session-level `CSX_DEBUG=1` enables it without changing the search response or
wrapped command result.

Failure diagnostics project the already-sanitized R2C-156 analysis into the
same trace. They do not reparse raw logs or create a second stage classifier.
Search candidates are bounded and carry public coordinates, scores and reason
codes only; their goal, contract, source, and evidence bodies remain behind the
normal-output relevance gate. See [diagnostics.md](diagnostics.md).

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

### Read models: bounded reads over Builder-owned attribution (CSX-452)

A symbol's package attribution is not a per-request computation. Which purl
owns a claimed symbol is decided by `snapshotTargetsFromClaims`
(`internal/serverstore/attribution.go`) comparing every receipt across the
whole corpus that claims that symbol — genuinely corpus-scale work, and one
a public request must never redo. The Builder runs that attribution once per
pass (`internal/compatibility/builder.go`, the same computation that decides
`compatibility_snapshots` rows) and publishes the result grouped by purl into
`package_symbols` (migration `0044_package_symbols.sql`), one row per purl,
upserted only for the purls a given pass actually touched.
`GetPackageSymbols(purl)` is the one bounded, primary-key read every public
and admin caller uses — `internal/httpapi/registry.go`'s
`GET /v1/registry/packages/{purl}` is the first caller migrated onto it, off
what used to be a `SELECT DISTINCT purl, symbol FROM evidence_agg` plus a
full receipts/samples join on every request.

**Freshness/rollback**: a purl the Builder has never touched has no row at
all (`GetPackageSymbols` returns `found=false`, not an empty list standing in
for "not computed yet"). A purl whose symbols later disappear but whose
package-level target survives publishes an empty list on the next pass that
touches it — not a stale non-empty one. A purl that leaves the corpus
entirely is not retired from `package_symbols`; it keeps answering its last
known symbols, the same stale-over-absent choice `failure_clusters` already
makes elsewhere. There is no feature flag: this is a plain read-path swap
behind the same `Store` interface, so rollback is a normal revert, not a
runtime switch like CSX-451's `CSX_BUILDER_MODE`.

**Atomic publication (CSX-452)**: `flushSnapshots()`'s batch boundary
(`internal/compatibility/builder.go`) is purl-aware -- a batch only flushes
once it has reached `snapshotWriteBatch` *and* the next target belongs to a
different purl (or there is no next target). A purl with more symbols than
fit in the remainder of a batch grows that batch past `snapshotWriteBatch`
rather than being cut across two `PutSnapshots` transactions. Readers query
one purl at a time (`GetSnapshotsForPURL`, `symbolsForPURL`), so this is the
actual atomicity unit that matters: a reader never observes a purl mid-pass,
part old symbols and part new. `generatedAt` (`package_symbols.generated_at`,
surfaced on `GET /v1/registry/packages/{purl}`) is the freshness contract API
consumers check -- it is the Builder pass timestamp that last published this
purl's symbol list, and is absent from the response for a purl no pass has
published yet (`found=false`).

`farm_coverage` (migration `0045_farm_coverage.sql`) is the same pattern
applied to the admin farm panel's coverage cells. The (os, ecosystem)
compatibility-map aggregation — `evidence_agg` joined to `packages` for what
the network has seen used, `receipts` joined to `samples` for what it has
actually proven, expanded per resolved package — used to run live on the
admin request path (`internal/serverstore/farm_pg.go`'s `FarmCoverage`, a
25-second-ceiling corpus scan) on every cache-miss the panel's own memo
allowed through. The Builder now runs that exact aggregation once per pass
(`Builder.computeAndPublishFarmCoverage`, called from `RunOnce` right after
`writePackageSymbols`) and publishes the whole result — every
`(os, ecosystem)` cell, not a per-key upsert — into `farm_coverage`.
`GetFarmCoverage()` is the bounded whole-table read
`internal/admin/farm_http.go`'s coverage memo now uses in place of the live
join; `FarmStatsStore` no longer declares `FarmCoverage` at all, so the
admin package cannot call it even by mistake.

Unlike `package_symbols`, `PutFarmCoverage` replaces the whole table on
every publish rather than upserting rows: `FarmAxisCoverage` carries no
per-row staleness marker, so an `(os, ecosystem)` axis the Builder stops
observing must disappear rather than answer forever. That means row count
alone cannot answer "has a Builder pass ever published?" — a pass that
legitimately computes zero coverage cells (bootstrap state, or a moment
where every axis is momentarily unobserved) still calls `PutFarmCoverage`
with an empty slice, and `farm_coverage` then holds zero rows exactly as it
would if no pass had ever run. Publication is tracked separately:
`farm_coverage_meta` is a singleton row (the same pattern
`anonymous_analytics_collection` uses), written in the same transaction as
every whole-table replace, holding the one `generated_at` all of that pass's
rows share. `found=false` means the meta row does not exist — no Builder
pass has ever published (a fresh install); the admin panel renders that as
"coverage not yet computed" rather than an empty corpus, and the two states
stay distinguishable no matter how many rows a given pass actually produced.
The admin memo also now exposes two independent timestamps: `at`, when this
process last successfully read the table, and `generatedAt`, when the
Builder pass that computed the current value actually ran — the read itself
is cheap and bounded, so the freshness question that matters is the second
one.

### Request-path query audit (#452)

Every route reachable without the operator's admin cookie was checked for a
computation that grows with corpus size, and two were found and fixed:
`symbolsForPURL` behind `GET /v1/registry/packages/{purl}` (fixed in #460,
the `package_symbols` read model above) and the admin farm panel's coverage
join (fixed in this plan's Task 1, the `farm_coverage` read model above).
Task 2 closed the remaining gap in how those read models are *published*,
not read: `flushSnapshots()`'s batch boundary is now purl-atomic, so a
reader can no longer observe a purl mid-Builder-pass, part old symbols and
part new.

What is left unbounded is left that way on purpose, not missed. This same
audit is what `cmd/csx-server/dbclass.go`'s `longRunningPrefixes` comment
already documents: `/v1/samples` (upload), `/v1/authoring/` (draft
submission and work leases), `/v1/wanted/batches` (bulk ask ingest),
`/admin` (the operator dashboard, which aggregates on the operator's own
behalf), and `/sitemap` (at most one full-corpus rebuild per freshness
window, serving from memory otherwise) are all aggregate-by-design routes,
each routed to `ClassBackground` instead of the interactive class exactly
because their cost is expected to scale with corpus size. Nothing on this
list is a public, unauthenticated, per-request corpus scan — the thing
#452 exists to rule out.

`internal/serverstore/readmodel_scale_pg_test.go` is the load-test evidence
this audit's acceptance criterion asks for: it seeds `package_symbols` to
5,000 purls and `farm_coverage` to 200 synthetic (os, ecosystem) axis pairs
(the plausible upper bound for a cross-product table, not today's real
count), then asserts with `EXPLAIN (ANALYZE, FORMAT JSON)` that
`GetPackageSymbols` reaches its row through an index, never a `Seq Scan` on
`package_symbols`, and separately bounds both read models' wall-clock time
at that scale. `farm_coverage` and its `farm_coverage_meta` singleton are
read whole rather than by key — by design, since the table's whole point is
to answer with every axis at once — so a `Seq Scan` there is the planner's
correct choice, not a regression to guard against; wall clock is the
guard that matters for that table instead.

## Resource governance

Four classes of database work share one small pool, and one loop decides
who gives way when there is not enough of it.

**The classes and their floors** (`internal/serverstore/pool.go`). Every
caller classifies itself, and each class holds a bounded share of the pool,
so the arithmetic guarantees the others a floor. With the shipped policy
(`MaxConns` 12, `ProbeReserve` 1, so `general` = 11):

| class | cap | guaranteed floor | ceiling on one statement |
| --- | --- | --- | --- |
| `probe` (`/healthz`) | 1 reserved | 1, always | 2s |
| `interactive` (pages, public API reads, search) | 6 | 11 − (4 + 2) = 5 | 8s |
| `background` (ingest, migrations, authoring, admin, CLI) | 4 | 11 − (6 + 2) = 3 | none, by design |
| `farm_ingest` (CodeSampleX-Farm's batches, receipts, job queue) | 2 | 11 − (6 + 4) = 1 | 30s |

The caps deliberately add up to more than the pool: the shares overlap so
spare capacity stays usable, and only the floor is reserved.

**The governor** (`cmd/csx-server/governor.go`) is the active half. Every
`defaultGovernorInterval` (5s) it takes a `PoolStats()` snapshot,
differences it against the previous one — so it judges the *window*, never
the process-lifetime totals — reads host CPU steal
(`internal/hostpressure`), and runs one pure function, `decide()`:

- `interactive` refusals (`Busy` / `Attempts`) in that window **≥ 10%** →
  `Reason: "interactive-pool-pressure"`.
- host CPU steal **≥ 20%** → `Reason: "host-cpu-steal"`.
- otherwise → no pause.

Either reason pauses both background producers, and a clean window puts
both back:

- **Builder**: `compatibility.Leader.Pause` writes `builder_lease.paused_until`
  (migration `0046_builder_pause.sql`), which every Builder — standalone
  `cmd/csx-builder` or the in-process one — checks before starting a pass
  and re-checks every 15s while paused. A paused pass is skipped, not
  started and abandoned; a pass already running keeps its own mid-pass
  yield (#445).
- **Farm ingest**: `PG.SetFarmIngestConns(0)` drops `ClassFarmIngest`'s live
  admission ceiling to zero, so Farm's next request gets the
  `ErrPoolBusy` → 503 + `Retry-After` answer CSX-453 already defined for a
  saturated pool. No new error path, and Farm's workers already back off on
  it.

Four properties are load-bearing and each has a test that fails without it:

1. **The pause never touches the lease.** `paused_until` is written with no
   fencing token, by a process (csx-server) that does not hold the lease,
   and `owner`/`fence`/`expires_at` are left alone. A Builder that is paused
   and then dies is still reclaimed on the lease's own TTL, which was
   CSX-451's whole point.
2. **The pause is a deadline, not a latch.** It expires after
   `compatibility.DefaultPauseTTL` (1 minute) unless the governor refreshes
   it, which it does on every paused tick. A governor that dies mid-incident
   therefore releases the Builder by itself, and a csx-server that restarts
   clears any stale pause on its first tick.
3. **The governor cannot stall inside its own tick.** It talks to the
   database over the pool it is reacting to, as `ClassBackground` — no wait
   budget, no statement ceiling, sharing the general gate with the
   interactive reads that saturate during the incident — so each tick's
   control calls run under a deadline of one interval (floored at 1s) and a
   stalled call costs one tick rather than the incident. The Farm lever,
   which is an atomic store that cannot block, is always pulled *before* the
   Builder pause, so a slow pause cannot delay the shedding that slowness is
   evidence for.
4. **The live Farm ceiling can only ever be lower than the configured one.**
   `cap(p.farm)` — the number the other classes' floors are computed
   against — never moves; `SetFarmIngestConns` clamps into
   `[0, FarmIngestConns]`. Farm's floor of 1 becoming 0 is a deliberate,
   reversible, governor-owned state, not a misconfiguration.

`CSX_GOVERNOR_ENABLED=off` is the no-build rollback; the server then behaves
exactly as it did before the governor existed. The operator-facing view of
all of this — what the log lines say, and what each reason means you should
do — is in `docs/operations.md`.

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

**The sitemap follows the corpus by construction.** `/sitemap.xml` is an
index over section shards under `/sitemaps/` (`static`, `packages`,
`samples`), built from the same store queries the pages render from and
cached in-process for a 15-minute freshness window
(`internal/web/sitemap.go`). A new package or sample enters the served map
because the store returns it, and a quarantined sample leaves the same way —
there is no sitemap file to regenerate or deploy. The packages section is
the full records inventory, not a hot-N window, filtered to the ecosystems
the router actually serves; the samples section is every published sample at
the address it declares canonical. `lastmod` is the page's own data change
(a sample's publication date, a package's last snapshot materialization),
never the generation clock, and pages with no honest date carry none. A
section splits into numbered shards below the protocol's 50,000-URL/50MB
file limits, and each rebuild logs advertised-versus-corpus counts (the
divergence ledger `docs/operations.md` reads). Sitemap requests are
background-class in `cmd/csx-server/dbclass.go`: one request per window
rebuilds on a crawler's behalf, and the rest serve from memory.

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
