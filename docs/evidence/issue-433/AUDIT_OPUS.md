# Audit (Opus): 10k corpus trust + site/admin performance — issue #433

Independent audit. Worktree `csx-433-opus-audit` at `8e4f4fc`. Production read
at `CSX_BUILD_VERSION=v0.1.189` / `CSX_VERSION=8e822f11766b0ebb23a0b756c85f6be5e0d07247`.
Measurement window **2026-09-15 14:33Z – 15:25Z**; `pg_stat_statements` window
**2026-09-09 05:49:06Z → 2026-09-15 15:10Z** (6.4 days, never reset since).
Every production touch was a `SELECT`, an `EXPLAIN`, a log read, or an HTTP `GET`.
Nothing was written, migrated, restarted or deployed by this audit.

Reproduction commands for every number: [AUDIT_OPUS_COMMANDS.md](AUDIT_OPUS_COMMANDS.md).
Machine-readable candidates: [AUDIT_OPUS_DATA.json](AUDIT_OPUS_DATA.json).

## Population, stated once

| population | count | basis |
| --- | --- | --- |
| samples, all | 8,346 | `samples` |
| samples, live (not quarantined) | **7,070** | `NOT quarantined` |
| samples, quarantined | 1,276 | 983 dup-coordinate + 244 dup-template-goal + 45 drafts + 4 corrections |
| live `CROSS_PASS` | 6,621 | the label this audit tests |
| receipts | 10,965 (10,467 PASS / 272 FAIL / 227 SKIPPED) | `receipts` |
| packages rows / distinct (eco,name) | 6,330 / **3,415** | `packages` |
| `evidence_agg` rows | 318,688 | |
| `failure_clusters` rows | 236,167 | derived |
| `compatibility_snapshots` rows | 21,692 | derived |
| sitemap-advertised URLs | 10,205 (3,120 package + 7,070 sample + 15 static) | server log 15:12:29Z |

The issue says "~10k corpus". The reachable published corpus is **7,070 live
samples**; 10,205 is the advertised *URL* count, and 8,346 the row count
including quarantine. Every corpus percentage below is over 7,070 unless the
row says otherwise.

**Exhaustiveness.** Every corpus claim is a full-table scan of production, not
a sample. The only sampled measurement is the public 503 rate (§P0-1), which is
a uniform random draw from the 3,415 distinct package coordinates
(`ORDER BY md5(ecosystem||name) LIMIT 50`), one request each, sequential.
Sampling limits are stated where they exist.

**Confound, disclosed.** An external deploy held `/opt/codesamplex/.deploy-lock`
from 15:10:04Z and recreated `codesamplex-server-1` + `codesamplex-caddy-1` at
**15:17:40Z**, having already recreated them at **14:31:59Z** — two
recreations of the *same* image (`created 2026-09-15T09:34:52Z`) inside 46
minutes, over SSH from `52.242.243.202` and `104.28.211.27`. Zero-byte HTTP 502
responses observed 15:13–15:20Z fall inside that teardown and are **excluded**
from every finding. The 503 measurement in §P0-1 was taken at 15:03–15:06Z
against a container `Up (healthy)` since 14:31:59Z, and re-confirmed at 15:25Z
against the freshly recreated one.

---

# P0

## P0-1 — 94% of public package pages serve HTTP 503

**Measured.** Uniform random sample, n=50 of 3,415 distinct package
coordinates, one `GET https://codesamplex.dev/{eco}/{name}` each, sequential,
15:03–15:06Z:

```
      3 200
     47 503
```

**47/50 = 94.0%** (Wilson 95% CI ≈ 83.5–98.0%). Re-measured at 15:25Z on the
first 20 of the same draw, against a server container 4 minutes old and
reporting `healthy`: **19/20 = 95.0%**. The condition survives a restart.

Not cluster-size dependent: a targeted A/B of the 15 highest-`failure_clusters`
packages and 15 of the lowest gave 12/15 and 13/15 503s respectively — the page
fails for `github.com/aryann/difflib` (3 clusters) as readily as for
`golang.org/x/sys` (9,352). `/npm/react` returned 503 on 6 of 6 consecutive
requests.

The response is a rendered error page (`<title>Data unavailable — CodeSampleX</title>`,
`Retry-After: 2`, ~10 KB), served in ~600–1,000 ms TTFB — a fast rejection, not
a timeout at the edge. Meanwhile `/`, `/findings`, `/dependencies`, `/version`
and `/robots.txt` all returned 200 in the same minutes, so this is the package
page path specifically.

### Mechanism (a): the interactive pool is two connections with a 250 ms wait

Production environment on `codesamplex-server-1`:

```
CSX_DB_MAX_CONNS=          -> defaultMaxConns = 8      (internal/serverstore/pool.go:144)
CSX_DB_READ_CONNS=2        -> InteractiveConns = 2     (default in code is 6)
CSX_DB_READ_WAIT=250ms     -> ReadWait = 250ms         (default in code is 3s)
CSX_DB_PROBE_RESERVE=      -> ProbeReserve = 1
                              BackgroundConns = 4
```

So page rendering has **2** of 8 connections and abandons after **250 ms**,
while background/builder work may hold **4**. The server log names the
consequence directly, one line per rejected page:

```
14:40:27 db pressure path=/npm/cfb        class=interactive cause=pool_busy waited=251ms
14:40:39 db pressure path=/npm/react-is   class=interactive cause=pool_busy waited=308ms
14:44:26 db pressure path=/npm/@babel/plugin-transform-computed-properties
                                          class=interactive cause=pool_busy waited=306ms
```

`pool_busy_total` climbed **102 → 316 between 14:44Z and 15:08Z** — 214
rejections in 24 minutes, ~9/min, sustained.

### Mechanism (b): one sub-read failing kills the whole page

`internal/web/explorer.go:1049-1052` makes `PackageVersions` a required read
and converts any error — `ErrPoolBusy` included — into a page-wide 503:

```go
versions, versionsErr := s.d.Store.PackageVersions(r.Context(), eco, name)
if versionsErr != nil {
        s.unavailable(w, r, lang)   // -> 503 for the entire page
        return
}
```

`buildCubeView` (explorer.go:1110) and, on version pages, `PackageSymbols`,
`loadVersionCubeFacts` and `versionSamples` (explorer.go:1264-1288) do the
same. There are **21 `s.unavailable(...)` call sites** — 15 in
`explorer.go`, 2 each in `dependencies.go` and `failureissuepage.go`, 1 each in
`gaps.go` and `samples.go`. The page is fail-closed even when the sitemap, the
snapshot, the samples and the cluster cache all hold serveable data. `loadClusters` alone has a stale-cache
fallback; nothing else does.

### Mechanism (c): the cluster read exceeds the 8 s statement ceiling

`EXPLAIN (ANALYZE, BUFFERS)` on production of the exact statement at
`internal/serverstore/pg.go:3090-3109`, run for `/golang/golang.org/x/sys`:

```
Sort (actual time=76167.856..76168.638 rows=9352 loops=1)
  Buffers: shared hit=396 read=6261
  I/O Timings: shared read=68749.053
  ->  Bitmap Heap Scan on failure_clusters (actual time=78.588..74546.600 rows=9352)
        Heap Blocks: exact=6627
        ->  Bitmap Index Scan on failure_clusters_pkg_idx (rows=9843)
Execution Time: 76177.169 ms
```

**76.18 s**, of which **68.75 s is disk read time**, at a **5.9% buffer hit
rate** (396 hit / 6,261 read). `ReadTimeout` for `ClassInteractive` is 8 s
(`DefaultPoolPolicy`; `CSX_DB_READ_TIMEOUT` is unset), so this query is always
cancelled and the page always 503s — and while it burns its 8 s it holds **one
of the two** interactive connections, which is what turns mechanism (a) from
occasional into near-total.

**Affected surface.** 3,120 package URLs are advertised in
`/sitemaps/packages-1.xml`. At the measured rate ~2,930 of them answer 503 to a
crawler. No 404 and no removal is involved: the error page is `NoIndex`-marked
(`internal/web/web.go:1077-1084`), so a crawler is told nothing except "come
back in 2 seconds", indefinitely.

**Expected fix (direction, not implemented).** Push the ecosystem predicate and
a `LIMIT` into `listFailureClusters` instead of filtering in Go
(`cmd/csx-server/webstore.go:2176-2180` discards everything past
`maxClustersToPage = 500` *after* reading all 9,352 rows); render the page from
whatever reads succeeded rather than 503-ing on the first failure; and raise
`CSX_DB_READ_CONNS` / `CSX_DB_READ_WAIT` above a builder that currently never
yields (§P0-2).

## P0-2 — the compatibility builder never finishes between passes, so background work owns the pool permanently

`CSX_SNAPSHOT_INTERVAL=5m`. Measured pass duration, from the server's own
ledger line:

```
14:43:49 builder pass complete full=false targets=2436 packages=58 clusters=15702
         cluster_read=978ms cluster_calculate=16.03s cluster_write=2m22.09s
         slowest_package=golang/golang.org/x/sys slowest_write=1m6.23s
         slowest_clusters=9352 total=8m36.41s
```

**8m36s for an *incremental* pass over 58 packages**, against a 5-minute
interval. The next pass started **14:48:50Z** and had still not completed at
**15:09Z (>20 min)**. Two starts were observed without an intervening
completion (`14:32:04` and `14:35:13`, both `since=13:45:31Z`, one completion at
`14:43:49`).

A pass that outlives its interval is not a pass, it is a resident process. For
the whole observation window the builder held background connections
continuously, which is why §P0-1 mechanism (a) fires ~9 times a minute rather
than at a deploy boundary. One package — `golang.org/x/sys`, 9,352 clusters —
accounts for **1m6s of the 2m22s cluster-write phase** on its own.

The same ledger shows an N+1 inside the pass: `phase=target_evidence
logical_calls=721 items=12433` — 721 separate round trips to fetch evidence for
721 snapshot targets, 16.39 s; against `phase=snapshot_write logical_calls=12
items=721`, which is the same cardinality of work correctly batched into 12
calls. The evidence read was never batched the way the write was.

## P0-3 — the host has 76–80% of its CPU stolen, and no application tuning reaches it

```
%Cpu(s): 11.5 us, 11.5 sy, 0.0 ni, 0.0 id, 0.0 wa, 0.0 hi, 0.0 si, 76.9 st
vmstat st column, 6 consecutive 5 s samples: 80 80 80 80 80 80
iostat avg-cpu since boot (3d 9h): %steal 44.79, %idle 32.19
load average: 7.02 -> 11.86 -> 13.08 on nproc=2
```

This is **hypervisor steal, not a container limit**: `cpu.max` is unset for all
three containers (`CpuQuota=0`, `NanoCpus=0`) and the root cgroup reports
`nr_throttled 0`, `throttled_usec 0`. The instance is a burstable Lightsail host
(`Xeon Platinum 8259CL`, 2 vCPU, 1,906 MB); `%idle 0.00` with a run queue of
14–30 means the processes are simply not being scheduled.

Consequence, measured rather than assumed: `nvme0n1` shows `r_await 4.40 ms` and
`%util 31.79` — the *device* is not saturated — yet the §P0-1 EXPLAIN measured
**11.0 ms per 8 KB block** (68,749 ms / 6,261 blocks). The I/O wait is
scheduling delay, not storage. This is the finding that bounds every other
performance finding: the DB-side items below are real and worth fixing, but
they are being amplified by a factor the application cannot configure away.

Two of the workloads on this box are jobs `goal.md` says do not belong on the
server at all (the aggregation builder, §P0-2) or are documented as needing four
minutes and both cores (§P1-8).

## P0-4 — the corpus's top trust label is unearned on 86.3% of the samples that carry it

`CROSS_PASS` renders publicly as `L4_CROSS_PASS`
(`internal/web/explorer.go:2146-2147`, `levelBadge`). The rule the code
documents and defends at length for granting it is `sampleStatusFromReceipts`
(`internal/httpapi/verifications.go:592-650`):

> `PUBLISHED → CROSS_PASS` — first contract-PASS receipt **from a peer ≠ origin**
> … "Independence is the single thing a cross pass asserts that a publisher
> cannot manufacture alone".

Exhaustive test of that rule against every live `CROSS_PASS` row:

| | count | share |
| --- | --- | --- |
| live `CROSS_PASS` | 6,621 | 100% |
| **PASS receipts from < 2 distinct peers → rule does not hold** | **5,713** | **86.3%** |
| PASS receipts from ≥ 2 distinct peers → rule holds | 908 | 13.7% |
| …of the 5,713, samples with **exactly one receipt in total** | **5,117** | 77.3% of all CROSS_PASS |
| …with every receipt from one peer | 5,646 | 85.3% |
| live `STABLE` failing its own rule (≥3 pass peers) | **0** / 355 | 0% |

A single receipt cannot satisfy `peer ≠ origin` under any reading, so 5,117 of
these labels were not written by that function at all. They were written by a
**second, undocumented promotion path**, `internal/serverstore/pg.go:1871-1878`:

```sql
UPDATE samples
   SET status='CROSS_PASS', quarantined=false, quarantine_reason=NULL, updated_at=now()
 WHERE sample_id=$1 AND status='DRAFT' AND quarantined
   AND EXISTS(SELECT 1 FROM verification_jobs WHERE id=$2 AND reason='cross')
   AND EXISTS(SELECT 1 FROM authoring_drafts d WHERE d.sample_id=samples.sample_id)
```

There is no peer comparison in it. Its justification is the comment above it —
"A designated sample author already proved LOCAL_PASS before upload. The claimed
verifier is the independent confirmation" — and the Farm README makes the same
argument the reason the corpus means anything:

> "publication happens server-side once an independent verification worker
> returns a PASS receipt. That separation is the point — it is why a published
> sample means something."

**The separation is not in the corpus.** The author's `LOCAL_PASS` is never
persisted as a receipt, so the store holds evidence from exactly one of the two
parties; there is no check that the verifying peer differs from the authoring
session's identity; and the README itself notes both halves run on farm nodes,
so "the farm alone can carry a sample from wanted to published".

**And it is live, not legacy.** Weekly, over live `CROSS_PASS`:

| week | CROSS_PASS | rule fails | % |
| --- | --- | --- | --- |
| 2026-08-10 | 335 | 0 | 0.0% |
| 2026-08-17 | 2,011 | 1,440 | 71.6% |
| 2026-08-24 | 1,962 | 1,960 | 99.9% |
| 2026-08-31 | 1,849 | 1,849 | 100.0% |
| 2026-09-07 | 257 | 257 | 100.0% |
| **2026-09-14** | **207** | **207** | **100.0%** |

Since 2026-08-24, **every** newly published `CROSS_PASS` sample carries the
label on one peer's signature. `internal/mcp/tools.go:989-992` already states
"half of production's CROSS_PASS labels do not hold under the rule that grants
them" and suppresses the status in MCP output for exactly this reason. The
exhaustive number is **86.3%, not half**, and the web badge does not suppress
it.

Supporting facts, exhaustive:

- **5 verifier peer keys exist in the whole corpus.** One
  (`ed25519:c1973797be207ac4`) signed 6,373 of 10,467 PASS receipts, covering
  5,921 of 8,301 verified samples (71.3%).
- **100% of the 10,467 PASS receipts are `linux` / `x64`.** Zero windows, zero
  darwin, zero arm64. All 7,070 live samples have passed only on linux/x64.
  57 distinct `env_hash` values exist, all inside that one os/arch.
- **Zero `MATRIX_PASS` samples exist**, although 3,251 `matrix` verification
  jobs completed. `spansContextBoundary` needs ≥2 values of os, runtime major or
  browser family; with one OS and one arch, 3,251 matrix jobs produced no status
  upgrade at all.

**Expected fix (direction).** Either persist the author's `LOCAL_PASS` as a
receipt so both parties are in the store and one rule governs, or stop
publishing `L4_CROSS_PASS` on the badge until `sampleStatusFromReceipts` grants
it — the MCP surface already took the second option. Recomputing status downward
is a separate decision (status is monotonic by design), but the badge is a read
and can be gated on the receipts without touching history.

---

# P1

## P1-5 — `failure_clusters` per-package read is the single largest DB consumer: 26.6 h in 6.4 days

`pg_stat_statements`, queryid `-8919612395518868614`
(`internal/serverstore/pg.go:3090`, via `ListFailureClusters`):

| | |
| --- | --- |
| total exec time | **95,648 s = 26.6 h** |
| calls | 337,310 |
| mean / max | 283.6 ms / 8,000 ms |
| rows returned | 55,549,740 (164.7 per call) |
| blocks read from disk | 30,008,237 |
| **block read time** | **82,556 s (86.3% of its own time)** |

26.6 h of DB time inside a 153 h window is 17.4% of wall clock for one
statement, on a host with a fraction of one effective core (§P0-3). The
`max_exec_time` of exactly 8,000 ms is the interactive statement ceiling cutting
it off.

Three compounding causes, all in code:

1. **No `LIMIT` and no ecosystem predicate.** The SQL filters on `package_name`
   only; `webstore.go:2176-2180` then drops rows whose `Ecosystem` differs and
   everything past `maxClustersToPage = 500` — in Go, after the rows have
   crossed the wire. For a name that exists in several ecosystems, every
   ecosystem's rows are read to serve one.
2. **Every wide JSONB column is cast to `text` for every row**: `env_summary`,
   `hypotheses`, `versions`, `env_variants`, `evidence_breakdown`,
   `outer_commands`. That is the TOAST traffic behind 30 M block reads.
3. **Cluster identity is unbounded per package.** Top of the distribution:

   | package | clusters | JSONB bytes |
   | --- | --- | --- |
   | `golang.org/x/sys` | **9,352** | 5,333 kB |
   | `electron` | 4,978 | 3,645 kB |
   | `github.com/jackc/pgx/v5` | 3,239 | 1,836 kB |
   | `golang.org/x/net` | 3,172 | 2,480 kB |
   | `vitest` | 2,269 | 1,665 kB |

   236,167 cluster rows for 3,415 package coordinates, derived from 318,688
   `evidence_agg` rows. `docs/schema.md` states that package/version scope is
   kept beside the fingerprint precisely to avoid "unbounded cluster
   identities"; at 9,352 identities for one package that bound is not holding.

`FailureIssueClusters` (`webstore.go:2214`) runs the same unbounded read with
**no cache at all**, deliberately, for `?issue=` URLs.

## P1-6 — `builder_purl_coord`, an expression index over a non-inlinable SQL function, costs 18.5 h of DB time

queryid `5126368844612351286` — the body of `builder_purl_coord(raw TEXT)`
(`internal/serverstore/pg_builder_prestage.go:13-30`):

| | |
| --- | --- |
| total exec time | **66,210 s = 18.4 h** (66,714 s at 15:10Z) |
| calls | **10,416,825** (10,444,795 at 15:10Z) |
| mean | 6.36 ms, returning 1 row |
| current rate | ~700 calls/min |

The function percent-decodes a purl by exploding the name into **one relational
row per character** (`regexp_matches(..., 'g') WITH ORDINALITY`), hex-encoding
each, `string_agg`-ing them back in ordinal order, then `decode` /
`convert_from`. Because it is a multi-CTE SQL function it cannot be inlined, so
each evaluation is a separate query execution — which is why it appears in
`pg_stat_statements` with a 10.4-million call count at all.

It is the leading expression of two indexes
(`0036_builder_projections.sql:50-53`):

```sql
CREATE INDEX evidence_agg_builder_coord_idx
  ON evidence_agg(builder_purl_coord(purl), purl, symbol);
CREATE INDEX snapshots_builder_coord_idx
  ON compatibility_snapshots(builder_purl_coord(purl), purl, symbol);
```

The reads do use the index — verified by `EXPLAIN`: `Index Only Scan using
evidence_agg_builder_coord_idx` — so the 10.4 M calls are **index maintenance on
the write path**, over tables taking 2,334,540 snapshot upserts and 804,718
evidence writes in the window (§P1-7). The same EXPLAIN also shows that
index-only scan doing `Heap Fetches: 5433` and 4.2 s of I/O for 574 result rows,
because the visibility map is never clean on a table updated this heavily.

**Expected fix (direction).** The coordinate is a pure function of a string the
server already holds in Go; compute it there into a stored column and index the
column. Failing that, a single-expression (inlinable) SQL or plpgsql
implementation removes ~18 h of DB CPU per week without changing a row.

## P1-7 — the snapshot upsert has no no-op guard; the cluster upsert has one but pays 11.5 h to use it

`internal/serverstore/pg.go:749-752`:

```sql
INSERT INTO compatibility_snapshots(purl, symbol, snapshot, generated_at)
VALUES($1,$2,$3,now())
ON CONFLICT (purl, symbol) DO UPDATE SET
  snapshot = EXCLUDED.snapshot, generated_at = now()
```

Unconditional. **2,334,540 calls / 14,887 s** in the window for **21,692
distinct rows** — 107 rewrites per row in 6.4 days. Lifetime `n_tup_upd` on that
table is **11,115,178 for 21,692 rows** (512 per row). Each rewrite is a new row
version on a 112 MB JSONB table: dead tuples, WAL, TOAST churn, and one
`builder_purl_coord` evaluation (§P1-6). A
`WHERE compatibility_snapshots.snapshot IS DISTINCT FROM EXCLUDED.snapshot`
would remove most of it.

`upsertFailureClusterSQL` (`pg.go:2939-2990`) **does** carry a 21-column
`IS DISTINCT FROM` guard, and it works — 20,035,511 upsert calls produced only
84,580 row changes. But the guard runs *after* the conflict is detected, so
every one of those 20 M calls still reads the existing wide row to compare it:
**41,479 s = 11.5 h of exec time and 22,999 s of block read time across 11.5 M
blocks**, to decide not to write. Comparing a digest column, or comparing in Go
against the rows the builder just read, converts 11.5 h into nothing.

## P1-8 — the admin farm aggregate times out on every poll

The admin panel polls `/admin/api/farm` every 60 s. Every poll in the
observation window failed:

```
14:34:09 db pressure path=/admin/api/farm class=background cause=query_timeout waited=7ms
14:39:29 ... cause=query_timeout waited=2.194s
14:40:29 ... cause=query_timeout waited=6.58s
14:41:29 ... cause=query_timeout waited=88ms
14:42:29 ... cause=query_timeout waited=1.041s
14:43:29 ... cause=query_timeout waited=796ms
14:44:29 ... cause=query_timeout waited=4.592s
```

`query_timeout_total` went **14 → 36** between 14:39Z and 15:08Z — one per
minute, matching the poll exactly. `farmAggregateTimeout` is 25 s
(`internal/serverstore/farmbacklog_pg.go:15`) and the queries behind it do not
fit:

| queryid | calls | mean | max | source |
| --- | --- | --- | --- | --- |
| `-1838375435808251149` | 192 | **183.4 s** | **433.0 s** | `authoringCoverageCTE` family |
| `5754581964168977175` | 2,124 | 10.4 s | 22.9 s | same |
| `-8060285611411695903` | 1,059 | 10.1 s | 22.8 s | `farmBacklogStocksSQL` |

The first alone is **35,220 s = 9.8 h** of DB time. This is not a surprise to
the code: `internal/serverstore/authoring_pg.go:277-281` documents "the parallel
plan takes both [cores] for the ~4 minutes the read needs on production", and
`authoringExpansionUnhurriedStatementTimeout` is **8 minutes**. So a documented
four-minute, two-core read runs on the same 2-vCPU box that serves the website,
and the operator's own dashboard is the surface that can never load.

Related background failures in the same window: `preload wanted snapshot:
ERROR: canceling statement due to statement timeout (SQLSTATE 57014)` (×3), and
`cause=deferred_refused` appearing for the first time
(`deferred_refused_total` 4 → 11).

## P1-9 — memory is the reason nothing stays cached

| | |
| --- | --- |
| host RAM | 1,906 MB (204 MB free, 162 MB swapped out, swap in use) |
| database size | **1,430 MB** |
| `codesamplex-db-1` memory limit | **640 MB** |
| `codesamplex-server-1` memory limit | 768 MB |
| `codesamplex-caddy-1` memory limit | **none** |
| `shared_buffers` | 256 MB |
| `effective_cache_size` | 768 MB |
| `work_mem` × `max_connections` | 16 MB × 40 = 640 MB worst case |

`evidence_agg` (462 MB) and `failure_clusters` (390 MB) are 852 MB of the
1,430 MB, and they are the two tables that miss:

| table | heap_blks_read | hit % |
| --- | --- | --- |
| `evidence_agg` | 566,285,078 | **64.38** |
| `failure_clusters` | 176,996,768 | **76.15** |
| everything else, together | 42,301,321 | ≥96 |

Those two account for **94.6% of all 785 M lifetime block reads**. The §P0-1
page query measured a 5.9% hit rate. A 1,430 MB working set behind a 640 MB
container limit and 256 MB of shared buffers cannot be cached, and
`random_page_cost=4` then pushes the planner toward sequential scans of exactly
those tables. Sequential-scan tuple volume, lifetime:

| table | seq_scan | seq_tup_read | rows/scan |
| --- | --- | --- | --- |
| `sample_packages` | 3,783,890 | **25,059,625,137** | 6,622 |
| `evidence_agg` | 255,335 | 9,399,200,043 | 36,811 |
| `samples` | 2,576,310 | 7,555,875,816 | 2,932 |
| `receipts` | 1,352,714 | 6,950,141,420 | 5,137 |
| `wanted` | 3,278,858 | 3,709,956,993 | 1,131 |
| `dependency_edge` | 123,698 | 3,369,869,357 | 27,242 |

25 billion tuples read from an 8,676-row table is a nested loop whose inner side
is a seq scan, repeated — the SQL form of the N+1 in §P0-2.

Three more aggregate shapes with no bound:

- `SELECT purl, symbol FROM compatibility_snapshots ORDER BY purl, symbol` —
  5,002 calls, **103,808,189 rows** (20,753 per call), 7,352 s.
- `SELECT purl, SUM(observation_count) FROM evidence_agg GROUP BY purl ORDER BY
  n DESC LIMIT $1` — 628 calls, mean **7.6 s**, 15.3 M blocks read. A full
  aggregate over the 462 MB table to produce a top-N.
- `SELECT (SELECT COUNT(DISTINCT bucket) FROM evidence_dedup …)` — 150 calls,
  mean **21.3 s**, max **101.9 s**, plus 6 s of JIT generation time. `jit=on`
  globally on a CPU-starved 2-vCPU box; `beginFarmAggregate` already disables
  JIT per-transaction for one query after measuring "162 ms of execution behind
  roughly 770 ms of JIT compilation", and that lesson has not been applied
  globally.

## P1-10 — the 94% failure rate on the main public surface is not logged anywhere

`deploy/caddy/Caddyfile:46` `log_skip @skipAPIMetrics` restricts the safe access
log to `/v1/*` routes, tagged with a fixed `csx_route` label. Across the whole
retained archive (**429,084 lines, 9 files, 2026-09-11 → 2026-09-15**) there is
no web-page route at all. The operator can see this:

| route | requests | 200 | 5xx | 5xx% | notable |
| --- | --- | --- | --- | --- | --- |
| `shards` | 360,232 | 121,740 | 11,486 | 3.19% | 219,541×304, **6,828×429** |
| `verification_jobs` | 46,419 | 46,223 | 192 | 0.41% | |
| `samples` | 4,938 | 2,979 | 84 | 1.90% | 1,218×404, 647×0 |
| `authoring_work` | 3,886 | 2,905 | **659** | **16.96%** | 644×503 |
| `evidence` | 2,571 | — | 161 | 6.26% | 1,909×202 |
| `search_hit` | 1,847 | — | **282** | **15.27%** | **271×500** |
| `peers` | 466 | 350 | 99 | **21.24%** | 96×500 |
| `registry` | 38 | 7 | 19 | **50.00%** | |
| `stats` | 1,069 | 933 | 122 | 11.41% | 104×503 |

…and cannot see that 94% of package pages are failing. Two consequences beyond
observability: `search_hit` 500s at 15.27% and 14 `csx: anonymous analytics
write unavailable (activity undercounted)` messages mean **the demand signal
this corpus is supposed to be targeted with is itself being dropped**; and
`authoring_work` failing 1 in 6 is the Farm being unable to fetch work from the
site (§P1-11).

**Expected fix (direction).** Log status and a route class for web pages too. A
failure rate this large should not require an auditor with an SSH key to
discover.

## P1-11 — corpus growth has collapsed 9×, while the backlog it should consume is untouched

Live samples created, by week:

| week | live samples |
| --- | --- |
| 2026-08-10 | 600 |
| 2026-08-17 | 2,195 |
| 2026-08-24 | 1,962 |
| 2026-08-31 | 1,849 |
| 2026-09-07 | **257** |
| 2026-09-14 | **207** |

An 89% drop from the 2026-08-17 peak (2,195 → 207). Meanwhile:

- **1,775 of 3,135 observed packages (56.6%) have zero live samples** (§P1-12).
- **128,590 `failure_clusters` rows are flagged `diagnostic_candidate`** — over
  half of all 236,167 — and nothing is consuming them.
- **`matrix` verification stopped entirely on 2026-09-09**; the newest matrix
  job is 6 days old, and no matrix job has ever produced a status upgrade
  (§P0-4).
- The authoring poll serves nothing: `authoring poll session=… wanted=200/79
  expansion=0/0 offeredSample=58 offeredEvidence=0 offeredDependency=0
  served=NO_WORK snapshotAge=6m12s partial=true`. `partial=true` is the snapshot
  being incomplete because the read behind it times out (§P1-8).
- 6 `cross` verification jobs are `open`, 5 created 2026-09-08 — seven days
  unclaimed. Those samples cannot be published.
- 12 authoring drafts were "stranded" at the 14:32Z boot
  (`csx-server: requeued 12 stranded authoring drafts`).

The author-side cause is an open Farm incident (issue 27, documented in
`CodeSampleX-Farm/docs/recovery-2026-09-14.md`: author lease starvation, a
capacity controller writing configuration it did not apply, and a PATH bypass of
the bounded upload adapter). The corpus-side consequence quantified here —
production has effectively stopped while the identified backlog is 1,775
packages and 128,590 candidates — is not recorded on either side.

There is a loop worth naming: the site's pool starvation makes `authoring_work`
fail 16.96% of the time and keeps the wanted snapshot `partial`, which starves
the Farm; the Farm's output drives the builder passes that never finish, which
starve the site.

## P1-12 — coverage is 85.5% of observed demand, and the 14.5% gap is concentrated and nameable

Demand proxy: `evidence_agg.unique_project_buckets` summed per package — the
count of distinct anonymous real projects observed using it. (`wanted.asks` tops
out at 22 across the entire board and is too small to rank with; that is itself
worth knowing, because the coverage scheduler's most explicit demand signal
carries almost no information.)

**Global: 379,673 project buckets, 324,615 covered by ≥1 live sample = 85.5%.**

| ecosystem | live samples | covered pkgs | observed pkgs | uncovered observed | proj buckets | uncovered buckets | demand covered |
| --- | --- | --- | --- | --- | --- | --- | --- |
| npm | 3,955 | 936 | 2,367 | **1,431** | 293,893 | **52,111** | 82.3% |
| golang | 2,162 | 208 | 322 | 114 | 70,379 | 1,383 | 98.0% |
| pypi | 516 | 94 | 99 | 5 | 10,766 | 21 | 99.8% |
| gem | 232 | 19 | 19 | 0 | 470 | 0 | 100.0% |
| **cargo** | 169 | 37 | 247 | **210** | 2,297 | 759 | **67.0%** |
| maven | 86 | 47 | 47 | 0 | 742 | 0 | 100.0% |
| pub | 57 | 11 | 11 | 0 | 162 | 0 | 100.0% |
| hex | 55 | 8 | 8 | 0 | 180 | 0 | 100.0% |
| composer | 5 | 1 | 0 | 0 | 0 | 0 | — |
| **generic** | **0** | 0 | 15 | 15 | 784 | 784 | **0.0%** |

The 100% rows are not coverage, they are tiny populations: gem observes 19
packages, pub 11, hex 8. Nine ecosystems are nominally live; four of them are
statistically empty, and `composer` has 5 samples on a package never observed in
any project.

### (2) High demand, sparse coverage

Top uncovered by real-project usage — read the split, because it decides whether
this is a backlog or a measurement artifact.

**Genuine library gaps, worth authoring:**

| package | project buckets | live samples |
| --- | --- | --- |
| `npm/recharts` | 494 | 0 |
| `npm/next` | 457 | 0 |
| `npm/zustand` | 358 | 0 |
| `npm/jiti` | 333 | 0 |
| `npm/@dnd-kit/core` | 320 | 0 |
| `generic/cli/go` | 274 | 0 |
| `generic/cli/npm` | 247 | 0 |
| `golang/github.com/grpc-ecosystem/grpc-gateway/v2` | 243 | 0 |
| `npm/webcrypto-core` | 233 | 0 |
| `npm/@aws-sdk/client-s3` | 225 | 0 |
| `npm/@tanstack/react-query` | 211 | 0 |

**Not authorable, and polluting the same ranking:** 25 of the top 40 uncovered
packages are platform-specific optional binaries with no API surface —
`@esbuild/openbsd-x64` (456), `@esbuild/win32-x64` (454), every
`@rollup/rollup-*-*` (351–400), `fsevents` (461). They rank high because a
lockfile resolves onto them on every install, not because anyone calls them. A
coverage scheduler ranking on this signal will spend the corpus's scarce
authoring capacity on stubs.

The highest `wanted.asks` entries with zero coverage show the same shape mixed
with real ones: `pub/path_provider` (22), `pub/shared_preferences` (19),
`golang/encoding/json` (4), `golang/net/http` (3) — the last two being
**standard library** coordinates, a category with no samples at all.

### (1) High density, low value

Absolute density tracks usage well at the top and is *not* misallocated:
`pgx/v5` 131 samples / 5,615 buckets; `golang.org/x/net` 129 / 7,190;
`golang.org/x/sys` 119 / 15,296. A naive "too many samples on popular packages"
reading is wrong. The saturation is in the tail, ranked by samples per project
bucket (n ≥ 5 samples):

| package | samples | covered versions | project buckets | samples/bucket |
| --- | --- | --- | --- | --- |
| `composer/league/csv` | 5 | 1 | **0** | never observed in any project |
| `gem/csv` | 16 | 3 | 9 | 1.78 |
| `gem/rack-test` | **21** | **1** | 36 | 0.58 |
| `gem/mustermann` | **22** | 2 | 39 | 0.56 |
| `gem/set` | 15 | 2 | 27 | 0.56 |
| `gem/sinatra` | 13 | 1 | 24 | 0.54 |
| `gem/rack` | 20 | 2 | 39 | 0.51 |
| `gem/json` | 17 | 1 | 36 | 0.47 |
| `pypi/httpx2` | 7 | 1 | 19 | 0.37 |
| `hex/req` | 12 | 1 | 40 | 0.30 |

21 samples on one version of `rack-test` is the pattern: depth on a single
release of a low-usage coordinate. And redundancy across releases where nothing
changed — samples whose contract array is byte-identical to another sample's for
the same package:

| package | identical-contract groups | redundant samples |
| --- | --- | --- |
| `golang.org/x/net` | 26 | **45** |
| `google.golang.org/genproto/googleapis/rpc` | 18 | 33 |
| `golang.org/x/sys` | 18 | 29 |
| `golang.org/x/crypto` | 10 | 20 |
| `npm/vitest` | 9 | 19 |
| `npm/electron` | 11 | 16 |

Corpus-wide: **518 identical-contract groups covering 677 redundant samples**
(9.6% of the live corpus) — the same assertions re-verified across releases that
did not change the API under test.

---

# P2

## P2-13 — 157 samples declare a symbol their own contract never exercises

The symbol is the search key. Measured over 11,385 declared symbol slots on
5,842 live samples, using a deliberately permissive test (case-insensitive
substring of the symbol's last segment after any of `. # / :`, searched in the
concatenated contract text):

| | count | note |
| --- | --- | --- |
| slots whose last segment never appears in the contract | **1,250** (11.0%) | upper signal |
| samples where **not one** declared symbol appears | **157** | working number |
| slots where the "symbol" is just the package name | **397** | not an API |
| samples where **every** "symbol" is the package name | **213** | e.g. `symbols:["react-refresh"]` for `npm/react-refresh` |
| samples with no supported symbol **and** no package-name symbol | **105** | conservative floor |

Take 105 as the floor and 157 as the working number; the permissive matcher
means the true count is at least this and the 1,250-slot figure is the upper
signal. A concrete case: a `@typescript-eslint/typescript-estree@8.67.0` sample
declares `symbols:["visitorKeys"]` while all six contract lines assert
`simpleTraverse` and `findNodesByType` behaviour — the sample is good, the
search key is wrong.

Also structural, exhaustive: **1,228 live samples (17.4%) declare no symbols at
all**, 49 samples disagree between `manifest.symbols` and
`manifest.case.symbols`, and 1 disagrees on `packages`.

## P2-14 — the published description is the internal work-queue line on 76% of the corpus

| | count | share of 7,070 |
| --- | --- | --- |
| `case.goal` matching `^verify ` (the `csx sample-worker next` template) | **5,406** | 76.5% |
| `case.goal` matching `^verify pkg:` (bare purl, no human words) | 1,871 | 26.5% |
| `subject` absent | **1,809** | 25.6% |
| `subject` present and equal to a raw `pkg:` purl | 5,261 | 74.4% |
| `case.goal` missing | 0 | — |

`docs/architecture.md` already records why SERP copy is derived rather than
taken from the goal, and `serpcopy.go` does the deriving — so the rendered
`<title>` is protected. The corpus field itself is not: `Goal:` is printed
verbatim by the MCP renderer (`internal/mcp/tools.go:995-997`), so an agent
asking this network what a sample is for is told
"verify pkg:npm/browserslist@4.28.7" on roughly a quarter of hits. Samples are
immutable, so this is a re-author-or-suppress decision, not an edit.

## P2-15 — duplicates accumulated after the 2026-08-19 dedup pass

| class | groups | redundant samples |
| --- | --- | --- |
| exact (packages + symbols + contract identical) | 82 | **82** |
| near (packages + symbols identical) | 204 | 213 |
| identical contract text (any package) | 518 | 677 |
| two live samples sharing one `case_id` | 44 | 44 |

1,227 samples are already quarantined as duplicates (983 "duplicate
coordinate", 244 "duplicate template-goal"), so dedup has run once and these
accumulated after it. The near-duplicate key cannot separate the worst groups
because they have no symbols: `@eslint/plugin-kit@0.7.3` has 5 live samples with
`symbols: null`, `babel-plugin-polyfill-corejs3@0.11.1` has 4,
`klauspost/compress@v1.20.0` has 4.

## P2-16 — stranded and orphaned state

| | count | detail |
| --- | --- | --- |
| `open` cross verification jobs | 6 | 5 created 2026-09-08; unclaimed 7 days |
| `unsupported` cross jobs | 20 | 2026-09-01 → 2026-09-09 |
| `authoring_drafts` with no `samples` row | **73** | oldest 2026-08-18; blob-store leak |
| receipts attached to quarantined samples | 1,419 | signed work on withdrawn samples |
| live samples carrying a FAIL receipt beside their PASS | 166 | page says verified; the FAIL is not surfaced |
| `authoring_attempts` quarantined | 69 | 25 still closed, 0 reopened |
| orphan DB tables absent from the entire source tree | **6** | see below |

The six tables — `evidence_agg_before` (4,622 rows), `evidence_dedup_before`
(17,076), `failure_clusters_before` (7,132), `target_pairs` (11,134),
`target_dedup` (17,076), `target_agg_ids` (4,622) — appear in no `.sql`
migration and in no `.go` file. All six were last autovacuumed within one second
of each other at **2026-08-26 07:26:39Z** and not touched since: they are debris
from a manual session on that date. They are small (a few MB), so this is
hygiene rather than a performance item — but they are indistinguishable from
schema to anyone reading the database.

## P2-17 — documentation contradicts production

`docs/schema.md` states:

> "Until such a client is released, `evidence_quality` on every FAIL row stays
> `legacy-evidence-incomplete` and the modern cluster count is legitimately
> zero — that is the contract reporting the truth, not a defect."

Production, exhaustively:

| `evidence_quality` | `evidence_agg` FAIL rows | `failure_clusters` rows |
| --- | --- | --- |
| `complete` | 244,389 | **229,517 (94.6%)** |
| `legacy-evidence-incomplete` | 11,634 | 5,185 |
| `partial` | 2,103 | 1,465 |

A structured-termination client shipped; the doc still tells a reader the modern
count is zero. Anyone sizing the diagnostic backlog from `schema.md` will be
wrong by five orders of magnitude.

Separately: two migrations share the number **0034**
(`0034_authoring_work_axis.sql` and `0034_samples_manifest_trgm_idx.sql`). They
apply in lexical order today, so nothing is broken, but the number no longer
identifies a migration.

## P2-18 — a 26 MB index maintained on every sample write, used once

| index | size | `idx_scan` |
| --- | --- | --- |
| `samples_manifest_lower_trgm_idx` | **26 MB** | **1** |
| `failure_clusters_pkey` | 9,544 kB | 0 |
| `search_hits_offer_idx` | 544 kB | 0 |
| `search_hits_day_idx` | 72 kB | 0 |
| `version_coresidence_lib_idx` | 56 kB | 0 |

The trigram index (migration `0034_samples_manifest_trgm_idx.sql`) is half the
size of the `samples` table it indexes, is maintained across all 8,352 inserts
and 37,586 updates, and has served one scan in the lifetime of this database.

## P2-19 — the sitemap's cold rebuild costs a crawler 48.8 s, and fails after a restart

```
GET /sitemap.xml   (cold)  200  ttfb=48.84s  size=451
GET /sitemap.xml   (warm)  200  ttfb=1.23s
14:32:18  web: sitemap rebuild failed: sitemap: samples: context canceled
```

The 15-minute in-process cache is the documented design
(`docs/architecture.md`, `internal/web/sitemap.go`), and the *content* is
healthy — `urls=10205 shards=3 packages=3120/3135 samples=7070/7070
unroutable_packages=15 malformed_sample_ids=0 sample_bound_hit=false`, the 15
unroutable being the `generic` ecosystem the router does not serve. What is not
healthy is that one crawler request per window pays a 48.8 s rebuild on a
starved host, and the first attempt after every restart is cancelled. The shards
themselves serve in ~1.0 s.

---

## Ranked summary

| # | finding | severity | measured |
| --- | --- | --- | --- |
| P0-1 | 94% of public package pages serve 503 | P0 | 47/50 random of 3,415; 19/20 post-deploy; 76.18 s EXPLAIN; 214 `pool_busy` in 24 min |
| P0-2 | builder pass (8m36s, then >20 min) outruns its 5 min interval and owns the pool | P0 | server ledger; overlapping starts |
| P0-3 | 76–80% CPU steal, no cgroup limit, load 13 on 2 vCPU | P0 | `vmstat`, `iostat`, `cpu.stat` |
| P0-4 | 86.3% of live `CROSS_PASS` labels fail the rule that grants them; 77.3% rest on one receipt | P0 | 5,713/6,621; 5 peer keys; 100% linux/x64 |
| P1-5 | `failure_clusters` per-package read: 26.6 h DB time, 86% disk I/O, no LIMIT | P1 | `pg_stat_statements` |
| P1-6 | `builder_purl_coord` expression index: 18.5 h DB time, 10.4 M calls | P1 | `pg_stat_statements`, `EXPLAIN` |
| P1-7 | snapshot upsert unguarded (2.33 M rewrites); cluster upsert pays 11.5 h to compare | P1 | `pg_stat_statements`, `pg_stat_user_tables` |
| P1-8 | `/admin/api/farm` times out on every 60 s poll; the read needs ~4 min by design | P1 | 22 timeouts in 29 min; mean 183 s / max 433 s |
| P1-9 | 1,430 MB DB behind a 640 MB container limit; 64% hit ratio; 25 bn seq tuples | P1 | `pg_statio_user_tables`, `docker inspect` |
| P1-10 | public page status codes are not logged at all | P1 | Caddyfile + 429,084 log lines |
| P1-11 | corpus growth down 9×; 128,590 candidates and 1,775 packages unworked | P1 | weekly counts; `diagnostic_candidate` |
| P1-12 | 14.5% of observed demand uncovered; cargo 67%, generic 0%; tail saturation | P1 | 379,673 project buckets |
| P2-13 | 157 samples declare a symbol their contract never exercises (floor 105) | P2 | 11,385 slots |
| P2-14 | 76.5% of goals are the internal work-queue template; 25.6% have no subject | P2 | exhaustive |
| P2-15 | 82 exact + 213 near + 677 identical-contract duplicates | P2 | exhaustive |
| P2-16 | 6 stuck jobs, 73 orphan drafts, 1,419 receipts on quarantined, 6 orphan tables | P2 | exhaustive |
| P2-17 | `docs/schema.md` says the modern cluster count is zero; production says 229,517 | P2 | exhaustive |
| P2-18 | 26 MB trigram index, 1 lifetime scan | P2 | `pg_stat_user_indexes` |
| P2-19 | 48.8 s cold sitemap rebuild; cancelled after restart | P2 | `curl -w`, server log |

## What this audit did not establish

- **Browser/admin JS cost.** `/admin` requires a credential this audit did not
  hold (`401` on `/admin` and `/admin/api/farm`). Admin findings here are from
  the server's own pressure log and `pg_stat_statements`, which measure the
  server and DB side only. Client-side render cost is unmeasured.
- **Whether the CPU steal is Lightsail burst-credit exhaustion specifically.**
  Steal, the absence of any cgroup throttle, and a 3-day 44.79% average are
  measured; the bundle's baseline allowance was not read (no Lightsail API
  credential), so credit exhaustion is the mechanism consistent with the
  measurement rather than a measured fact.
- **Per-page latency history.** No web-page access log exists (§P1-10), so
  "intermittent page-to-page latency" could only be measured live, in one
  52-minute window, during an external deploy.
- **The 502s.** Excluded as deploy-window artifacts; see the confound note at
  the top.
- **Sample source code.** Non-runnability was tested through receipt linkage,
  contract presence and symbol support — not by re-executing artifacts. Every
  live sample has at least one PASS receipt (`A1=0`, `A2=0`), so no sample in
  the corpus is non-runnable in the sense of "never ran". What §P0-4 shows is
  that it ran **once, in one place, signed by one key**.
- **Staleness is not a problem here, and that is a finding too.** Of 7,237 live
  sample↔package pairs, 1 is pinned to a version unseen in 30 days, 131 to one
  unseen in 14 days, and 668 to a version superseded by more than 7 days. No
  live sample is older than 60 days. The corpus is young; its trust problem is
  §P0-4, not decay.
