# Issue #485: the verifier queue's 503 was never the database

Root-cause analysis of the 2026-09-17..19 production incident in which every
`GET /v1/verification/jobs` answered `503 {"error":"database busy"}` for more
than 33 hours, CodeSampleX-Farm produced zero verification receipts, and the
Builder completed no pass after 2026-09-17 12:34 UTC. Everything below was
measured on the production host on 2026-09-19 between 02:28 and 02:45 UTC
(11:28–11:45 KST), read-only, with the incident live.

## The chain, shortest form

```text
csx-server holds more live memory than its GOMEMLIMIT
  -> the Go collector runs at its 50% CPU cap on every cycle (~1 core, forever)
  -> the 2-vCPU burstable instance's CPU credit balance is spent
  -> the hypervisor reports every cycle above the baseline as steal (54% of 6.8 days; 77% in a window)
  -> the resource governor (#454) reads steal >= 20% -> "host-cpu-steal"
  -> it pauses the Builder AND drops Farm ingest's admission ceiling to 0
  -> every Farm request is refused at the pool gate before touching a connection
  -> writeStoreErr maps the refusal to 503 "database busy" + Retry-After: 2
```

The database was idle the entire time. The queue query itself runs in 75 ms.
"database busy" was the pool's admission gate, driven to zero by a CPU
signal the server was generating against itself.

## What the ops hand-off said, and what was measured

The hand-off (Farm #163 / PR #186) named "PostgreSQL pool pressure /
statement timeout around `OpenJobsPage`" as the likely pressure point. That
was the first thing tested and the first thing ruled out.

| Claim | Measured | Verdict |
| --- | --- | --- |
| `OpenJobsPage` is slow / times out | `EXPLAIN (ANALYZE, BUFFERS)` of the exact query with representative parameters on production: **75.2 ms execution, 833 shared buffer hits, 0 reads**, 250 candidate rows, top-N heapsort 55 kB. Statement ceiling for the class is 30 s. | ruled out |
| the pool is saturated | `GET /v1/ops/pool-metrics`: `pool.inUse: 0`, `idle: 10`, `open: 10` of 12. `pg_stat_activity`: 1 active, 12 idle backends. | ruled out |
| Farm's requests are refused by the pool | `pool.classes[farm_ingest]`: **`limit: 0`**, `busy: 159104`, `waited: 1`, `timeouts: 0`. The class was refused 159,104 times and queued once. | confirmed — refused at admission, not by a connection |
| the 503 is a wait-budget timeout | TTFB of the 503 from this workstation: 0.76–3.0 s including ~0.5 s TLS; the Farm class's wait budget is 5 s and statement ceiling 30 s. The refusal is immediate: `farmCappedErr(0)` — "admission paused: ceiling 0". | confirmed |
| something is wrong with the Farm client | restarting `csx-verify` reproduced the 503 immediately (Farm #163); the same URL from this workstation 503s. | ruled out |

## The evidence, in the order it was gathered

### 1. The live 503 and the idle pool (02:28–02:37 UTC)

```text
GET https://codesamplex.dev/healthz                                    200
GET https://codesamplex.dev/v1/wanted                                  200
GET https://codesamplex.dev/v1/verification/jobs?peerId=...&limit=1   503  Retry-After: 2  {"error":"database busy"}
GET https://codesamplex.dev/version                                    200  v0.1.197 ca6480e built 2026-09-16T20:03:39Z
```

`GET /v1/ops/pool-metrics` (admin Basic auth), verbatim:

```json
{"pool":{"enabled":true,"maxConns":12,"open":10,"inUse":0,"idle":10,"classes":[
  {"class":"interactive","limit":6,"inUse":0,"waited":60196,"busy":10417,"timeouts":158,"retries":0,"suppressed":2033},
  {"class":"background","limit":4,"inUse":0,"waited":39496,"busy":28,"timeouts":154,"retries":49770,"suppressed":0},
  {"class":"probe","limit":1,"inUse":0,"waited":702,"busy":322,"timeouts":0,"retries":0,"suppressed":0},
  {"class":"farm_ingest","limit":0,"inUse":0,"waited":1,"busy":159104,"timeouts":0,"retries":0,"suppressed":0}]},
 "host":{"stealPercent":76.83,"loadAvg1":2.96,"sampledAt":"2026-09-19T02:37:23Z"},
 "farmIngest":{"lastCommitAt":"2026-09-17T17:49:42Z","lastCommitFound":true}}
```

Three numbers carry the whole diagnosis: `farm_ingest.limit: 0`,
`pool.inUse: 0`, `host.stealPercent: 76.8`.

### 2. The governor's own log: paused since 11:51 UTC on the 17th

`docker logs codesamplex-server-1 | grep governor`:

```text
2026/09/17 11:51:13 governor paused background work reason=host-cpu-steal builder=paused farm_ingest=paused interactive_busy=0 interactive_attempts=13
...
2026/09/18 13:53:38 governor resumed background work after=host-cpu-steal builder=running farm_ingest=2
2026/09/18 13:53:43 governor paused background work reason=host-cpu-steal builder=paused farm_ingest=paused interactive_busy=0 interactive_attempts=19
...
2026/09/19 02:43:39 governor paused background work reason=interactive-pool-pressure builder=paused farm_ingest=paused interactive_busy=4 interactive_attempts=31
2026/09/19 02:43:43 governor paused background work reason=host-cpu-steal builder=paused farm_ingest=paused interactive_busy=0 interactive_attempts=32
```

The first pause landed 16 seconds after the process started (the container
was created 2026-09-17 11:50:57 UTC). Per day: 303 pause lines / 38 resumes
on the 17th, 855 / 11 on the 18th, 6 / 0 on the 19th up to 02:45. Every
resume is followed by a re-pause on the next 5-second tick. The pause lines
alternate between the two reasons, which is why there are so many of them:
a change of reason is logged as a new transition, but Farm's ceiling was 0
throughout. `db pressure path=/v1/verification/jobs class=farm_ingest
cause=pool_busy` appears 6,722 times in the same log.

### 3. The host: steal is chronic, and csx-server is the load

```text
$ uptime
 02:38:01 up 6 days, 20:22,  load average: 2.40, 2.24, 2.19
$ head -1 /proc/stat           # user nice system idle iowait irq softirq steal
cpu  19345566 9768 3165441 28120430 5148980 0 386365 65239380 0 0
```

Over the 6.8-day uptime: user 19.3 M ticks, idle 28.1 M, **steal 65.2 M of
121.4 M total = 54%**. A 30-second window while the incident was live:

```text
/proc/stat delta over 30 s:  user 1038  system 112  idle 222  iowait 67  steal 4676   (6121 ticks = 2 vCPU x 30 s)
csx-server utime+stime delta over the same 30 s: 4519 ticks = 45.2 s of task time
```

The guest as a whole received 11.5 CPU-seconds in 30 s (0.38 core) and was
stolen 46.8 s (77%). csx-server alone accounted 45 s of task time in the
same 30 s — it is runnable for ~1.5 cores continuously, with the Builder
paused, a standalone Builder container at 0% CPU, ~2 requests/s arriving
(2,813 of the last 3,000 API-log lines are `304` shard checks), and the
pool idle. `top` agreed: `csx-server` 111.8% CPU, `50h38m` of cumulative CPU
in `1d14h47m` of process lifetime (1.3 cores averaged over its life), `st`
44.1%. CPU pressure-stall (`/proc/pressure/cpu`): `some avg60=42.31`.

A 2-vCPU Lightsail plan schedules a baseline of roughly a fifth of each vCPU
and lends the rest from a credit balance; a process demanding 1.5 cores
around the clock drains that balance, after which every cycle above the
baseline is reported as steal. The governor's steal branch was designed for
a noisy neighbour. It was reading the server's own demand.

### 4. Where the 1.5 cores go: the collector at its memory-limit cap

```text
$ grep -E 'VmRSS|RssAnon|VmSwap' /proc/2996915/status
VmRSS:    627844 kB
RssAnon:  617292 kB
VmSwap:   199124 kB
$ cgroup memory.stat (server container, limit 768 MiB): anon 632729600  file 37285888  swapcached 214106112
$ docker exec codesamplex-server-1 env | grep GOMEMLIMIT
GOMEMLIMIT=600MiB
```

617 MiB of anonymous memory resident plus 199 MiB swapped is ~816 MiB of
Go-owned memory against a 600 MiB `GOMEMLIMIT`. When the live set cannot be
brought under the limit, the Go runtime's GC CPU limiter engages and the
collector runs on every cycle at its cap of 50% of `GOMAXPROCS` — one full
core of the two, permanently. That, plus the mutator, is the 1.5 cores. The
host is at 84 MiB free with 540 MiB of swap in use, so the collector's
full-heap scans are also paging.

This is the one link in the chain that could not be read from any existing
operator surface: the runtime exposes it (`/gc/limiter/last-enabled:gc-cycle`,
`/memory/classes/total:bytes`) but nothing served it. This PR adds those
samples to `GET /v1/ops/pool-metrics` as `runtime` (see the runbook), so the
next reading is a curl, and so the "after" number for the memory work below
is measurable without a host shell.

### 5. What is holding ~800 MiB: the whole snapshot corpus, in the server, twice over

`cmd/csx-server/webstore.go` keeps, for the life of the process:

| cache | what it holds | bound |
| --- | --- | --- |
| `snapshotRows []SnapshotRow` | every row of `compatibility_snapshots` with its full JSON text | none; refreshed every 5 min, old and new copies coexist during the refresh |
| `snapshotJSON sync.Map` (`purl\|symbol` → JSON) | the same documents, one entry per (purl, symbol), also filled per package by `loadSnapshotsForPURL` | TTL 30 min on *staleness*, but entries are replaced, never deleted |
| `targetsRows` + `targetsIndex` | the whole (purl, symbol) inventory and three derived indexes | none |
| `sampleArtifacts sync.Map` | decoded sample tarballs (files + source text) for every sample page ever rendered | never deleted |
| `pkgVersions`, `pkgSamples`, `pkgCounts`, `pkgFailureClusters`, `pkgDependencies`, `wantedPackage`, `searchSamples`, `dependencySubjects` | per-package / per-query result sets | never deleted |
| `recordRows`, `hotRows`, `gapsAll` | whole-corpus rankings | one copy each |

The corpus, measured on production 2026-09-19:

```sql
SELECT count(*), pg_size_pretty(sum(octet_length(snapshot::text))) FROM compatibility_snapshots;
-- 22547 rows, 234 MB of JSON text
```

The same table was 17,255 rows / 149 MB on 2026-09-02 (the comment above
`recordSnapshotCacheTTL` records that measurement). It grew 57% in 17 days,
and the server holds all of it as Go strings. Crawlers render every package
page (`db pressure path=/npm/...`, `/golang/...` lines arrive about once a
second), so `snapshotJSON` converges on the whole corpus through the
per-package path even if no `/records?os=` request ever loads
`snapshotRows`. 234 MB of JSON text plus the `[]SnapshotRow` headers, plus
the target index, plus decoded artifacts, plus (while the filter path is warm) the pgx receive buffers of a
234 MB result set every five minutes, is the ~800 MiB.

`snapshotRows` exists for exactly one consumer: `rankedRecordPackages` with
an OS / runtime / basis filter, which needs per row only the environment
fingerprints and which stages have counts — a few dozen bytes per row that
`recordSnapshotMatches` currently recovers by `json.Unmarshal`-ing the full
document on every filtered request.

### 6. Collateral: the standalone Builder is being OOM-killed

Not the cause of the 503, but the reason `generatedAt` has not moved since
2026-09-17 12:34 UTC even in the windows the governor did resume:

```text
Sep 18 18:13:16 kernel: Memory cgroup out of memory: Killed process 3717950 (csx-builder) anon-rss:242480kB
Sep 18 19:36:22 kernel: Memory cgroup out of memory: Killed process 3726988 (csx-builder) anon-rss:245028kB
Sep 18 19:48:04 kernel: Memory cgroup out of memory: Killed process 3759846 (csx-builder) anon-rss:242860kB
```

The `builder` service has a 256 MiB cgroup limit and a 192 MiB `GOMEMLIMIT`;
a pass over the 234 MB corpus no longer fits. This belongs to the memory
lane below, not to the governor fix.

## What this PR changes

1. **Governor: host steal pauses the Builder only.** `decide` no longer sets
   `PauseFarmIngest` for `host-cpu-steal`. Farm is still shed on
   `interactive-pool-pressure` — the one signal its connections can actually
   contribute to — so the bounded-backpressure contract under true
   saturation is unchanged, and `interactive-pool-pressure` still takes
   precedence when both are present. The pause log line now reports the
   live Farm ceiling (`farm_ingest=2`) instead of the literal `paused`
   whenever Farm was not shed, so the runbook's `farm_ingest=paused` grep
   matches only transitions that stopped Farm. Tests:
   `TestGovernorDecidePausesOnlyTheBuilderOnSustainedHostSteal`,
   `TestGovernorDecideStillShedsFarmOnPoolPressureUnderSteal`,
   `TestGovernorShedsOnHostStealWithAHealthyPool` (now asserts the ceiling
   and the log line through a pause and a resume).

   After this deploys, with the memory problem still present, the pool idle
   and steal still ≥ 20%: Farm's ceiling stays at 2, `OpenJobsPage` runs its
   75 ms, and `/v1/verification/jobs` answers 200. The Builder stays paused
   until the memory work lands — which is the correct shedding order.

2. **`GET /v1/ops/pool-metrics` gains `runtime`.** `goMaxProcs`,
   `goroutines`, `memoryLimitBytes`, `memoryTotalBytes`, `heapLiveBytes`,
   `heapGoalBytes`, `gcCycles`, `gcLimiterLastEnabledCycle`, `gcCPUSeconds`,
   `totalCPUSeconds`, `gcCPUFraction`. Additive; nothing that reads the
   existing fields changes. This is the "before/after" instrument for the
   memory lane and the one reading that tells self-inflicted steal from a
   noisy neighbour.

3. **Runbook.** The governor table, the steal guidance (check `runtime`
   before "resize"), and the `runtime` field reference in
   `docs/operations.md`.

## The plan for the root cause (memory), for Chief to split

Each item is independently deployable and independently measurable through
`runtime.memoryTotalBytes` / `runtime.gcLimiterLastEnabledCycle` after this
PR. Ordered by bytes freed per line changed.

1. **Stop retaining the snapshot corpus for the record filters.** Replace
   `snapshotRows []SnapshotRow` with a compact per-row record built while
   streaming `ListSnapshots` (`purl`, `symbol`, environment fingerprints,
   `hasObservedStage`, `hasVerifiedStage`) and make `recordSnapshotMatches`
   read that instead of unmarshalling the document per request. Expected:
   −234 MB steady state, −468 MB at refresh peaks. Do not `Store` the whole
   corpus into `snapshotJSON` from this path.
2. **Bound `snapshotJSON`, `sampleArtifacts` and the per-package maps.**
   A sweep on the existing 30-minute TTL that *deletes* expired entries
   (today they are only replaced), or an LRU with a byte budget. At ~1
   crawler page/s, a 30-minute window is ~1,800 packages — tens of MB, not
   the corpus.
3. **`SnapshotUpdatedAt` and `SnapshotKeys` are per-purl / per-(purl,symbol)
   maps of the whole inventory.** Keep, but make sure they are not duplicated
   across `targetsRows` and `targetsIndex.rows` (they currently are the same
   slice — verify with the heap profile after 1 and 2).
4. **Builder memory.** After 1–3 have shrunk the server, re-measure the
   standalone Builder's pass against the 256 MiB cgroup limit; if a pass
   over the corpus genuinely needs more, that is a compose change
   (`CSX_BUILDER_GOMEMLIMIT`, `deploy.resources.limits.memory`) within the
   current 2 GB host, not a paid change.
5. **Only then** revisit the instance size. If, with the process well under
   its limit and the limiter never engaging, steal still holds ≥ 20% for
   minutes at a time, the runbook's original advice applies and it is an
   Owner decision.

Not in the plan: raising `GOMEMLIMIT`. The container is capped at 768 MiB
and the host has 84 MiB free; the process cannot be given the 816 MiB it is
using, and the collector working harder is the correct behaviour for a heap
that should not exist.

## Production acceptance (completed 2026-09-19)

Release `v0.1.199` and production deploy run `35429687395` activated
`ebde5fc4d120c23b122a48ae6ea14bbdfde65ac2`. The first post-deploy observer
failed during the restart/convergence window, so #485 remained open until a
fresh representative Farm observation could distinguish a real regression
from that transient result.

The fresh evidence at 11:06–11:11 UTC closes that gap:

1. `/version` returned `v0.1.199` / `ebde5fc4` before and after the probe;
   `/healthz` returned 200.
2. While the production Farm verifier was active, 20 consecutive
   `GET /v1/verification/jobs?peerId=...&limit=10` requests returned 200.
   Maximum TTFB was 0.816 seconds.
3. CodeSampleX-Farm evidence run `35439176130` recorded, for the 11:00 UTC
   hour, `queue_unavailable=0`, `receipt_completed=28`, `verdict_fail=0`.
   Farm health run `35439337257` then passed and reported a receipt at age
   zero, 133 completed jobs in 24 hours, and cleared the `server_errors`,
   `zero_gen_flow`, and stale-output alerts.
4. Two authenticated `/v1/ops/pool-metrics` reads 11 seconds apart showed
   `farm_ingest.limit=2`, `farm_ingest.busy=557` on both reads,
   `farm_ingest.timeouts=0`, and pool `inUse=1 → 0`, `idle=8 → 9`. This is
   the after-state for the pre-deploy reading (`limit=0`, `busy=168327`,
   pool `inUse=0`, `idle=11`, host steal 73.5%). The queue is no longer
   refused when the pool is idle.
5. The same after-state records `memoryTotalBytes=770884024` against
   `memoryLimitBytes=629145600`, `gcCycles=1575`,
   `gcLimiterLastEnabledCycle=0`, and `gcCPUFraction=0.0487`. That is the
   baseline for the separate memory lane; it is not a reason to re-open this
   verifier-admission incident.
6. True-saturation backpressure remains covered by
   `TestIntegrationGovernorPausesBuilderAndFarmIngestUnderPressure`: the
   `interactive-pool-pressure` branch still sets `farm_ingest=paused`.

## Sources

- Production probes 2026-09-19 02:28–02:45 UTC: `curl -w` TTFB splits,
  `/v1/ops/pool-metrics` with the admin credential, `ssh ubuntu@54.116.158.230`
  for `/proc/stat`, `/proc/<pid>/status`, cgroup `memory.stat` and `cpu.stat`,
  `docker logs`, `journalctl -k`, and read-only `psql` (`EXPLAIN (ANALYZE,
  BUFFERS)`, `pg_stat_activity`, corpus sizes).
- Farm-side evidence: CodeSampleX-Farm #163 / PR #186 (workflow 35346103976),
  scheduled health run 35411547311.
- Code: `cmd/csx-server/governor.go` (`decide`),
  `internal/serverstore/pool.go` (`acquire`, `farmCappedErr`),
  `internal/httpapi/api.go` (`writeStoreErr`), `cmd/csx-server/webstore.go`
  (the caches), `deploy/docker-compose.yml` (`GOMEMLIMIT`, memory limits).
