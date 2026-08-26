# The coverage scheduler, and the dependency closure

*R2C-89. Every number here is measured against production on 2026-08-23
unless it says otherwise.*

## What the queue is made of

`ListAuthoringExpansionCandidates` is one query in two implementations — a Go
walk over the in-memory store and a CTE chain over PostgreSQL — that decides
what the authoring fleet is handed next. It has five sources, and the order
they rank in is the contract:

| Rank | Source | The question it asks |
| --- | --- | --- |
| — | `WANTED` | somebody asked for this by name |
| 0 | `FINDING` | this coordinate has a failure nobody has explained |
| 1 | package-level `EXPANSION` | this release is used on a platform we have never proven it on |
| 2 | symbol `EXPANSION` | this symbol is used and no verified sample calls it |
| 3 | `DEPENDENCY` | a resolved lockfile named this release and nobody has ever reported it |
| 4 | sibling `EXPANSION` | this release exists and no evidence row reaches it |

Real demand first, then the holes evidence points at, then the dependency
closure, then version breadth. Within one rank the most-used coordinate wins,
because authoring should follow what people actually run.

The ordering term that leads all of them is `version_depth`: how many jobs this
exact release has already been offered. Every version earns its first job
before any version earns its second, which is what stops one package with a
long release history filling the window.

## The dependency closure

A resolved lockfile is the only place the network learns that *this* release of
one package pulled *that exact release* of another. `dependency_edge` records
it, one row per (edge, project, day), and the server cannot derive it — an
observation batch carries a single package, so a resolution arrives already
shredded.

A child of such an edge that no batch ever reported on its own is unreachable
by every other source above: each of them starts from an evidence row keyed by
the exact purl. This branch is what turns it into a job.

**The versions are resolved, never guessed.** Nothing here expands a range.
The coordinate handed out is one a machine actually had installed.

**It is canonical.** One work item per (ecosystem, name, version) however many
parents pulled it, scored by the distinct project-days that resolved it. Two
parents wanting the same child is one question.

**It is anchored.** The parent must be a package somebody *listed in their own
manifest* (`evidence_agg.direct`) or one the network has already proven.
Without an anchor the branch walks out of every shadow the network has ever
seen.

### The bounds, and why a cycle cannot spin

| Bound | Value | What it stops |
| --- | --- | --- |
| walk depth | one edge from an anchor | a huge tree arriving in one pass |
| releases per library | `authoringSiblingVersionsPerPackage` (6) | one long release history filling the window |
| coordinates per pass | `authoringDependencyClosureCap` (200) | one ten-thousand-package lockfile taking the queue |
| registry probes per poll | `maxDependencyProbesPerRequest` (4) | pointing this server at npmjs.org several times a minute |

Transitive expansion still happens, one level at a time: a dependency's own
dependencies enter only once that dependency is itself proven or chosen, so
each level costs a verified sample.

A cycle cannot spin because the exit condition is a property of the coordinate,
not of the walk — anything already observed or already proven is not a
candidate. A ↔ B yields nothing while both are observed, and nothing once
either end is answered.

### Publicness

A dependency coordinate is the only work this server hands out that no
publicness gate has necessarily seen. The ingest gate checks every `dependsOn`
child, but only outside trust mode and only inside its own per-request lookup
cap, so an edge can reach the table unconfirmed. `/v1/authoring/work/next`
therefore confirms each new dependency coordinate against its public registry
before offering it, and drops — rather than refuses — the ones it could not ask
about this pass. An unanswered probe is not a verdict: caching it would turn
one bad afternoon at a registry into a permanent exclusion.

The confirmation is not only a filter. `registry.Checker` writes its verdict
through to the `packages` table, so confirming a coordinate is also what
registers it: the release stops being an edge nobody has a row for and becomes
a package the registry endpoints, the version axis and the next scheduling pass
can all see.

### What it produces today, and why that number is zero

Measured against production, 2026-08-23:

| | |
| --- | --- |
| `dependency_edge` rows | 1,497 |
| distinct children | 353 |
| children with a chosen parent | 185 |
| children **nobody has reported on their own** | **0** |
| children with a chosen parent and no proof | 73 |
| coverage holes (PUBLIC, observed, unproven) | 2,048 |

The branch generates nothing right now, and that is the correct reading of the
requirement it implements. The client scanner walks the whole resolved tree and
reports a batch per package, so today every edge child already has its own
evidence row. The branch is a net rather than a firehose: it catches the
children a scan does not reach — a batch truncated at `MaxDependsOnPerBatch`, an
ecosystem whose adapter records edges without enumerating the tree, a child
dropped as UNKNOWN past a request's registry-lookup cap — and stays empty when
nothing is missing.

The 73 observed-but-unproven children are a different backlog. They are already
reachable through the package-level branch; they rank near the bottom of it
because a carried sighting is weighted 1 against a chosen one's 1000. Lifting
them is a ranking change to *that* branch, not a coordinate this one should
duplicate, and it is deliberately not done here.

## The panel

`GET /admin/api/farm` → `backlog` reports the two stocks and the two flows, all
over the same one-hour window as the worker rates beside them:

* `coverageHoles` — PUBLIC releases the network watches people use and has
  never proven.
* `dependencies` — releases only a resolved lockfile names, whole rather than
  capped. A backlog reported at its own cap reads as finished work.
* `handedOutInWindow` / `handedOutByKind` — what the scheduler actually
  produced, read from `authoring_assignments.claimed_at` so a restart does not
  reset it.
* `firstProvenInWindow` — coordinates that earned their **first** passing
  receipt. Any-pass would count a coordinate re-proven on another platform,
  which is real work that takes nothing off the stock printed beside it.

The dependency figure comes from the same `dependency_open` CTE the scheduler
hands work out of (`authoringCoverageCTE`), not from a second definition
written to look like it. A backlog counted from a different predicate than the
queue would sit still while the fleet drains work, or reach zero while jobs are
still going out — and the two halves of this panel have drifted apart before.

## The grid, counted at the grain a reader sees it

*Measured against production on 2026-08-24.*

Every stock above counts a RELEASE: a purl either has a passing sample or it
does not. The completeness census instead spreads every known SYMBOL across
every VERSION with a PUBLIC snapshot, so one release with forty symbols is
forty cells, and thirty-nine of them can be empty while the release counts as
proven. This is the **unbounded corpus cross-product**, not the package page's
bounded browse window (six versions, ten symbols loaded per version, then
bounded rendered axes). Display limits must not remove canonical coordinates
from the completion denominator. At release grain production reads 99%
covered. At corpus-cell grain it reads this:

| | |
| --- | --- |
| PUBLIC corpus symbol x version cells | 9,409 |
| Observation >= 1 | **1,295 (13.8%)** |
| our sample passed, nobody seen using it | **3,013 (32.0%)** |
| nothing recorded at all | **5,101 (54.2%)** |
| package pages showing both dash forms at once | 158 |

Both numbers are true and neither substitutes for the other, so
`GET /admin/api/farm` -> `backlog.matrixCells` prints the second beside the
first.

### The two absence states

When a coordinate is inside the current browse window, these two corpus states
map to visually different dashes. The difference decides what work would
answer it.

* **`≡ —`, and the cell is a LINK.** We wrote a sample, it passed here, and no
  project build has been attributed to the coordinate. The mark is our run;
  the dash sits where the usage rate would go. `verifiedNoObservation`.
* **A plain, unlinked `—`.** No evidence row, no sample. The cell exists only
  because the symbol was recorded at some OTHER release of the package, so the
  grid draws the pair. `unmeasured`.

A census that pooled them would report the same figure for a coordinate this
network has already executed and one nothing has ever touched.

`packagesShowingBothDashes` is a legacy JSON field name. It counts packages
whose full corpus contains both states; it does not claim both coordinates
survive the UI's bounded browse window simultaneously. R2C-89's two live
reproductions (`golang/github.com/jackc/pgx/v5` and `npm/semver`) are carried as
a fixture in `matrixcells_test.go`, along with a regression proving the census
intentionally retains versions and symbols beyond the display caps.

### What it does not do

It hands out no work. The scheduler's five sources are unchanged.

Two of the three states cannot be closed by scheduling alone, and pretending
otherwise in code would be the expensive mistake:

* `verifiedNoObservation` cannot be closed by this network at all. An
  observation is a real project build reported by a `csx` install; a farm run
  is a verification. Producing another sample for a coordinate that already
  has one is duplicate work the queue is built to refuse, so no amount of
  authoring moves this number. Only somebody out there building that
  coordinate does.
* `unmeasured` contains cells that cannot exist: the grid draws every symbol
  against every release, and a symbol introduced in 7.x has a cell at 5.7.2
  where the API is simply absent. The network stores nothing that says which,
  so a source that handed the cross product out would spend the fleet on jobs
  that must fail and teach the quarantine ledger that the coordinates were at
  fault.

Which of the three is the target, and what an unauthorable cell is called, is
a product question rather than a scheduling one. It is recorded on R2C-89. The
census is the instrument that will judge the answer, and it is worth having
before the answer as well: it is the only number on the panel that moves the
way a reader's page does.

### Cost

One statement, one row, on an admin endpoint nobody polls. Against production
(9,455 stored documents): 930 ms, of which 162 ms is the work -- the rest is
PostgreSQL JIT-compiling a plan for a query that runs once. Written as a UNION
of scalar aggregates it was 1.37 s and 83 compiled functions. If it ever moves
somewhere hot it belongs in the aggregation pipeline that already materialises
on `CSX_SNAPSHOT_INTERVAL`, not in a request handler.

## Two stores, one behaviour

`(*Fake).dependencyOpen` and the `dependency_open` CTE are the two halves of
one rule, and the repo's history records what happens when halves of this query
drift: a test proves an assignment the server would never make.
`TestIntegrationDependencyClosureParity` replays scripted scenarios through both
stores and compares the candidate order row for row.

Two things that parity check cannot cover, and why:

* **`last_seen` as an ordering term.** PostgreSQL stamps it with `now()` from
  inside the query; the Fake reads a clock the test has to pin. On a Windows
  timer the Fake's writes all land in one tick while PostgreSQL's do not, so
  every scenario gives its packages distinct observation counts and the order
  is settled before recency is ever consulted. (The Fake did not populate
  `WantedRow.LastSeen` at all until this change, so its own recency term was
  dead code while PostgreSQL's was live — that is fixed.)
* **Schema constraints.** The Fake has none. `0013_authoring_expansion.sql`
  wrote the assignment `kind` vocabulary as an inline CHECK, so a `DEPENDENCY`
  claim failed the INSERT rather than a filter — every Fake test passed while
  PostgreSQL refused every job the branch produced.
  `0022_dependency_work_kind.sql` widens it, and
  `TestIntegrationDependencyWorkCanActuallyBeClaimed` is the test that only
  fails against a real database.

## Cost

The candidate query is recomputed on every `/work/next` poll. Measured on
`postgres:17-alpine` with a synthetic corpus 120× production's edge count
(180,003 edges, 56,002 evidence rows, 9,002 open dependency coordinates):

| | median of 3 |
| --- | --- |
| before this branch | 1.70 s |
| with this branch | 2.57 s |

At production's actual volume the branch reads 1,497 rows and the cost is not
measurable against the noise. The number worth watching is `dependency_edge`:
if it grows toward six figures, this query belongs in the aggregation pipeline
that already materialises snapshots on `CSX_SNAPSHOT_INTERVAL`, not in a
request handler. Two rewrites were tried against the synthetic corpus — hash
anti-joins in place of per-row index lookups, and aggregating before
materialising — and neither beat the plain form reliably, so the plain form
stands.
