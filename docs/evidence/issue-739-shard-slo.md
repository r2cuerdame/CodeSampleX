# #739 `/v1/shards/npm/zod/3` p95 violation: where the seconds are

## What the probe measured

Production `v0.2.23` / `b33f00ea4fd1303fcad45fd1224c29b27dcc7db4`, GitHub
`ubuntu-latest` vantage, run
[43551395439](https://github.com/r2cuerdame/CodeSampleX/actions/runs/43551395439)
at 2026-11-14T03:28:47Z. Seconds:

| measure | median | p95 |
| --- | ---: | ---: |
| server time | 3.554 | 10.000 |
| in-process (`Server-Timing`, 17 of 20) | 2.887 | 4.641 |
| outside the process | 0.512 | 1.235 |
| target | | 0.415 |

Three of the 20 requests failed with HTTP 429. Each counts as 10 s, so p95 is
10 s whatever the other samples were. The previous run (2026-11-13T03:29:05Z)
was also 10 s. Source: the #739 body.

## What this shows

Unlike `/v1/stats` in #716, this path spent most of its server time inside the
process: 2.9 s median. The deployed build, from 2026-10-14, reads the 151 KB
document from PostgreSQL on every request. It waits for the shared pool and
detoasts the JSONB each time. When the pool cannot admit the read in time, the
handler answers 429 `shard service busy` (`writeShardStoreErr`). That is the
probe's three failures.

`main` no longer does this. Production is 172 commits behind `main` (`68b4463`) and does not
run these changes:

- #639: the last document read is answered from memory for one builder
  cadence, revalidations included.
- #695: a document requested in the last fifth of its cadence is reread in the
  background, so a hot shard does not expire under steady traffic.
- #717: concurrent misses for one key share a single store read.

With these, the probe's 20 requests read the store at most once. The 429s
came from that per-request read.

## What changed for this Issue

On `main`, one per-request cost was left on this path. The CLI syncs with
`Accept-Encoding: gzip`, and the handler compressed the whole document again
for every sync. A remembered document now keeps its gzip body. It is
compressed once when it is read from the store. An unchanged reread keeps it.
Syncs and identity reads serve the stored bytes. The memory bound counts the
compressed body.

Local benchmark (`BenchmarkShardSync`, synthetic 159 KB shard, Ryzen 7 9800X3D,
200 iterations):

| one gzip sync | time/op | bytes allocated/op |
| --- | ---: | ---: |
| remembered gzip body (this change) | 6.9 µs | 21 KB |
| compressing per request (before) | 819 µs | 1.0 MB |

The synthetic document repeats more than the real one, so the real compression
cost is probably higher. On the 2-vCPU host, that CPU and allocation competed
with every public path. #545 measured `csx-server` as the dominant CPU user,
with 32–42 GCs per minute near `GOMEMLIMIT`. The SLO probe sends no
`Accept-Encoding`, so this change does not shorten its own request. It removes
compression load that CLI syncs put on the host the probe measures.

`TestRememberedShardIsCompressedOncePerRead` pins the count: one compression
for each store read, none for syncs, identity reads or an unchanged reread. It
also checks that the served bytes equal the streamed gzip body.
`TestShardCacheCountsCompressedBody` pins the memory accounting.

## What closes the Issue

The probe closes #739 after two consecutive passing runs against production.
Deploys are the owner's gate (`.loop/project.md`, `deployWorkflow:
not_required`), so neither the cache (#639, #695, #717) nor this change
reaches the probe until a deploy.

Even after a deploy, the outside-the-process time on this run was 0.512 s
median and 1.235 s p95. Both are above the 0.415 s target. #716 measured the
same pattern on routes that do no work. If the outside time stays after the
deploy, the remaining cause is host capacity. More capacity is a new cost and
needs owner approval. Changing the target is a decision #511 did not take, so
this change leaves it alone.

## Unknown

- Pool wait versus store-read split for this run. The Issue table prints only
  the sum of the `Server-Timing` phases.
- Whether the three 429s lacked `Server-Timing` (17 of 20 reported it) or
  carried a partial header.
- Host CPU steal, burst credit and production CLI sync volume at 03:28Z on
  2026-11-14. No host telemetry for that window is in the repository or the
  operations snapshot.
- The real zod shard's compression time on the production host.
