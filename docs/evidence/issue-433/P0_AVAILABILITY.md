# Issue #433 P0 availability evidence

## First production remediation

Before, the same deterministic 20-package set returned 1/20 HTTP 200 (5%) and
19/20 HTTP 503. The 2-vCPU host showed a run queue of 12–17, 0% idle, and
78–82% CPU steal. The deployed interactive DB pool was 2 connections with a
250 ms acquisition wait; pressure counters included 578 pool rejects and 22
statement timeouts. The broad package-page failure-cluster read was a concrete
hotspot, and background rebuilds ran for 8m36s–43m44s on a five-minute cadence.

Implemented and merged:

- #434 deployment recovery correctness: `8c0e9fdbb7fc39c417e5d381d7303fc856313ac1`
- #435 package-page query/index: `feadfb59fa713364c08c07def810e46fb3f0b4ac`
- #436 recovery lock health proof: `dd9bdfd94fe201b2db90d9e7c9e8bf4477faae13`
- #437 DB recovery verifier timeout envelope: `100449236335887af5bb0d9a63653e2224f2ef9e`

Release and deployment:

- Release: https://github.com/r2cuerdame/CodeSampleX/releases/tag/v0.1.192
- Release workflow: https://github.com/r2cuerdame/CodeSampleX/actions/runs/35001631665
- Farm rollout: https://github.com/r2cuerdame/CodeSampleX-Farm/actions/runs/35003588507
- Recovery: https://github.com/r2cuerdame/CodeSampleX/actions/runs/35005761028
- Production deployment: https://github.com/r2cuerdame/CodeSampleX/actions/runs/35006177462

Live `/version` is v0.1.192 at exact commit
`dd9bdfd94fe201b2db90d9e7c9e8bf4477faae13`; the server container is healthy
with restart count 0 and the deploy lock is absent. The migration ledger
contains `0042_failure_cluster_page_idx.sql` and the production index definition
is present. Runtime incident tuning was applied atomically with a rollback
backup: interactive reads 6, acquisition wait 3s, snapshot interval 1h.

After deployment, the exact same 20 URLs returned 20/20 HTTP 200 (100%), with
p50 1.898s, p95 4.458s, minimum 0.697s, and maximum 4.934s. Additional public
routes returned:

| Route | HTTP | Latency |
|---|---:|---:|
| `/` | 200 | 0.970s |
| `/samples` | 200 | 2.936s |
| `/dependencies` | 200 | 0.268s |
| `/compatibility` | 200 | 3.983s |
| `/v1/stats` | 200 | 0.292s |
| `/gaps` | 200 | 0.835s |

## Remaining P0 admin work

Authenticated `/admin` still returned 200 in 9.605s. An isolated
`/admin/api/reports` request returned 200 in 1.385s but exceeded the Playwright
5s bound during one full browser run. `/admin/api/farm` still returned 503.
Host contention remained severe immediately after deployment: steal 71–80%,
server 722 MiB/768 MiB, and DB 539 MiB/640 MiB. A bounded admin/farm fan-out
fix is being rebased and tested before a second deployment and repeated probes.

The independent Fable-high and Opus-high reports were frozen before
cross-validation. Full corpus remediation and clean Farm VERIFY continue after
admin availability is restored.

## Second production remediation in progress

Production v0.1.193 improved authenticated `/admin` from 9.605s to 5.512s and
`/admin/api/reports` from 1.385s to 0.749s, but `/admin/api/farm` still returned
HTTP 503 after 25.321s. `FarmBacklogNow` repeats the builder's
`authoringCoverageCTE` family; historical observations averaged about 10.4s and
reached 22.9s. Because the endpoint treated four independent whole-corpus reads
as one required result, one slow backlog scan discarded all completed data.

Two further P0 changes are in required CI:

- #442 skips physical PostgreSQL updates when snapshot JSONB differs only in
  its per-pass `generatedAt` clock. Disposable PostgreSQL 17 integration tests
  prove byte-identical, JSONB-equivalent, clock-only, and real-content-change
  cases while preserving the successful-pass stats clock and repair barrier.
- #443 gives the Farm HTTP refresh a 5s budget and a fixed-size last-good cache
  per section. Overlapping readers receive cache state, failed sections are
  `null` rather than misleading zeros, measurement ages are shown, and a failed
  core section prevents optional coverage fan-out.

Immediately before the combined release, the same 20-package set again returned
20/20 HTTP 200 despite severe host contention. Total latency ranged from 0.900s
to 6.382s. At the same time the two-vCPU host had load averages
5.19/6.10/5.78, CPU steal of 47--80%, server memory 724.9 MiB/768 MiB, and
continuing interactive admission refusals. This is the pre-deploy comparison
point for the combined fix; final measurements must separately prove route
success and reduced resource pressure.

## Failed v0.1.194 activation and availability containment

Canonical production deployment run `35019545402` completed its offline
migration but failed closed when post-helper cleanup found a PostgreSQL client
that it could not prove it owned (`application_name` empty, client
`172.18.0.2`). The deployment subsequently left the server service stopped and
live requests returned HTTP 502. Artifact `production-evidence-35019545402`
(artifact id `10416862861`) records the failure. The exact retained pre-deploy
image was restored as `latest` and only the Compose server service was started;
it reported healthy and served v0.1.193 at `71f3b43` again. The v0.1.194 target
image remains present and was not accepted as a successful deployment.

Recovery alone still reproduced the availability defect: a low-impact
12-route representative probe returned 9/12 HTTP 200. `/golang/github.com%2Fgoogle%2Fuuid`,
`/npm/react`, and `/pypi/sqlalchemy` returned 503, with observed latency as high
as 13.740s. In the same window, the server logged hundreds of interactive
admission refusals while the host reported 79--81% CPU steal. Container
measurements were approximately 496% CPU and 629.5 MiB/768 MiB for the server,
and 564% CPU and 378 MiB/640 MiB for PostgreSQL. PostgreSQL autovacuum workers
were active while the restarted v0.1.193 server ran an immediate full builder
pass.

As a reversible availability containment, the server was recreated without
changing the deployment `.env`, with Compose overrides
`CSX_SNAPSHOT_PASS_TIMEOUT=1s` and `CSX_SNAPSHOT_INTERVAL=24h`. The current
full pass and its five bounded retries all stopped in `list_targets` before any
materialization stage; the retry series then entered its documented 24-hour
deferred state. No schema or data deletion was performed. Afterward server
memory fell from 629.5 MiB to 162.4 MiB and its sampled CPU from 496% to 81%;
PostgreSQL fell from 564% to 294% while host steal remained an independently
unresolved 59--78% infrastructure constraint.

The same deterministic 20-package set then returned 20/20 HTTP 200 (100%),
with p50 1.015s and p95 4.314s. The slowest success was 7.555s. This is a
containment result, not final acceptance: the deployment cleanup ownership bug
must be fixed and reviewed, v0.1.194 must deploy through the canonical path,
and the live public/admin/Farm probe sets must be repeated after activation.

Authenticated post-containment probes also returned HTTP 200: `/admin` in
5.852s, `/admin/api/reports` in 0.200s, and two consecutive
`/admin/api/farm` calls in 25.090s and 16.969s. The repository's read-only
production Playwright admin regression passed. This proves restored success,
but the Farm endpoint remains unacceptably slow on v0.1.193; the bounded
section refresh/cache change already released in v0.1.194 still requires a
canonical production activation and post-deploy measurement.

An additional deterministic 50-page sample selected at equal intervals from
the 3,120 live package URLs in `/sitemaps/packages-1.xml` returned 50/50 HTTP
200. Its p50 was 0.156s, p95 0.294s, minimum 0.122s, and maximum 0.343s. This
directly reverses the reported 3/50 success condition for a broad package-page
set under the containment, while the fixed 20-page cold-path set above keeps
the slower uncached behavior visible.

Five minutes after the builder retry series deferred, the host had fully
settled: load average 0.37/1.90/3.26, 88--93% idle, and 3--4% steal. The server
used 11.30% sampled CPU and PostgreSQL 1.64%, versus the earlier multi-hundred
percent contention. This time-series change, together with the builder phase
logs, identifies the immediate causal bottleneck rather than treating the
earlier steal observation alone as proof of an external noisy neighbor.

## Canonical v0.1.194 activation

PR #446 was squash-merged as operational commit
`0c620cf9d6887c719d3e745732704b7babb78290`. Main CI run `35032203379`
passed the native Windows job and the Linux unit/contract, PostgreSQL
integration, and end-to-end pool-pressure jobs. The exact-head Fable rereview
of the deployment recovery changes returned PASS before merge.

The first post-merge dispatch (`35033004869`) was rejected before host access
because `additive-migration` was declared even though the target added no
migration. A corrected `safe` dispatch (`35033097992`) passed eligibility and
then stopped before mutation because the retained lock from failed run
`35019545402` still existed. The lock contained one owner UUID, no matching
deploy process existed, and GitHub showed no concurrent production workflow.
Only that exact owner file and the resulting empty lock directory were removed.

Canonical production deployment `35033295563` then succeeded. Live `/version`
reports v0.1.194 at exact revision
`9116765a834bca962418bcb19e2913f274778c47`; the host-accepted artifact records
healthy server and proxy smokes, the same 43-row migration ledger ending at
`0042_failure_cluster_page_idx.sql`, valid/ready required indexes, and no
rollback. The deploy lock was absent after completion. Measured phase durations
were 36.553 s preparation, 50.273 s staging, 9.951 s offline migration, 3.997 s
activation, and 13.538 s activation smoke.

Independent live probes after activation produced:

| Probe | Result | Latency |
| --- | ---: | ---: |
| Fixed cold-path package set | 20/20 HTTP 200 | p50 0.507 s, p95 1.691 s, max 1.932 s |
| Seeded package-sitemap sample | 50/50 HTTP 200 | p50 0.168 s, p95 0.307 s, max 0.449 s |
| Authenticated `/admin` | HTTP 200 | 5.536 s |
| Authenticated `/admin/api/reports` | HTTP 200 | 0.163 s |
| Authenticated `/admin/api/farm` (three calls) | 3/3 HTTP 200 | 0.106 s, 0.102 s, 0.108 s |

The read-only production Playwright baseline also passed through a CSX-observed
command. This changes the reported random-page outcome from 3/50 success to
50/50 and the deterministic cold-path result from 1/20 to 20/20. The Farm API
improved from 16.969--25.090 s under v0.1.193 containment to approximately
0.10 s after the bounded refresh/cache release.

The startup compatibility pass was still active during the first resource
sample: load average was 3.22/2.71/1.43, the server used 111.68% CPU and
676.3 MiB/768 MiB, and PostgreSQL used 38.73% CPU and 457.3 MiB/640 MiB.
Package availability nevertheless remained 100% in both probe sets. The pass
completed successfully in 8m16.734s; it covered 21,897 targets and 241,932
clusters, with no pool-busy event, no query timeout, and only 19 ms of target
evidence pool wait in the phase log.

The first post-pass resource sample showed load average 1.29/2.29/1.57, 90--94%
idle and 0% steal in the interval samples. Server CPU was 6.18% and PostgreSQL
CPU 0.35%; memory was 528 MiB/768 MiB and 480.1 MiB/640 MiB respectively. Only
two DB-pressure events appeared since activation, both background query
timeouts while the startup pass was active; the package-page probes had no
failure. A post-pass authenticated repeat returned 200 for `/admin` in 5.545s,
reports in 0.105s, and Farm in 0.271s. The remaining admin initial-render
latency and full corpus/Farm VERIFY are tracked as the next issue #433 work,
not as evidence that production availability is still impaired.
