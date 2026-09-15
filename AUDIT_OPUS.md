# AUDIT_OPUS — independent adversarial audit of CodeSampleX

**Auditor:** Claude Opus 5, independent adversarial pass.
**Date:** 2026-09-15.
**Branch:** `audit/opus-20260915`.
**Snapshot:** production `csx-prod-1` (54.116.158.230), server `v0.1.189` / `8e822f1`, probed 13:55Z–14:45Z.
**Inputs:** `README.md`, `docs/architecture.md`, `docs/schema.md`, `docs/operations.md`, `docs/authoring-quarantine.md`, Farm `README.md` / `goal.md`, the production database (read-only), the public API, and the public npm registry.
**Method:** every corpus conclusion is an exhaustive scan, not a sample. Every performance conclusion is a measurement taken during the audit, not an inference from code reading.

No other audit's output was read. Everything below was derived from first principles.

---

## Population and sampling limits

| Data | Population | Coverage |
|---|---|---|
| `samples` | 8,345 rows | **100%** — full `COPY ... TO STDOUT` dump, decoded offline |
| `receipts` | 10,965 rows | **100%** — full dump |
| `sample_packages` | 8,676 rows | **100%** — full dump |
| npm version existence | 1,413 distinct `name@version`, 936 packuments | **100%** of the npm corpus |
| `evidence_agg` demand | 2,898 coordinates at >=50 observations | top-4000 bounded; the tail below 50 observations is out of scope and stated as such |
| `pg_stat_statements` | since reset `2026-09-09T05:49:06Z` | 6 d 08 h window, not lifetime |
| HTTP availability | 6–12 probes per path | **sample, not census** — a ~45-minute window on one afternoon |

The HTTP figures are the only sampled numbers in this report. They are labelled as such wherever they appear.

---

## Summary of what is wrong

The single most important finding is not in the corpus. **The product's primary surface — package detail pages — is returning `503 database busy` for the large majority of requests.** The corpus itself is in better shape than the serving stack: evidence linkage is sound, and every npm version pinned by a live sample really exists. The corpus defects that do exist are quality-of-description and duplication defects, and all three of the largest ones are *still being produced today*.

| # | Severity | Finding |
|---|---|---|
| P0-1 | **P0** | Package pages return 503 for ~92% of requests; uncached packages are in a self-sustaining lockout |
| P0-2 | **P0** | Lightsail CPU burst capacity exhausted; host clamped to 20% baseline (~0.4 usable vCPU) |
| P0-3 | **P0** | Compatibility builder runs at a 78% duty cycle consuming ~0.9 vCPU, starving every request path |
| P1-4 | P1 | Pool policy in production is 3x tighter on connections and 12x tighter on wait than the shipped default |
| P1-5 | P1 | Admin farm panel times out 76x/day; its backlog aggregate is uncached and polled every 60 s |
| P1-6 | P1 | `builder_purl_coord()` costs **16.5 ms per call** (672x `lower()`) and is maintained on expression indexes over the two hottest write tables |
| P1-7 | P1 | ~55 billion tuples read by sequential scan from tables of 8k–320k rows |
| P1-8 | P1 | `csx-server` lives permanently above `GOMEMLIMIT`, so the Go collector never stops |
| D1 | P1 | 1,871 live samples (26.5%) publish the placeholder authoring goal as their description |
| D2 | P1 | 1,228 live samples (17.4%) declare no symbol at all |
| D3 | P1 | 215 live redundant samples; **100% created after the last dedup pass** |
| D4 | P1 | 57 releases stored under two purl spellings; index, manifest and receipt disagree |
| P2-9..11, D5 | P2 | Cache TTL mismatch, 3–7 minute authoring query, failing analytics writes, Go stdlib as a module |
| D6, D7 | OK | **Verified clean:** npm version plausibility, and sample-to-receipt evidence linkage |

---

# P0 findings

## P0-1 — Package detail pages return `503 database busy`

This is the product. `/npm/axios` is the page a developer lands on.

**Evidence.** Six sequential `GET`s per path, 2026-09-15T14:30Z:

| Path | 200 | 503 |
|---|---|---|
| `/npm/axios` | 0 | **6** |
| `/npm/react` | 0 | **6** |
| `/golang/github.com/google/uuid` | 1 (TTFB 10.13 s) | 5 |
| `/pypi/sqlalchemy` | 1 | 5 |
| **total** | **2** | **22** (91.7% failure) |

A sustained probe of one path — 25 attempts over 35 seconds — returned **503 on all 25**:

```
try1  503:1.069711   ...   try25 503:1.169658
never succeeded in 25 tries
```

Response body: `{"error":"database busy"}`, with `Retry-After: 2`.

**Mechanism.** Three admission layers stack, each with a ~250 ms budget:

1. `cmd/csx-server/webstore.go:381` — `packageLoadSlotCount = 4`, `packageLoadAdmissionWait = 250ms` produces `admission_refused`.
2. The interactive pool — production sets `CSX_DB_READ_CONNS=2` and `CSX_DB_READ_WAIT=250ms` produces `pool_busy`.
3. `snapshotLoadRetryDefer = 15s` — a post-failure blackout — produces `deferred_refused`.

The underlying snapshot read takes 10–36 s while a builder pass runs (the repo measures this itself — see P0-3). So no request can hold a slot long enough to complete, the 30-minute package cache is never filled, and **the cache can only be filled by a request that succeeds.** That is a livelock, not congestion: the failure is self-sustaining for as long as the builder duty cycle stays high.

The three most recent merges on `main` (#426, #429, #431) all target package-page responsiveness, and a worktree exists for `fix/issue-396-package-503`. The condition is still present in production.

**Reproduce.**

```bash
for i in $(seq 1 6); do curl -s -o /dev/null \
  -w "%{http_code} %{time_starttransfer}\n" https://codesamplex.dev/npm/axios; done
```

**Recommended fix.** Shortest path first:

1. Decouple package-page rendering from a synchronous snapshot load — serve stale-while-revalidate the way `HotPackages` already does, and let the *first* miss return a skeleton rather than a 503.
2. Raise `CSX_DB_READ_WAIT` back toward the shipped 3 s and `CSX_DB_READ_CONNS` toward 6. The current values were presumably tuned during an incident; with 2–8 s queries they convert slowness into unavailability.
3. Make the builder yield (P0-3) so reads return to their 0.59 s idle cost.

None of these is a broad refactor; (2) is an env change on the host.

---

## P0-2 — The host has exhausted its CPU burst capacity

`csx-prod-1` is a Lightsail `small_3_0`: 2 **burstable** vCPU. Three independent measurements say the same thing.

**CloudWatch, 2026-09-08 to 2026-09-15 (6-hour buckets):**

```
                     BurstCapacityPercentage     CPUUtilization
2026-09-10 - 09-12             0.01                  20.00  (max 20.02-20.07)
2026-09-13T21:00               0.08                  19.98
2026-09-15T09:00               0.00                  20.00  (max 20.58)
2026-09-15T15:00               0.00                  20.00  (max 21.90)
```

`CPUUtilization` sitting at **exactly 20.00 average with a 20.03 maximum for days** is not a workload shape. It is a throttle ceiling. Burst capacity is **0.00%**.

**Guest-side confirmation** (`vmstat 1 5`):

```
 r  b   swpd   free  ...  us sy id wa st
 3  1 414228 129224 ...  17  3  3  0 76
 9  0 414228 133408 ...  17  3  0  0 80
 7  0 414228 133156 ...  17  4  3  0 77
```

**76–80% steal time**, 0–3% idle, load average 6.15 / 7.03 / 6.10 on 2 vCPU.

Effective sustained compute is therefore **~0.4 vCPU**. The only window in the observed period where the instance escaped was 2026-09-12T15:00 to 2026-09-13T15:00, when burst recovered to 56.41% and CPU rose to 32–51% average — then it burned straight back to zero.

**Memory is in the same state.** 1,906 MB total, 366 MB available, 404 MB swapped. Since boot (3 d 07 h): **9,989,094 pages in / 11,295,783 pages out** — roughly 39 GB read and 44 GB written through swap, ~150 KB/s sustained. The `db` container has read **1.04 TB** from its block device in three days against a 1,430 MB database, because `shared_buffers` is 256 MB and there is no page cache left to help it.

**Recommended fix.** This is a sizing decision, not a code change, and it is the owner's call — but the data supports it plainly: the workload does not fit in 0.4 vCPU and 2 GB. Either move to a non-burstable instance (or a larger bundle) **or** cut the resident workload enough to fit inside baseline. P0-3, P1-6 and P1-7 are all candidates for the second route; on current numbers they would need to remove roughly 60% of the CPU demand between them.

---

## P0-3 — The builder runs 78% of the time and takes both throttled cores

`CSX_SNAPSHOT_INTERVAL=5m` is the **gap between passes**, not the period. A pass takes far longer than the gap.

**Measured from the server log, 2026-09-15 09:39 to 13:56:**

```
10:44:03 builder pass start    full=true
12:56:25 builder pass complete full=true targets=21678 packages=3135 clusters=234837
         cluster_read=1m26s  cluster_calculate=11m32s  cluster_write=33m09s
```

One full pass = **2 h 12 m**, of which **33 minutes is writing 234,837 cluster rows** into a table that holds 235,988 — a near-complete rewrite of the table, every full pass.

Duty cycle over that 257-minute window: **201 minutes busy = 78%**.

Process accounting agrees: `csx-server` has used 14,111 CPU-seconds in 15,436 elapsed (`ps -o etimes,times`) — it is scheduled on ~0.91 of a core continuously, on a host that has 0.4 cores to give.

**The repo has already measured the consequence.** `cmd/csx-server/webstore.go:410`:

> "With the builder idle the full read costs under a second (0.59 s measured after 35 s idle); while a builder pass runs on the same two cores it took **36.5 s** under EXPLAIN ANALYZE — all buffer hits, so CPU contention, not disk."

That is a 62x read amplification, and it is exactly the condition that produces P0-1.

**Recommended fix.**

1. Make the full pass incremental at the write layer: skip an upsert whose computed row is byte-identical to the stored one. 234,837 writes per pass for a corpus that gained a handful of samples is the defect.
2. Give the builder an explicit CPU budget (a `GOMAXPROCS`-limited worker, or a token bucket that yields when interactive pool waits exceed a threshold), so it cannot take the second core while a request is queued.
3. Back the interval off from "5 minutes after the last one finished" to something derived from actual corpus change.

---

# P1 findings — serving stack

## P1-4 — Production pool policy is far tighter than the shipped default

From `docker inspect codesamplex-server-1`:

```
CSX_DB_READ_CONNS=2        # DefaultPoolPolicy.InteractiveConns = 6
CSX_DB_READ_WAIT=250ms     # DefaultPoolPolicy.ReadWait        = 3s
CSX_DB_MAX_CONNS=          # unset -> 8
```

Two connections serve the entire public website and API, and a read gives up after 250 ms. `internal/serverstore/pool.go` documents the intent of the defaults — "the slowest page this site is known to have served under load took 9.3 s ... 8 s is far below the 60 s WriteTimeout that produced the 502s" — and the production values are a fraction of that.

**Measured pressure, 24 h of server log** (lines are throttled to one per second per class, so these are **lower bounds**):

```
class=interactive  984      cause=pool_busy          999
class=background    76      cause=query_timeout       93
class=probe         58      cause=admission_refused    8
                            cause=deferred_refused    18
```

Top pressured paths: `/dependencies` (220), `/admin/api/farm` (76), `/healthz` (58).

**`/healthz` is being refused.** The probe class has a reserved connection and still recorded 58 `pool_busy` events in 24 h, one of them after waiting **1.443 s**. The Docker health log shows `exit=8` twice in a row at 14:13:47 and 14:13:59 on 2026-09-15. The liveness probe is one bad minute away from restarting the container during the exact period it is least able to recover.

**Recommended fix.** Restore `CSX_DB_READ_WAIT` to `3s` and `CSX_DB_READ_CONNS` to `6` once P0-3 has reduced contention — doing it before then will trade 503s for 8-second page loads, which may still be the better failure. Raise `CSX_DB_PROBE_RESERVE` to 2 so `/healthz` cannot be starved.

## P1-5 — The admin farm panel times out

`internal/admin/farm_http.go` issues **five sequential uncached aggregates** per request: `FarmWorkers`, `FarmHealthNow`, `FarmBacklogNow`, `FarmCompletenessNow`, `coverage`. Only `coverage` is memoized.

`FarmBacklogNow` over the 6 d window: **2,103 calls, mean 10,409 ms, max 22,855 ms**. `farmAggregateTimeout` is 25 s, so the max is inside the ceiling only by 2 seconds.

`internal/admin/static/admin.js:569` and `:658` — `window.setInterval(load, 60000)`. An open admin tab re-runs this every 60 seconds.

**Measured outcome: 76 `/admin/api/farm` query timeouts in 24 h**, every one of them `cause=query_timeout`:

```
14:00:29 db pressure path=/admin/api/farm class=background cause=query_timeout query_timeout=1
```

When it fires the handler returns `503` with the Korean string for "could not load the backlog" — the admin dashboard's farm panel simply fails.

`/admin` is `ClassBackground` (`cmd/csx-server/dbclass.go`), which has **no statement ceiling** and can hold 4 of 8 connections. So a polling admin tab is also a sustained draw on the pool that public reads are competing with.

**Recommended fix.** Memoize `FarmBacklogNow` and `FarmCompletenessNow` the way `coverage` already is, with an age shown in the UI; the panel's own doc comment already argues that a stale number that states its age is an honest number. Then raise the poll interval, or make it poll only while the tab is visible.

## P1-6 — `builder_purl_coord()` costs 16.5 ms per call

`internal/serverstore/pg_builder_prestage.go:14` defines an `IMMUTABLE` SQL function that percent-decodes a purl with `regexp_match`, `regexp_matches ... WITH ORDINALITY`, `string_agg`, `decode` and `convert_from`.

**Measured on production (read-only, `generate_series`):**

| | 10,000 evaluations | per call |
|---|---|---|
| `builder_purl_coord(...)` | **165,432 ms** | **16.54 ms** |
| `lower(...)` baseline | 246 ms | 0.0246 ms |
| **ratio** | | **672x** |

It is maintained on expression indexes over the two hottest write tables:

```
evidence_agg_builder_coord_idx    ON evidence_agg(builder_purl_coord(purl), purl, symbol)
snapshots_builder_coord_idx       ON compatibility_snapshots(builder_purl_coord(purl), purl, symbol)
```

Every insert and update on those tables must evaluate it: 401,229 inserts + 400,961 updates on `evidence_agg`, 2,322,683 inserts on `compatibility_snapshots` over the window. `pg_stat_statements` attributes **10,358,032 calls and 65,366 seconds** to it — 18.2 hours of database CPU in 6.3 days, **~12% of one core continuously**.

**Recommended fix.** The decode is doing at query time what should be done once at write time. Store a canonical coordinate column populated by the writer (the Go side already has `domain.ParsePURL`), index that plain column, and drop the expression indexes. If the function must stay, replace the `regexp_matches ... WITH ORDINALITY` percent-decode with a single `convert_from(decode(...),'UTF8')` form or a PL/pgSQL equivalent — the ordinality join is what costs the 16 ms.

## P1-7 — 55 billion tuples read by sequential scan

`pg_stat_user_tables`, since stats reset:

| table | live rows | seq scans | seq tuples read |
|---|---:|---:|---:|
| `sample_packages` | 8,676 | 3,783,022 | **25,052,104,800** |
| `evidence_agg` | 318,441 | 255,307 | 9,395,035,302 |
| `samples` | 8,345 | 2,575,987 | 7,553,405,879 |
| `receipts` | 10,965 | 1,352,559 | 6,948,496,704 |
| `dependency_edge` | 109,686 | 123,603 | 3,360,173,687 |
| `compatibility_snapshots` | 21,686 | 86,780 | 1,203,005,922 |
| `packages` | 6,330 | 188,017 | 714,003,678 |

3.78 million sequential scans of an 8,676-row table is the signature of a per-item loop. The index side is no better: `sample_packages_coord_idx` recorded **35,675,890 scans**, `evidence_agg_target_idx` **270,928,044** — 497 index lookups per second sustained against `evidence_agg` for six days.

This is what produces the **1.04 TB** the `db` container has read from disk in three days.

Two supporting observations:

- `failure_clusters_pkey` has **0 scans** on a 390 MB table. The primary key is never used for lookup; every read goes another way.
- `samples_manifest_lower_trgm_idx` is **26 MB with 1 scan** — dead weight in a memory-starved instance.

**Recommended fix.** The highest-value single change is to batch the `sample_packages` / `samples` / `receipts` reads that run per-target in the builder into one keyed read per pass (the builder already knows its whole target set). Drop `samples_manifest_lower_trgm_idx`. Re-examine whether `failure_clusters_pkey` is the right key given nothing reads it.

## P1-8 — The server lives above `GOMEMLIMIT`

`GOMEMLIMIT=600MiB` (629 MB). Measured RSS: **659.7 MiB**, 85.9% of the 768 MB container limit.

A Go process above its soft memory limit runs the collector continuously, capped by the runtime at 50% of `GOMAXPROCS` — which is 2 here, so up to a full core. That is a plausible large share of `csx-server`'s 0.91-core draw, on top of the builder's own work.

`deploy/docker-compose.yml` documents the history: three OOM kills on 2026-09-01 at anon-rss 741,872 / 697,448 / 694,388 kB, and the comment states the fix "did not move the ceiling away from where the process actually lives." The measurement above says the process has since moved *above* the ceiling.

**Recommended fix.** This one should not be fixed by raising `GOMEMLIMIT` — that reintroduces the OOM band. Reduce live heap instead: the builder's `load_samples` phase decodes **22.5 MB of JSON in one pass** (`json_bytes_examined_or_constructed_cumulative=22583664`) and holds 7,069 sample rows and 9,546 receipt rows in memory at once. Stream and discard per package rather than materializing the corpus.

---

# P1/P2 findings — corpus and data

Evidence linkage came out **clean** (D7) and npm version plausibility came out **clean** (D6). The defects that exist are description quality, duplication, and coordinate canonicalization — and the first three are still being produced.

## D1 — 1,871 live samples publish the placeholder authoring goal (P1)

**26.5% of the live corpus** has a `case.goal` matching `^verify pkg:` — the line `csx sample-worker next` prints for an agent to start from.

```
2026-08: 1,555     2026-09: 316     most recent: 2026-09-15T08:32:30Z
```

`docs/architecture.md` acknowledges this and describes the mitigation: `internal/web/serpcopy.go` derives page titles from the subject rather than the goal, so the internal package URL no longer reaches search results. That mitigation is real and it works. **But it is a rendering workaround, not a corpus fix** — the goal is still the sample's own description in `GET /v1/samples/{id}`, which is what MCP clients and the CLI read. An agent asking "what does this sample do?" is told "verify pkg:npm/picomatch@2.3.2".

The rate is improving (32.6% of August samples, 13.7% of September's) but a sample was published with a placeholder goal **six hours before this audit ran**.

**Reproduce.**

```sql
SELECT count(*) FROM samples
WHERE NOT quarantined AND manifest->'case'->>'goal' ~* '^verify\s+pkg:';
```

**Recommended fix.** Refuse publication when the goal still matches the scaffold template. The check belongs at `csx sample publish`, alongside the leakage scan that already hard-refuses — it is the same class of rule, and publishing is already a human action.

## D2 — 1,228 live samples declare no symbol (P1)

**17.4% of the live corpus** has an empty `symbols` array. Per `docs/architecture.md`, code availability is keyed `package + version + symbol/API`. A sample with no symbol cannot populate a symbol-level cell; it can only say "some code exists for this release."

```
2026-08: 1,100     2026-09: 128     most recent: 2026-09-15T08:32:30Z
```

This overlaps heavily with D1 — the exact-duplicate groups below are almost all symbol-less.

**Recommended fix.** Same gate as D1: a sample whose contract asserts something must be able to name what it asserted against. If a package genuinely has no callable symbol, that is the quarantine system's "structurally impossible" case (`docs/authoring-quarantine.md`), and it should be recorded there rather than published as a symbol-less sample.

## D3 — 215 live redundant samples, all created after the last dedup pass (P1)

206 groups of live samples share an identical package set **and** an identical symbol set; 215 samples are redundant within them.

The database already shows this was a known problem — 1,227 of the 1,276 quarantined samples carry a dedup reason:

```
duplicate coordinate: superseded by the kept sample for this purl+symbols (dedup 2026-08-19)   983
duplicate template-goal sample: the work queue reissued an already-answered coordinate         244
```

**Of the 215 still-live redundant samples, 214 (99.5%) were created after 2026-08-19** — 165 in August, 50 in September. The second quarantine reason names the cause: *the work queue reissued an already-answered coordinate*. That reissue is still happening; the 2026-08-19 pass cleaned the backlog and nothing stopped the source.

Every one of these cost the fleet a full container verification run.

**Recommended fix.** The dedup key already exists and is already written into the quarantine reason. Apply it at claim time in the authoring scheduler — refuse to hand out a coordinate whose `(purl, symbols)` already has a live PASS sample — rather than at a periodic cleanup pass.

## D4 — One release under two purl spellings (P1)

**57 releases** exist under two spellings, affecting **882 manifest package rows**:

```
pkg:golang/github.com/google/uuid@v1.6.0    <-  [@1.6.0, @v1.6.0]        # missing v-prefix
pkg:golang/github.com/go-chi/chi/v5@v5.3.1  <-  [@5.3.1, @v5.3.1]
pkg:npm/%40babel/core@8.0.1                 <-  [@babel/core, %40babel/core]   # scope unencoded
```

113 golang rows lack the `v` prefix; 81 npm rows leave the scope `@` unencoded; 4 pypi names are not lowercased (`pkg:pypi/Django@6.1`).

Three stores disagree about the same sample:

| Store | Count that disagree |
|---|---|
| `sample_packages` index vs. the manifest it indexes | **94 samples** |
| Signed receipt `resolvedPackages` vs. the manifest | **287 receipts** |

Example: sample `sha256:c91f08ec...` — the manifest says `pkg:golang/github.com/google/uuid@1.6.0`, the index says `@v1.6.0`, and the receipt's `resolvedPackages` says `@v1.6.0`. The verifier installed `v1.6.0`; the published, immutable, content-addressed document claims `1.6.0`, which is not a Go module version string.

The system already knows the truth — README: *"Only signed v2 receipts may claim `resolvedPackages` — the versions the verifier actually installed, not the versions an author typed"* — and the snapshot is filed under the version that really ran. So the **cell** is right. What is wrong is the **document**: a reader who follows a package page into a sample sees a coordinate that does not resolve. `schema.md` solves exactly this problem for *symbols* via `symbolSpellings`; there is no equivalent reconciliation for *packages*.

Confirmed live: `GET /v1/samples/sha256:c91f08ec...` returns the uncanonical `@1.6.0` today.

**Recommended fix.** Canonicalize the purl at ingest (`csx sample create`), before the content address is computed, so the two spellings cannot both enter. For the 882 existing rows the receipt already carries the canonical form — a one-off reconciliation can derive it without guessing, and because published samples are immutable the correction belongs in a served-side alias rather than a rewrite.

## D5 — Go standard library recorded as a third-party module (P2)

21 package rows across 6 distinct purls:

```
pkg:golang/net/http@go1.26.5      7        pkg:golang/embed@1.26.5           2
pkg:golang/net/http@1.26.5        5        pkg:golang/encoding/json@go1.26.5 2
pkg:golang/encoding/json@1.26.5   4        pkg:golang/net/http@go1.22.0      1
```

`net/http`, `encoding/json` and `embed` are standard library, not modules. There is no registry coordinate to resolve, so these can never be version-verified, and they mix two version spellings (`1.26.5` and `go1.26.5`) on top of it. `GET /v1/registry/packages/pkg:golang/net%2Fhttp@1.26.5` returns 404 — the coordinate is advertised by a sample the router cannot resolve back.

Low volume, but it is a coordinate class that should be either rejected or given its own namespace (the toolchain, not a module).

## P2-9 — `HotPackages` TTL is 60 s for a ranking that changes hourly

`cmd/csx-server/webstore.go:2041` — `hotPackagesTTL = time.Minute`. The refresh runs `SnapshotKeys`, which is:

```sql
SELECT purl, symbol FROM compatibility_snapshots ORDER BY purl, symbol
```

**4,976 calls, mean 1,470 ms, max 32,236 ms, 103,244,335 rows returned in total** (20,748 per call) — a full ordered scan of the snapshot table every minute, to render **12 rows** on the landing page (`internal/web/landing.go:645`). 7,316 seconds of database time over the window.

Snapshots only change when the builder writes them, and a builder pass takes hours. **Recommended fix:** set the TTL from `SnapshotInterval` (or invalidate on builder completion), exactly as `hotShardTTL()` already does for the shard hint.

## P2-10 — Authoring expansion query takes 3–7 minutes

`internal/serverstore/dependencyclosure_pg.go` — `authoringCoverageCTE`. The `dependency_open` CTE does a `CROSS JOIN LATERAL` over `dependency_edge` (109,686 rows) and runs four `EXISTS` probes per row, two of them against `verified_packages`, a `MATERIALIZED` CTE with no index — so each probe is a scan of the CTE result.

| Variant | Calls | Mean | Max | Total |
|---|---:|---:|---:|---:|
| expansion candidates | 192 | **183,436 ms** | **432,955 ms** | 35,220 s |
| farm backlog stocks | 2,103 | 10,409 ms | 22,855 ms | 21,891 s |
| third variant | 1,059 | 10,143 ms | 22,814 ms | 10,741 s |
| | | | | **67,850 s** |

18.8 hours of database CPU in 6.3 days for one CTE family. The code acknowledges it — `ListAuthoringExpansionCandidatesUnhurried`'s comment reads *"the ~4 minutes the read needs on production"* and explains that it runs single-core so the website keeps the other. On a host with 0.4 effective cores, "the other core" is not there to keep.

**Recommended fix.** Materialize `verified_packages` into a real table (or a `MATERIALIZED VIEW` with an index on `purl`) refreshed on the builder's cadence. The `EXISTS` probes then become index lookups instead of CTE scans.

## P2-11 — Anonymous analytics writes fail continuously

```
csx: anonymous analytics write unavailable (activity undercounted)
```

appears in the server log roughly every 2–15 seconds. The message is honest about the consequence — but it means the activity figures on `/v1/stats` and the admin dashboard are undercounts of an unmeasured size, while the README's stats table presents `peers` / `projectsMonth` as counted buckets. Under sustained pool pressure this is almost certainly the same 503 path as P0-1 reaching a write.

---

# Verified clean

These were tested adversarially and came back sound. They are reported because a negative result from an exhaustive check is worth as much as a positive one.

## D6 — Every npm version pinned by a live sample exists on the registry

All **1,413** distinct `pkg:npm/name@version` coordinates in the live corpus were checked against `registry.npmjs.org` (936 abbreviated packuments, 0 fetch failures).

```
version EXISTS on registry: 1413
version ABSENT on registry: 0 (0.0%)
```

There are **no phantom versions and no non-installable npm pins.** Staleness relative to `dist-tags.latest` is a product decision rather than a defect, but for the record: 32.5% are exactly `latest`, 11.6% one patch behind, 19.2% one minor behind, 36.6% a major behind. Two coordinates are *ahead* of `latest` (`accepts@2.0.0` vs latest `1.3.8`, `fresh@2.0.0` vs `0.5.2`) — those are real published versions outside the `latest` tag, not errors.

## D7 — Evidence linkage is sound

```
live samples                          7,069
live samples with >=1 PASS receipt    7,069   (100%)
live samples with no receipt at all       0
live samples with receipts but no PASS    0
receipts with no sample row               0
```

Every served sample is backed by a passing contract receipt, and no receipt is orphaned. The README's claim that `verifiedSamples` counts "distinct samples with a sandbox contract-PASS receipt" is borne out exactly.

One thing to note rather than fault: `stages.compile` is `SKIPPED` on **10,798 of 10,965 receipts (98.5%)**, `PASS` on 164, `FAIL` on 3. For interpreted ecosystems that is correct and expected. It does mean the L2 "compiled" rung of the ladder is carried by the resolve and contract stages for almost the whole corpus — worth stating on the badge legend so `L2` is not read as "a compiler accepted this."

---

# Coverage priority

## The uncovered ranking is dominated by packages that cannot have a sample

Ranking `evidence_agg` observation volume against live sample coverage produces **1,420 uncovered coordinates** at >=50 observations. The top of that list is almost entirely **platform-specific binary sub-packages**:

| Slice of the uncovered ranking | Platform binary shims |
|---|---|
| top 20 | **16 (80%)** |
| top 40 | **35 (88%)** |
| top 100 | 57 (57%) |
| top 200 | 102 (51%) |

```
2117  @esbuild/linux-loong64@0.25.12      2029  @rollup/rollup-win32-x64-msvc@4.62.4
2106  @esbuild/linux-ppc64@0.25.12        2029  @rollup/rollup-win32-x64-gnu@4.62.4
2897  fsevents@2.3.3                      1977  @rollup/rollup-linux-x64-musl@4.62.4
```

**139,172 of the 316,939 uncovered observations (44%) belong to packages with no callable API.** They rank high because every `npm install` lockfile lists all of them as `optionalDependencies`, so the evidence path sees them on every scan.

`docs/authoring-quarantine.md` already names this exact shape — *"Per-platform npm native builds: `main` is the `.node` binary the parent package selects internally"* — as structurally impossible to author, and records a worker that burned four hours and 22 attempts on one such coordinate while network sample production fell from 33/hour to zero. **The quarantine system catches these after a worker has already paid for them; the ranking that hands them out has not been taught the same rule.**

Note the scope precisely: `GET /v1/wanted` — the *ask*-derived demand queue — is clean (0 shims in its top 25), and `/gaps` renders a different, alphabetical slice. The pollution is in the **observation-derived** coverage computation that feeds the dependency-closure authoring scheduler.

**Recommended fix.** Apply the quarantine document's own shape rules as a *filter on the ranking*, not only as a post-hoc quarantine: exclude coordinates that are optional-dependency-only platform artifacts (`{os}-{arch}[-{libc}]` name pattern under a known shim scope, plus `fsevents`), Maven pom-only coordinates, and Gradle plugin markers, before ranking. That reclaims the top 40 slots of the authoring queue immediately.

## Genuine high-demand gaps

After filtering shims, **1,081 real API coordinates with >=50 observations have zero live samples**, carrying 177,767 observations between them. Top of that list:

| Observations | Fail obs | Observed symbols | Coordinate |
|---:|---:|---:|---|
| 4,697 | 1,128 | 6 | `npm/node-forge@1.4.0` |
| 3,721 | 671 | 5 | `golang/golang.org/x/mod@v0.37.0` |
| 2,259 | 352 | 2 | `npm/zustand@5.0.14` |
| 2,161 | 249 | 1 | `npm/jiti@2.7.0` |
| 1,320 | 459 | 17 | `npm/recharts@2.15.4` |
| 1,189 | 405 | 19 | `npm/next@14.2.18` |
| 1,154 | 253 | 10 | `npm/@dnd-kit/core@6.3.1` |
| 1,001 | 147 | 21 | `golang/google.golang.org/grpc@v1.80.0` |
| 881 | 297 | 11 | `npm/@playwright/test@1.58.1` |
| 577 | 189 | 7 | `npm/@tanstack/react-query@5.90.16` |

The fail column is a more interesting ranking signal than raw volume: `recharts@2.15.4` has 459 failure observations across 17 observed symbols and no sample at all. That is a Finding waiting to be written, not just a coverage hole.

**265 more coordinates have coverage but at more than 2,000 observations per sample**, led by `golang.org/x/sys@v0.47.0` — 63,850 observations, 8 samples.

Full ranked lists (150 gaps, 100 thin, 100 oversaturated) are in `AUDIT_OPUS_DATA.json` under `coveragePriority`.

## Oversaturated low-value regions

148 coordinates carry 8 or more live samples against fewer than 100 observations per sample:

| Live samples | Observations | Coordinate |
|---:|---:|---|
| 63 | 2,936 | `npm/semver@7.8.5` |
| 47 | 2,041 | `npm/bare-os@3.9.3` |
| **40** | **186** | `npm/three@0.185.1` |
| 37 | 1,128 | `npm/typescript@7.0.2` |
| **35** | **156** | `npm/yaml@2.9.0` |
| **21** | **94** | `npm/@babel/core@8.0.1` |
| **21** | **147** | `npm/scheduler@0.25.0` |

`three@0.185.1` has 40 verified samples against 186 observations — more samples than the corpus holds for most of the top-10 demand list. Concentration overall: the top 50 package names hold 30.8% of the live corpus, while 514 of 1,344 package names (38.2%) have exactly one sample.

---

# What I could not establish

Stated plainly rather than guessed:

- **Whether the 503 rate varies by time of day.** All availability probes ran in one ~45-minute window. The rate could be better at low traffic; the *mechanism* (P0-1) does not depend on load, but its frequency does.
- **Whether symbol names are real exports.** Verifying that `zod.safeParse` is an actual export of `zod@4.1.12` requires resolving and introspecting 936 packages, which I did not do. I checked the cheap proxy (builtin and global names recorded as package symbols) and found 91 samples, most of them legitimate — `JSON.parse` under `pkg:gem/json` is that gem's API. **I am not reporting a symbol-accuracy defect, because I did not measure one.**
- **The exact share of `csx-server`'s CPU attributable to GC vs. builder work.** Both are established independently (P1-8, P0-3); splitting them needs `GODEBUG=gctrace=1` on a production restart, which is not a read-only probe.
- **Whether `pg_stat_statements` double-counts the `builder_purl_coord` body against its callers** under `track=top`. The 16.5 ms per-call figure is measured directly and does not depend on this; only the 65,366 s total does.

---

# Recommended order of work

1. **P0-1 + P1-4** — get package pages answering. Env change plus stale-while-revalidate on the package cache. Smallest change, largest user-visible effect.
2. **P0-3** — stop the builder rewriting 234,837 unchanged rows per pass and give it a CPU budget. This is what lets everything else fit.
3. **P0-2** — decide sizing. If (2) does not bring sustained demand under 0.4 vCPU, the instance is the answer, and the burst metric will say so within a day.
4. **P1-6 + P2-9 + P2-10** — three independent multi-hour-per-week database costs, each fixable on its own.
5. **D3 then D1/D2** — close the duplicate reissue at claim time, then gate publication on goal and symbols. D3 is still consuming fleet verification capacity every day.
6. **Coverage ranking filter** — apply the quarantine document's shape rules before ranking, not after.

---

*Commands and queries used are in `AUDIT_OPUS_COMMANDS.md`. Machine-readable findings and the full coverage rankings are in `AUDIT_OPUS_DATA.json`. All production access was read-only: `SELECT` / `COPY ... TO STDOUT`, `docker inspect`, `docker logs`, `GET` requests, and CloudWatch reads. Nothing was written to production.*
