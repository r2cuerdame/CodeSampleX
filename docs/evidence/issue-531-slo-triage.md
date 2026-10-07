# Issue #531: public read latency triage (2026-10-05)

## Deployment attempt for phase timing (13:06 UTC onward)

Production still served `v0.2.1 / a6ae2ecb5900f8719e70bb23aabfadd49b4437aa`
at the start of this attempt; all four diagnostic routes returned 200 without
`Server-Timing`. The merged #532 instrumentation is on main commit
`560dfe0f28dce124085e98fd0b3ab128b4a19a9e`. We tagged that commit
`v0.2.2` to follow the repository's existing Release → Farm → Production
deploy path. Main CI [run 37309608306](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37309608306)
passed. Release [run 37314315137](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37314315137)
passed Windows tests, Linux tests/build, signing, security scan, Windows
bootstrap, and GitHub release asset verification. Its MCP Registry publish
step failed with HTTP 504 Gateway Time-out after roughly 60 seconds in both
attempts 1 and 2; the Farm job was skipped. A direct read-only request to the
Registry API also timed out, while its website root returned 200. On attempt
3, the Registry returned HTTP 400 `invalid version: cannot publish duplicate
version`, showing that `v0.2.2` had been accepted despite the lost response.
The same main commit was tagged `v0.2.3`; its normal Release
[run 37317101157](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37317101157)
passed, including an acknowledged Registry publish and verified
[Farm rollout](https://github.com/r2cuerdame/CodeSampleX-Farm/actions/runs/37319107246).

Production deploy
[run 37319704107](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37319704107)
passed eligibility but failed in the staging phase at the existing
`deploy/lightsail/deploy.ps1` release identity check: **"deployment revision
must have exactly one canonical release tag"**. Both `v0.2.2` and `v0.2.3`
point to the same main commit, so the check correctly refused to choose
between them. Its rollback succeeded, and the retained deployment evidence
reports `health: ok`, `servedRevision: a6ae2ecb5900f8719e70bb23aabfadd49b4437aa`
and `rollback: succeeded`. A fresh public `/version` check also returned the
prior `v0.2.1 / a6ae2ecb...` revision. No new server revision was activated.

The safe route to reconcile the public tags and release gate needs an owner
of deployment policy outside this latency Issue. Removing a published tag or
weakening the exact-release check here would expand the Issue and risks
serving an unverified download generation. Until instrumentation is deployed,
the required 30 paired external/`Server-Timing` samples per route cannot be
collected. The latency cause, RED→GREEN fix, and independent QA PASS are
therefore still unknown and unmet.

## Observation

The production SLO probe's 20-request runs on the unchanged
`v0.2.1 / a6ae2ecb5900f8719e70bb23aabfadd49b4437aa` deployment reported
the following server-time p95 values (seconds). The controls include
`/version`, which reads only process build stamps and does not access the
database, blob store, or clock.

| UTC date | `/healthz` (target .3731) | `/v1/stats` (target .3425) | `/v1/shards/npm/zod/3` (target .4155) | `/version` (target .3311) |
| --- | ---: | ---: | ---: | ---: |
| Oct 1 | .923 | .831 | .928 | .952 |
| Oct 2 | .526 | .449 | .612 | .453 |
| Oct 3 | .613 | .444 | .594 | .363 |
| Oct 4 | .340 | .421 | .541 | .518 |
| Oct 5 (GitHub Actions) | .449 | .449 | .531 | .437 |

Sources: the `github-actions` measurements in [#526](https://github.com/r2cuerdame/CodeSampleX/issues/526), [#527](https://github.com/r2cuerdame/CodeSampleX/issues/527), [#528](https://github.com/r2cuerdame/CodeSampleX/issues/528), and the unrelated static-route control [#529](https://github.com/r2cuerdame/CodeSampleX/issues/529). These are all the same GitHub Actions vantage and deployment revision. One 20-request p95 is the nineteenth ordered observation, so these repeated runs are stronger evidence than a single tail sample.

At 12:59 UTC on Oct 5, the same `scripts/perf-slo.py measure` ran for the
normal 20 rounds from this worker's Windows/KR vantage. Its [raw result](issue-531-live-probe-2026-10-05.json)
records the unchanged `v0.2.1 / a6ae2ecb...` deployment before and after,
20 successful requests per path, and server-time p95 of .3495 (`/healthz`),
.2042 (`/v1/stats`), .3883 (`/v1/shards/npm/zod/3`), and .2331 (`/version`)
seconds. All four were below their existing targets. This is a different
vantage from GitHub Actions and a later window, so it cannot establish a
production recovery. It does show that the same deployed revision can return
within target in a full run, while the GitHub Actions run at 10:40 UTC reported
four violations. All four routes had `appTimingCount: 0`, confirming that the
current production revision does not expose this branch's app timing yet.

I also ran the repository's read-only `scripts/perf-slo.py measure` on Oct 5
at 10:05 UTC from the worker's Windows/KR vantage, six rounds through the five
configured paths. The deployment was still `v0.2.1 / a6ae2ecb...` before and
after. All responses were 200. Observed median/p95 server seconds were:

| Path | Median | p95 | TCP RTT median |
| --- | ---: | ---: | ---: |
| `/healthz` | .305 | .745 | .248 |
| `/v1/stats` | .237 | .475 | .244 |
| `/v1/shards/npm/zod/3` | .422 | .716 | .259 |
| `/version` | .232 | .352 | .248 |
| `/v1/verification/jobs?...` | .435 | 1.284 | .252 |

Six samples make p95 the **maximum**, and this vantage differs from GitHub
Actions. This small run corroborates common-route latency; it is not a new SLO
verdict or a replacement for the 20-request production history.

## Route audit and limit

- `/healthz` uses the reserved probe database class and shares a successful
  health read for one second. The store call is `GetLatestStats`.
- `/v1/stats` keeps the builder's rollup in memory for its snapshot interval;
  repeated probe requests within that interval do not each query PostgreSQL.
  Its optional hot-shard hint is separately bounded.
- `/v1/shards/npm/zod/3` reads a materialized document by the shard table's
  primary key. ETag revalidation reads only the ETag, though this SLO probe
  sends an unconditional GET.
- `/version` computes a tiny JSON object from in-process build stamps. Its
  repeated p95 violations rule out a database query shared by the three
  target handlers as the sole explanation.

The shared delay could be host CPU/steal, Go scheduling, Caddy/upstream
queuing, or a network effect left in the probe's `request-to-first-byte minus
one TCP RTT` estimate. Public HTTP samples cannot distinguish those layers.
There is no established route-specific code defect to write a meaningful
baseline-RED/head-GREEN regression test for. Changing one of these handlers,
its latency budget, or the SLO target without that diagnosis would not
establish a fix.

## Evidence needed to resume

The diagnosis was split into [#532](https://github.com/r2cuerdame/CodeSampleX/issues/532).
Its [merged PR #533](https://github.com/r2cuerdame/CodeSampleX/pull/533)
adds `Server-Timing` phases (`middleware`, `db_wait`, `query_handler`,
`serialize`) for the four routes. This supersedes the earlier instrumentation
committed on this Issue branch; the branch will merge that implementation from
main rather than publish two overlapping timing layers. The full Oct 5 probe
still saw no app timing because production was serving `a6ae2ecb...`, not
PR #533. #532 is closed, but its requested production deployment and 30
paired samples per path have not been recorded on the Issue as of this check.

Until those paired samples exist, the route violations cannot be attributed
to a code, host, edge, or network defect with confidence; the `/version`
control argues against a database-only fix. A baseline-RED/head-GREEN
regression for an identified latency defect and independent QA PASS remain
outstanding for this Issue. The prior branch's timing-header tests established
instrumentation only, **not** a repaired p95 SLO.

Correlate the same probe windows with host CPU/steal and memory pressure,
server process CPU/GC/scheduler pauses, PostgreSQL pool wait and query times,
and Caddy upstream timing versus client-side TCP/TLS/request phases. Then
decide whether this is capacity/edge contention, a shared runtime defect, or
a probe-baseline policy problem. No production mutation or paid capacity is
needed for this read-only diagnosis.

## Resumed diagnosis after #534 (2026-10-05 16:06–16:40 UTC)

The [#534 fix](https://github.com/r2cuerdame/CodeSampleX/pull/535) was on
main at `a9fe24839bae5dfda10da04dcd7d6cb99e3674c7`. Its main
[CI run 37335864101](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37335864101)
passed. The new `v0.2.4` tag points at that exact commit. The normal
[Release run 37338225311](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37338225311)
passed Windows/Linux tests, build, signing, publication, and the verified Farm
rollout. The existing
[Production deploy run 37340865409](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37340865409)
passed eligibility and rollout. Its retained artifact reports target, deployed,
and served SHA all `a9fe2483...`, health `ok`, smoke `pass`, rollback
`not-needed`, and migration `0050_builder_status.sql`. Public `/version` then
returned `v0.2.4 / a9fe2483...`, so the four-route measurements below are on
the instrumented deployment, not on the earlier `v0.2.1` revision.

The worker's Windows/KR probe sent 30 successful GETs per route round-robin,
with a fresh TCP/TLS connection and 0.5-second spacing. It used the SLO
formula `request-to-first-byte - TCP connect` and captured the four
`Server-Timing` phases on **all 120 responses**. Raw per-request timestamps,
client timings, phases, and the before/after revision are in
[the 30-round result](issue-531-phase-probe-a.json). The independent
[GitHub Actions post-deploy run 37341878929](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37341878929)
used the existing 20-round SLO script on the same revision; its
[result artifact](issue-531-postdeploy-gha.json) is copied here. Values are
nearest-rank p95 seconds, except the application column, which is milliseconds:

| Route | Target s | Worker server s | Worker app ms | Worker residual ms | GitHub Actions server s |
| --- | ---: | ---: | ---: | ---: | ---: |
| `/healthz` | .3731 | .4286 | 237.796 | 271.165 | 1.0759 |
| `/version` | .3311 | .3267 | .050 | 326.667 | .2926 |
| `/v1/stats` | .3425 | .2733 | 76.930 | 272.252 | .3484 |
| `/v1/shards/npm/zod/3` | .4155 | 1.0494 | 400.154 | 277.065 | .7358 |

`app` is the sum of the instrumented `middleware`, `db_wait`,
`query_handler`, and `serialize` durations. `residual` is client-estimated
server time less that sum, per request. Each column's p95 may come from a
different request, so the columns must not be added. `/version` runs without
a store read: its app p95 was only 0.050 ms while its residual p95 was
326.667 ms. The residual can include Caddy/host scheduling and error in the
one-TCP-RTT network subtraction; this evidence does not separate them.

The database pool did not explain the store-backed tails: `db_wait` p95 was
0.003 ms for `/healthz` and 0.004 ms for the shard route. Their
`query_handler` p95 values were 237.785 ms and 400.052 ms, respectively.
`/healthz` calls `GetLatestStats` and reads the current stats document rather
than only checking connectivity. The shard route reads a materialized JSONB
document by primary key; this document returned 151,460 bytes. These facts
locate latency in the store read and its host execution, but do not establish
whether SQL, storage, CPU contention, or Go scheduling made it slow.

To check whether the full shard body alone causes the violation, a second
[30-pair result](issue-531-shard-pair.json) alternated unconditional GET
with `If-None-Match` against the same ETag. All 30 full responses were 200
with 151,460 bytes; all 30 revalidations were 304 with zero body bytes. The
full path's client/server-estimate p95 was .4639 s and app p95 167.995 ms.
Even the ETag-only path had .4539 s and 80.546 ms, with `db_wait` p95
0.003 ms. One full GET took 1.8601 s externally while app timing was only
3.647 ms. Thus reading/serializing 151 KB is not the sole cause, and the
primary-key ETag read plus transport still have substantial tails.

There is no isolated code defect yet for a baseline-RED/head-GREEN regression.
Changing the health check, caching the shard, or adjusting the SLO target
without isolating the source would leave the observed ETag and static-route
residuals unexplained. The running
[post-deploy observation 37341878738](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37341878738)
may provide host CPU-steal and pool-pressure evidence. PostgreSQL execution
time, host CPU/steal, Go scheduler pauses, and Caddy upstream timing were not
available to this read-only public probe. Independent QA PASS and the required
RED→GREEN regression are still unmet; a handler-only patch is not justified
by these measurements.

## Follow-up after the completed production observation (2026-10-05 18:31 UTC)

[Observation run 37341878738](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37341878738)
finished with a failure on the exact instrumented production SHA
`a9fe24839bae5dfda10da04dcd7d6cb99e3674c7`. Its retained
`post-deploy-observation.json` has 168 polls over 80 minutes. It records
server CPU peak 1016.78% (Docker's process-relative percentage, not a
measurement of host steal), memory peak 88.29% of the server container limit,
host one-minute load peak 6.71, four query-timeout log lines and one pool-busy
log line in the observation window, and maximum DB-pressure wait 4.968 s.
The container had no observed restart or OOM. The Builder never converged
and no active-Builder latency rounds were captured, so this run does not prove
that Builder work caused the public-route tails. The governor's host signal
was **unmeasured**: `pool_metrics_status=not-configured`,
`pool_metrics_host_error=true`, and `governor.maxHostStealPercent=null`.
The reported `0.0` steal field on individual polls is a placeholder in this
state, not a zero-steal measurement. The extended public-surface check also
timed out once (`curl` exit 28), while exact revision and health remained
stable. These readings show concurrent resource pressure; they do not
separate SQL, process scheduling, Caddy queuing, and hypervisor contention.

A fresh 20-round [public probe](issue-531-live-probe-2026-10-06.json) from the
worker's Windows/KR vantage, starting 18:31 UTC, remained on the same
production SHA with 20/20 HTTP 200 responses on every path. It found p95
server-time estimates of 0.3763 s for `/healthz` (target 0.3731), 0.294 s
for `/v1/stats` (target 0.3425), 0.515 s for
`/v1/shards/npm/zod/3` (target 0.4155), and 0.2251 s for `/version`
(target 0.3311). The route mix has shifted since the earlier 30-round probe;
the two current violations and two passes do not prove a durable recovery or
a path-specific fix. This script excludes TLS time and subtracts one TCP RTT
from request-to-first-byte, as verified in `scripts/perf-slo.py`.

The remaining discriminator is a **read-only, time-correlated host capture**
during a four-route probe: `/proc/stat` CPU-steal delta, per-container CPU and
memory/GC metrics, PostgreSQL statement and wait time for `GetLatestStats`
and shard ETag/full-document reads, and Caddy upstream duration versus public
TTFB. The existing protected Actions SSH identity can reach the host, but
the available production observation workflow cannot report steal without
the unconfigured `CSX_PRODUCTION_ADMIN_TOKEN`. At the time of that earlier
probe, this Worker had no direct approved SSH execution path; the later Luna
order enabled the one-window capture below. There is no
confirmed code defect to test RED→GREEN, and no measured evidence that a
paid capacity increase is necessary. A speculative handler, pool, or SLO
threshold change would not satisfy Issue #531's acceptance criteria.

## One-window protected SSH aggregate (2026-10-06 13:21:21–13:21:34 UTC)

Luna's #531 diagnostic order permitted one read-only, time-aligned capture
through the existing home-PC production SSH identity and pinned host key. The
served revision remained `v0.2.4 / a9fe24839bae5dfda10da04dcd7d6cb99e3674c7`.
The command read `/proc/stat`, `/proc/loadavg`, `/proc/meminfo`, one `docker
stats --no-stream` snapshot for server/Caddy/PostgreSQL, the line count of
Caddy's privacy-safe access log, and two bounded read-only PostgreSQL activity
aggregates. It sent one local-Caddy GET per fixed path, with no repeated load
probe, configuration change, service restart, or production data mutation.

| Signal | Same-window observation |
| --- | ---: |
| Host CPU steal, `/proc/stat` delta | 78.10% |
| Host 1-minute load | 2.07 |
| Host memory available / total | 359,932 / 1,951,768 KiB |
| Server container CPU / memory limit | 761.29% / 96.80% |
| Caddy container CPU / memory limit | 33.73% / 2.49% |
| PostgreSQL container CPU / memory limit | 5.75% / 62.60% |
| Privacy-safe Caddy log lines | 31,336 (count only) |
| PostgreSQL before and after | active 1, wait-event 12, lock-wait 0 in both snapshots |

The `wait-event` count includes idle backends and is **not** a count of active
queries waiting. The snapshot has no statement durations or query text. The
Caddy log line count proves only that the safe log was readable; the log omits
`/healthz` and `/version`, so it cannot provide four-route timing.

| Fixed path | HTTP | Local TCP connect s | TLS complete s | Local Caddy TTFB s |
| --- | ---: | ---: | ---: | ---: |
| `/healthz` | 200 | .000155 | .398520 | .886729 |
| `/version` | 200 | .000150 | .232007 | .392218 |
| `/v1/stats` | 200 | .000172 | .174121 | .553111 |
| `/v1/shards/npm/zod/3` | 200 | .000121 | .312356 | .709992 |

The requests used `curl --resolve codesamplex.dev:443:127.0.0.1`; each TTFB
starts before local TCP/TLS setup. The curl header formatter returned no
`Server-Timing` field, so app phases and Caddy upstream wait were not measured
in this capture. These four single requests are diagnostic samples, not p95
estimates or a new SLO verdict. They may also induce the application's
periodic demand-telemetry writes.

The large contemporaneous CPU steal, near-limit server-container memory, and
static `/version` latency support host scheduling/resource contention as the
leading shared hypothesis. The measurements do not isolate hypervisor
contention, process scheduling, Caddy upstream time, or a code defect. They
also cannot establish that paid capacity is required. No handler change or
threshold adjustment is justified, and there is still no baseline-RED/head-
GREEN regression to write for an identified defect. Independent QA PASS and
the Issue's latency-fix done condition remain unmet. Luna must decide the next
operational diagnostic or capacity path; this worker cannot deliver a truthful
fix PR on the present evidence.

## Three spaced protected SSH samples (2026-10-06 13:31–13:32 UTC)

The read-only [sample script](issue-531-host-sample.sh) ran through the existing
home-PC SSH identity and pinned host key. Each steal value is a five-second
`/proc/stat` delta; the starts were 35 and 25 seconds apart. The script read
host load and memory, Docker's process/container summaries, the server
process's `/proc/1/status`, cgroup memory composition/events, and the public
HTTP status of the existing ops endpoint from inside the container. It made no
public-route requests, restarted no service, changed no host setting or data,
and did not read a credential. The Windows PowerShell-to-SSH pipe appended a
carriage return after the final script line, so the three invocations exited
1 *after* printing all measurements; the script's final comment now absorbs
that transport artifact. This exit does not indicate a collector failure.

| Sample start UTC | Host steal | Load 1m | Server memory/768 MiB | Server anonymous RSS | Server swap | Cgroup anonymous / file | Restart / OOM-kill |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| 13:31:15 | 50.89% | 4.26 | 728.9 MiB / 94.90% | 733,768 KiB | 73,956 KiB | 751,702,016 / 10,928,128 B | 0 / false |
| 13:31:50 | 76.23% | 3.26 | 729.2 MiB / 94.94% | 737,936 KiB | 81,324 KiB | 756,764,672 / 10,846,208 B | 0 / false |
| 13:32:15 | 77.73% | 2.70 | 736.7 MiB / 95.92% | 745,484 KiB | 79,756 KiB | 764,579,840 / 10,899,456 B | 0 / false |

The only substantial process listed inside the server container was
`csx-server` (roughly 725–736 MiB RSS); Caddy and PostgreSQL were separate
containers. Server memory was dominated by anonymous memory, not file cache.
The cgroup's cumulative `oom` and `oom_kill` counters were both zero at each
sample, and Docker reported zero restarts since the server started at
2026-10-05 16:33:28 UTC. The server still has `GOMEMLIMIT=600MiB` configured.
These measurements rule out page cache as the reason Docker reported ~95%
memory, but `/proc` cannot partition the anonymous bytes into live Go heap,
free heap, stacks and other allocations. The protected runtime endpoint
returned HTTP 401 without an admin credential, so live `heapLiveBytes`,
`memoryTotalBytes`, `gcCPUFraction`, and GC limiter state were not obtained.
Historical [#485 diagnosis](../issue-485-verifier-queue-root-cause.md) traced
similar anonymous memory and swap to unbounded in-process caches. The current
source still retains `snapshotRows` and `snapshotJSON`, but their present heap
sizes and contribution to this window were not measured. This is a concrete
code hypothesis, not a verified current defect or evidence that capacity alone
is the remedy.

For correlation, the original alert timestamps were #526 `/healthz` and
#527 `/v1/stats` at **2026-09-28 09:58:45 UTC**, #528
`/v1/shards/npm/zod/3` at **2026-09-30 09:51:56 UTC**, and the added #529
`/version` at **2026-10-01 10:19:52 UTC**. Those alerts recorded no host steal
sample at their exact times. The latest read-only [GitHub Actions SLO artifact](issue-531-latest/perf-slo-result.json)
for run [37450108173](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37450108173)
was measured at **2026-10-06 10:37:04 UTC** on the same `v0.2.4 / a9fe2483`
revision, about three hours before these host samples:

| Route | Latest server-time p95 | Target | Status |
| --- | ---: | ---: | --- |
| `/healthz` | 10.0000 s (3 errors / 20) | .3731 s | violation |
| `/v1/stats` | 6.3655 s | .3425 s | violation |
| `/v1/shards/npm/zod/3` | 10.0000 s (6 errors / 20) | .4155 s | violation |
| `/version` | 3.2664 s | .3311 s | violation |

The alert history and the newer SLO artifact show persistent public
violations, while the three current host samples establish sustained severe
steal over one minute. They are **not simultaneous route/host measurements**;
these data cannot calculate a contemporaneous steal-versus-p95 correlation.
Memory stayed above the ordered 80% bound throughout the capture, and all four
latest p95 values were above target. A RED→GREEN regression, code fix, QA PASS,
and a post-fix production measurement are unmet. A capacity-only conclusion
would be premature while the server's anonymous memory is near its cap and
its present Go heap/GC state is unknown.

## Protected runtime and historical host-series follow-up (2026-10-06 13:44–13:47 UTC)

The pinned LoopOffice `luna-operating-harness@v17` was re-read from GitHub
commit `96f85d81b10184f466d6cf78f0c7851789d7d38b`. Its file SHA-256 is
`65772ce5a2078d776af050ee3ebe5a6a968cb2e9ef44d18da91fc9f6854ba597`,
matching this dispatch. This follow-up used the existing home-PC SSH identity
and pinned host key only for bounded read-only commands. It created no load
probe, changed no host setting or production data, and did not read or print
credential values.

Inside `codesamplex-server-1`, the running environment has
`CSX_ADMIN_TOKEN_SHA256` but no raw `CSX_ADMIN_TOKEN` or
`CSX_PRODUCTION_ADMIN_TOKEN`. A loopback GET to
`http://127.0.0.1:8080/v1/ops/pool-metrics` returned HTTP 401. The configured
digest cannot authenticate a request; there is no usable raw operator
credential in those service environment variables. The ordered fallback was
therefore process RSS and cgroup composition. The 13:45:09 UTC five-second
sample reported host steal **72.82%**, server memory **588.4/768 MiB
(76.62%)**, `RssAnon=592,900 KiB`, `VmSwap=229,372 KiB`, cgroup
`anon=608,026,624 B` and `file=11,358,208 B`. Server restart count and
OOM-kill count were zero. A subsequent read showed cgroup
`anon=619,745,280 B`, `file=11,694,080 B`, process
`RssAnon=605,148 KiB`, and `VmSwap=217,940 KiB`. These reads cannot split
anonymous memory into live Go heap, retained cache, free heap, or stacks, and
there was no same-time route p95. The first collector printed its complete
measurements but exited 127 after the final line because PowerShell appended
a carriage return to SSH stdin; the next collector ended cleanly.

The host retains `/var/log/sysstat/sa28`, `sa30`, `sa01`, `sa05`, and `sa06`.
`sar -u` gives ten-minute CPU intervals ending at the shown record time;
`sar -r` gives host memory snapshots at those endpoints. The original alert
Issues supply the runner-side p95, not local Caddy p95:

| Alert route and UTC time | Runner p95 / target | Host `sar` interval end | Host steal over preceding interval | Host available memory at interval end |
| --- | ---: | --- | ---: | ---: |
| [#526](https://github.com/r2cuerdame/CodeSampleX/issues/526) `/healthz`, Sep 28 09:58:45 | .443 / .373 s | Sep 28 10:00:03 | 72.88% | 498,424 KiB |
| [#527](https://github.com/r2cuerdame/CodeSampleX/issues/527) `/v1/stats`, Sep 28 09:58:45 | .352 / .343 s | Sep 28 10:00:03 | 72.88% | 498,424 KiB |
| [#528](https://github.com/r2cuerdame/CodeSampleX/issues/528) `/v1/shards/npm/zod/3`, Sep 30 09:51:56 | .731 / .415 s | Sep 30 10:00:03 | 76.08% | 464,080 KiB |
| [#529](https://github.com/r2cuerdame/CodeSampleX/issues/529) `/version`, Oct 1 10:19:52 | .952 / .331 s | Oct 1 10:20:03 | 74.79% | 362,732 KiB |

The latest [20-round Actions run 37450108173](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37450108173)
at Oct 6 10:37:04 UTC had p95 of 10.0000, 6.3655, 10.0000, and 3.2664 s
in that route order; the enclosing `sar -u` interval ending 10:40:02 had
78.16% steal, with 384,548 KiB host memory available at its endpoint. These
coarse host intervals establish severe steal during the original alerts and
the latest violation. They do not measure request-time steal or historical
server-container memory, and they cannot establish that steal alone caused
each request tail.

The running Caddy configuration logs only fixed API route labels, status,
method bucket, and timestamp. It excludes `/healthz` and `/version`, removes
the entire request field (hence the specific shard path), and explicitly
deletes `duration`. Forty-two privacy-safe log files are retained, but none
can provide the three paths' historical Caddy p95. The existing Actions
artifacts provide runner-side p95 instead; no recorded same-window Go heap/GC
series is available. In particular, the lower present cgroup memory than the
prior ~95% samples and high present swap do not establish a flat heap.

Host scheduling contention remains the leading shared hypothesis. The
ordered discriminator—heap/anonymous-memory movement **with** route p95,
versus flat heap **with** steal/p95—cannot be evaluated from the retained
signals. A cache defect and a host-only capacity problem are both unproven;
no speculative code change, paid capacity request, or SLO threshold change
follows from these data. The Issue still lacks a baseline-RED/head-GREEN fix,
independent QA PASS, and post-fix p95 verification. Luna must choose a next
diagnostic path that can collect time-aligned, privacy-safe runtime and route
timings, or decide how to handle the unavailable evidence.

## v0.2.5 release gate for the instrumented diagnostic (2026-10-06)

Luna's one-attempt rollout order targeted main commit
`18d8c881b66ae77a3e1e998902d0b4c6011f2c0d` (merged #539). Immediately
before tagging, GitHub's compare `v0.2.4...18d8c881` reported exactly one
commit, `v0.2.4` peeled to
`a9fe24839bae5dfda10da04dcd7d6cb99e3674c7`, no `v0.2.5` ref existed,
and the release-run list had no successful run for the target. The pinned
`luna-operating-harness@v17` was re-read from GitHub commit
`96f85d81b10184f466d6cf78f0c7851789d7d38b` and its SHA-256 matched
`65772ce5a2078d776af050ee3ebe5a6a968cb2e9ef44d18da91fc9f6854ba597`.

One annotated `v0.2.5` tag was created at the target and pushed. The normal
tag-push [Release run 37498360867](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37498360867)
passed Windows tests, Linux tests/build, signing, Defender scan, and the
published asset verification. It published the GitHub release, then failed in
`Publish to the MCP Registry` at 2026-10-06 17:01:37 UTC. The original error
line was:

```text
Error: failed to get token: failed to exchange OIDC token: failed to send request: Post "https://registry.modelcontextprotocol.io/v0/auth/github-oidc": dial tcp 34.61.200.254:443: i/o timeout
```

The run ended `failure` with exit code 1. Its `Roll the farm` job was skipped,
so no farm rollout was verified and production deploy eligibility was not
established. The prior blocked deploy run `37496519668` was not replayed; no
new deploy was dispatched. The public `/version` read before this release
reported `v0.2.4 / a9fe24839bae5dfda10da04dcd7d6cb99e3674c7`; no
post-deploy version value exists. The one permitted protected SSH diagnostic
window was not entered. No release dispatch retry, tag rewrite, manual farm
operation, or host change was attempted.

This gate failure leaves the issue's route diagnosis, RED→GREEN regression,
fix, independent QA PASS, and post-fix p95 verification unmet. Luna must
decide a recovery path for the already published tag and failed registry
publication while preserving the one-attempt order.

## v0.2.5 failed-publish rerun preflight (2026-10-06 17:21 UTC)

The pinned `luna-operating-harness@v17` was re-read from GitHub's
`r2cuerdame/LoopOffice` commit `96f85d81b10184f466d6cf78f0c7851789d7d38b`;
SHA-256 was `65772ce5a2078d776af050ee3ebe5a6a968cb2e9ef44d18da91fc9f6854ba597`,
matching the dispatch. This was a read-only preflight under Luna's single
`rerun-failed` recovery order. The registry lookup did not return, so the
ordered rerun was **not** invoked.

Preflight (a) raw GitHub observations:

```text
tag v0.2.5 -> tag object 46d362e3d7c87ba94028b7fc73b5fc7ffd54ac49
tag object -> commit 18d8c881b66ae77a3e1e998902d0b4c6011f2c0d
Release run 37498360867: event=push, headBranch=v0.2.5,
headSha=18d8c881b66ae77a3e1e998902d0b4c6011f2c0d,
path=.github/workflows/release.yml, run_attempt=1, conclusion=failure
```

The workflow was read at that exact commit. Preflight (b) raw REST response
projection:

```text
GitHub release v0.2.5: draft=false, prerelease=false
assets: codesamplex-mcp.mcpb, codesamplex-mcp.mcpb.sha256,
csx-bootstrap-stable.json, csx-darwin-amd64, csx-darwin-arm64,
csx-launcher-windows-amd64.exe, csx-launcher-windows-arm64.exe,
csx-linux-amd64, csx-linux-arm64, csx-server-linux-amd64,
csx-update-stable.json, csx-windows-amd64.exe,
csx-windows-arm64.exe, SHA256SUMS.txt
```

The pinned workflow's `Create GitHub release` step uploads with `--clobber`
only when `isDraft=true`, creates a release only when absent, and skips both
paths for the existing published release. `Atomically publish the verified
draft` edits only when `isDraft=true`. Thus the inspected workflow has no
published-asset overwrite path. Run 37498360867 failed in `Publish to the
MCP Registry`; `Roll the farm` was skipped.

Preflight (c) attempted the documented public query
`GET https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.r2cuerdame/codesamplex&limit=100`
with a 30-second timeout. Raw local result:

```text
Invoke-RestMethod: The request was canceled due to the configured HttpClient.Timeout of 30 seconds elapsing.
```

Registry version 0.2.5 is **unknown**, not absent. The conditional permission
to run `gh run rerun 37498360867 --failed` is therefore unsatisfied. There
was no rerun attempt, Farm dispatch, Production dispatch, host operation, or
code change. The earlier original publish failure remains the only recorded
attempt. The Issue's route cause and remedy, baseline-RED/head-GREEN regression,
independent QA PASS, and post-fix production p95 verification remain unmet.
Luna must decide a recovery path after an authoritative Registry read succeeds.

## v0.2.5 public Registry status check (2026-10-07 UTC)

Under Luna decision `DLG-20261007-113:4`, this session made exactly three
unauthenticated, read-only GET attempts against the public MCP Registry. Each
request had a 45-second timeout (below the 60-second limit); retries used
2-second then 4-second backoff. The pinned `luna-operating-harness@v17` was
re-read from `r2cuerdame/LoopOffice` commit
`96f85d81b10184f466d6cf78f0c7851789d7d38b` and its SHA-256 matched
`65772ce5a2078d776af050ee3ebe5a6a968cb2e9ef44d18da91fc9f6854ba597`.

| Attempt | Public GET | HTTP | Timeout | Result |
| --- | --- | --- | --- | --- |
| 1 | `/v0.1/servers?search=io.github.r2cuerdame/codesamplex&limit=100` | No response | Yes, at 45 s | `TaskCanceledException` from the configured HTTP timeout. |
| 2 | Same search | 200 | No | 100 versions returned, with `metadata.nextCursor=io.github.r2cuerdame/codesamplex:0.1.20`; this first page cannot establish whether 0.2.5 exists. |
| 3 | `/v0.1/servers/io.github.r2cuerdame%2Fcodesamplex/versions/0.2.5?include_deleted=true` | 404 | No | Exact version not found, including deleted versions. |

The [Registry's public API reference](https://github.com/modelcontextprotocol/registry/blob/main/docs/reference/api/official-registry-api.md)
documents the exact-version GET and `include_deleted` option. The search
request established that the named server exists, while the exact-version
request returned 404. **Classification: `not_registered` for v0.2.5.** No
publish, repost, login, credential use, release rerun, Farm or Production
dispatch, or Registry mutation followed. Luna must choose the recovery step.

This status check does not resolve the four-route latency cause. The Issue's
RED-to-GREEN regression test, fix, independent QA PASS, and post-fix p95
verification remain unmet.

## v0.2.5 publication preflight and four-route probe (2026-10-07 21:20 UTC)

The `luna-operating-harness@v17` GitHub source was re-read at LoopOffice commit
`96f85d81b10184f466d6cf78f0c7851789d7d38b`; its SHA-256 again matched
`65772ce5a2078d776af050ee3ebe5a6a968cb2e9ef44d18da91fc9f6854ba597`.
The issue workspace was clean on `issue/531` before this observation.

Under decision `DLG-20261007-114:2`, a fresh unauthenticated public Registry
GET for `io.github.r2cuerdame/codesamplex` returned HTTP 200 and 100 entries
on its first page. Entries for versions `0.1.0`, `0.1.1`, `0.1.10` and
`0.1.100` all had that exact server name and `active` official status.
Thus **earlier versions were registered**; v0.2.5 is a routine subsequent
version, not this server's first public publication. A separate exact-version
GET to `/v0.1/servers/io.github.r2cuerdame%2Fcodesamplex/versions/0.2.5?include_deleted=true`
returned HTTP 404. **v0.2.5 is `not_registered` at this preflight.**

The already published GitHub release `v0.2.5` has its expected 14 assets;
the failed Release run `37498360867` had passed all release verification
steps and failed at `mcp-publisher login github-oidc`. The repository's
existing `.github/workflows/release.yml` has no Registry-only dispatch: its
`publish` job runs the Registry command with GitHub Actions OIDC, and the
downstream `Roll the farm` job depends on `publish`. `gh run rerun --failed`
or `--job publish` would rerun that job including dependencies and could
continue to the Farm job after publication. This does not meet the current
order to register **only** v0.2.5 metadata. No local GitHub Actions OIDC
credential exists outside that job. No rerun, login, publish, release asset
write, farm dispatch or production deployment was attempted. Luna must choose
a Registry-only execution path or explicitly permit the existing Release
continuation before this worker can publish.

The repository's read-only SLO probe ran 20 rounds against exactly the four
requested public paths from the worker's Windows/KR vantage. The
[configuration](issue-531-latest/four-route-config.json) and
[raw result](issue-531-latest/four-route-2026-10-07-utc.json) retain all
samples. `/version` before and after the probe reported the unchanged
`v0.2.4 / a9fe24839bae5dfda10da04dcd7d6cb99e3674c7` deployment.

| Path | Server-time p95 | Target | HTTP results |
| --- | ---: | ---: | --- |
| `/healthz` | 3.9569 s | 0.3731 s | 19 × 200, 1 × 503 |
| `/version` | 2.2860 s | 0.3311 s | 20 × 200 |
| `/v1/stats` | 1.0221 s | 0.3425 s | 20 × 200 |
| `/v1/shards/npm/zod/3` | 3.8339 s | 0.4155 s | 20 × 200 |

All four paths violated their p95 targets in this window, including the
in-process `/version` control. Together with the prior 72–78% host CPU steal
observations, shared host scheduling/resource contention remains the leading
candidate. The new probe has no simultaneous host or Go heap metrics, and
the 503's specific cause was not measured. It cannot prove a host-only cause
or justify a speculative code patch. Baseline-RED/head-GREEN regression,
independent QA PASS, and post-fix p95 recovery remain unmet.
