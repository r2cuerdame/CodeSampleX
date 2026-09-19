# Issue #385 Result: authoring gates are retained at head; the receipt lane is what is down

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/385
- Branch: `issue/385-restore-farm-draft-production-retain-authoring`
- Related: #485 (root cause of the pressure and the memory plan), #486
  (governor fix, merged 2026-09-19, **not deployed**), #488 (boot schedule,
  merged, not deployed), #406-family (retained deploy lock)
- Measured: 2026-09-19 05:30–05:42 UTC, read-only, from this workstation
  (SSH to the production host, `psql` SELECTs, `/v1/ops/pool-metrics`)

## Verdict

The authoring recovery that #385 records (v0.1.173, 220def5e) is intact at
`origin/main` `ebde5fc4`: every gate named in the issue still has its test,
and every one of those tests passes on this workstation against PostgreSQL
(below). Farm **draft** production is also intact on the live server: drafts
landed every day since the proof draft (25 on 09-13, 100, 181, 178, 236, 112,
18 so far on 09-19 UTC), all `LOCAL_PASS`, all from `csx-farm-linux-1-slot1`.

What is not intact is the step after the draft. **No receipt has been written
since 2026-09-17 17:49:41 UTC** (35.8 h at measurement), so every draft since
then — 122 on 09-17, 112 on 09-18, 18 on 09-19 — is still quarantined, and
`verification_jobs` holds 254 `open` rows (oldest 2026-09-08, most since
09-17) plus 3 `claimed` rows whose claims will expire on their own. The
public corpus stopped at 7,432 samples (8,960 rows in `samples`).

The cause is the one #485 measured, still live because its fix is not
deployed. Production serves `v0.1.197` / `ca6480e9` (built 2026-09-16
20:03 UTC, container up 42 h). At 05:41:58 UTC `/v1/ops/pool-metrics`
read `host.stealPercent = 71.1`, `pool.classes[farm_ingest].limit = 0`,
`busy = 170425`, `runtime = null` (the old binary — the `runtime` section
only exists from #486). `GET /v1/verification/jobs` answered **503** from
this workstation at 05:38 UTC. The governor's last decisions in the server
log are all `reason=host-cpu-steal builder=paused farm_ingest=paused`
(02:40:03, 02:43:43 UTC), interleaved with
`governor could not pause the builder: ... context deadline exceeded` at
00:43, 01:12 and 01:19 UTC. `docker stats` showed `codesamplex-server-1` at
557 % CPU and 704 / 768 MiB — the self-inflicted GC load #485 describes. A
10 s `/proc/stat` delta on the host gave steal 1584 of 2073 jiffies (76 %).

The observer failure the issue keeps itself open for (run 34768100788, 4
query-timeout lines and 1 builder error in its window) is the same pressure,
not a separate defect: the observation artifact's first sample already had
`builder_error_events_before_observation = 1` and `settled_invariant_exit_code
= 1 / 22 s`, i.e. the settled-invariant SQL itself timed out on the loaded
host. On the current server the pattern continues: 286 `db pressure ... cause=
query_timeout` lines in 42 h — 167 on `/admin/api/farm class=background`,
97 on `/golang/golang.org/x/sys` (interactive), 8 on `/golang/golang.org/x/net`,
5 on `/dependencies`, 1 on `/v1/authoring/work/next` — beside 21,833
`interactive admission_refused` lines and 7,575 `farm_ingest pool_busy`
lines. Total counters: `pool_busy_total=178188`, `query_timeout_total=330`,
`admission_refused_total=471838`. The standalone Builder (`csx-builder`) has
been `paused by the resource governor; skipping passes` since 2026-09-18
19:48:40 UTC; `/v1/stats.generatedAt` is 2026-09-17T12:34:46Z.

## Why the fix is not live

Every deploy since 2026-09-17 11:51 UTC has failed with `remote script failed
(73) another deploy owns /opt/codesamplex/.deploy-lock` (six attempts for
`v0.1.198` / 2fcce190 on 09-17, runs 35216124491 … 35219599662). The lock is
still on the host: `/opt/codesamplex/.deploy-lock/owner =
97e81cc4431c45e78f819b84f709c53e`, mtime 2026-09-17 11:46 UTC, left by run
35217331550 after `host migration recovery unresolved; deployment lock
retained` (the rollback phase ended in 0.001 s). The lock-recovery workflow
refused it twice (runs 35219763913, 35220848167: `host phase sequence does
not prove a pre-migration rollback failure`). The two follow-up commits
(76e16a3 `stop builder container during offline migration`, 578b915 `allow
lock recovery when caddy rollback fails`) are on `main` behind the same lock.
No process on the host holds it (`pgrep -af deploy` is empty).

## What a Worker cannot do here, and the exact Source decisions

1. **Clear the retained lock.** The deploy's own message asks for it:
   "confirm it is no longer running, inspect owner, then remove only owner
   and the empty directory manually". That is a destructive operation on the
   production host and is not a Worker's to run:
   ```sh
   ssh -i ~/.ssh/lightsail-csx-r3 ubuntu@54.116.158.230 \
     'cat /opt/codesamplex/.deploy-lock/owner && \
      rm /opt/codesamplex/.deploy-lock/owner && rmdir /opt/codesamplex/.deploy-lock'
   ```
2. **Tag and deploy `main`.** `ebde5fc4` has green CI (run 35422698043).
   Release-tag push is a human gate:
   ```sh
   git tag -a v0.1.199 ebde5fc4 -m "v0.1.199" && git push origin v0.1.199
   # after the Release run for ebde5fc4 is green: production-deploy.yml,
   # target ebde5fc4, tracking issue 385
   ```
   Note `v0.1.198` (2fcce190) exists and was released but never went live;
   deploying `main` supersedes it.
3. **Accept that the deploy itself runs under the same pressure.** The
   09-17 attempt lost its offline migration on this host; #488's boot
   schedule and #486's governor are both in the image being deployed, but
   the deploy runs against the current process. Deploy while the site is
   idle (every restart costs a Builder pass on this host); expect the
   observer to report the #174/#433 pressure again in its window.

## Acceptance after the deploy (what would let #385 close)

- `/version` → revision `ebde5fc4...`; `/v1/ops/pool-metrics.runtime` is
  non-null (unforgeable proof the #486 binary is live).
- `pool.classes[farm_ingest].limit = 2` while `host.stealPercent` is still
  ≥ 20; `GET /v1/verification/jobs` → 200.
- `SELECT max(created_at) FROM receipts` moves past 2026-09-17 17:49:41 UTC
  and `SELECT count(*) FROM verification_jobs WHERE status='open'` falls
  from 254; the 252 quarantined drafts from 09-17..09-19 turn public as
  receipts land (`samples.quarantined = false`).
- The post-deploy observer's query-timeout and builder-error counts are then
  a #485 (memory) number, not a #385 number; if they are still non-zero,
  #385 hands them to #485 rather than staying open for them.

## Tests (retention proof at head)

2026-09-19, this workstation (Windows 11, Go 1.26.5, `CSX_TEST_DSN` on
127.0.0.1:5433, `CSX_REQUIRE_TEST_DSN=1` so PostgreSQL suites fail rather
than skip):

```text
go build ./...                                                          ok
go test -count=1 -run 'AuthoringAxis|AuthoringQuarantine|Quarantine|AuthoringWork|Farm' ./internal/serverstore/
ok  github.com/r2cuerdame/codesamplex/internal/serverstore      38.684s
  --- PASS: TestAuthoringAxisSwitchFake
  --- PASS: TestIntegrationAuthoringAxisSwitchPostgres (1.24s)
  --- PASS: TestAuthoringAxisRoundTripPreservesUnsupportedGate
  --- PASS: TestAuthoringAxisGatesSurviveReloadAndCompletion
  --- PASS: TestAuthoringAxisCooldownAndLegacyJSON
go test -count=1 -run 'Authoring' ./internal/httpapi/                   ok (28 PASS, 0 SKIP)
go test -count=1 ./internal/evidence/ ./internal/verifier/ ./internal/storage/localdb/ ./adapters/... ./internal/domain/
ok  internal/evidence 37.534s   ok  internal/verifier 1.493s   ok  internal/storage/localdb 1.278s
ok  adapters  adapters/goadapter  adapters/node  adapters/python  adapters/rust  adapters/unreal
ok  internal/domain
go test -count=1 -run 'Stats|Contention' ./internal/cli/ ./internal/daemon/   ok
```

These are the suites the v0.1.173 recovery added or changed (1b91691 axis
gates, cdb69a4 partial-scan recovery, 79b1c8c observer eligibility, 11d36ca /
e452e0f dependency leaves, 1a129f8 CLI companion delivery, 220def5 health
reads under writer locks). Nothing in this delivery changes code; the
result is the record that the gates hold at the current head and that the
remaining #385 symptom has a named owner and a named human action.

## Not done here, and why

- **No code change.** The only defects found are already fixed on `main`
  (#486, #488, 76e16a3, 578b915) or owned by #485's memory lane; a second
  fix for the same governor branch would be scope expansion.
- **No lock removal, tag or deploy.** All three are Source-gated
  (destructive host operation, release-tag push, production deploy).
- **No requeue or manual receipt.** The 254 open jobs need the queue to
  answer 200, not a ledger edit; the issue's own standard is that no manual
  draft, receipt, priority or ledger reset produces the result.
