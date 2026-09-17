# Issue #392 Result: Deploy anonymous analytics and admin retention

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/392
- Branch: `issue/392-deploy-anonymous-analytics-and-admin-retention`
- Milestone: v0.1.195

## Deployment outcome

The anonymous client analytics feature (`56922de`, deploy-gate acceptance
`503e98a`, migration `0040_anonymous_analytics.sql`) is live in production.

| Fact | Value | Source |
| --- | --- | --- |
| Previous production SHA | `8681328328ea73ca2fac6615dd78cd56d127c6bf` | production-deploy-evidence.json `previousProductionSha` (matches the issue) |
| Deployed SHA | `282df873a7319ce86444b3d01133390cbb7251d3` (contains `56922de` and `503e98a`) | Production deploy run [34811708620](https://github.com/r2cuerdame/CodeSampleX/actions/runs/34811708620), `workflow_dispatch`, conclusion `success`, 2026-09-14T06:00:56Z–06:09:03Z |
| Release tag | `v0.1.179` | migration ledger `releaseTag` |
| Migration ledger | `0039_report_review_notes.sql` (40) → `0040_anonymous_analytics.sql` (41) | offline-migration host ledger, `migrationVerification: pass`, elapsed 8.966 s |
| Offline migration phases | preflight, quiescence, migration, helperCleanup, migrationVerification, readiness, proxyReadiness, activation — all `pass` | host phase timings |
| New indexes valid+ready | `anonymous_clients_pkey`, `anonymous_clients_first_seen_idx`, `anonymous_clients_last_seen_idx`, `anonymous_client_days_pkey`, `anonymous_client_days_client_idx`, `anonymous_analytics_collection_pkey` | migration verification index list |
| Activation | `health: ok`, `servedRevision: 282df873…`, `smoke: pass`, `representativeSmoke: pass`, server started 2026-09-14T06:07:39Z | host acceptance |
| Rollback | `not-needed` | deploy evidence |
| Current production | `ca6480e` (v0.1.197, deploy run 35144143729, 2026-09-16) — a descendant of `282df873`, so the feature remains deployed | page footer commit + `/v1/stats` on 2026-09-17 |

Production has been collecting data since activation: on 2026-09-17T09:35Z
`anonymous_analytics_collection.started_at = 2026-09-14 06:07:03Z`,
`credential_adoption_started_at = 2026-09-14 10:48:13Z`, and
`count(*) FROM anonymous_clients = 42,996` (read-only psql audit).

Post-deploy observation run 34814076429 classified the deploy window as
`incident-only` (builder convergence pressure on the 2-vCPU host: pool-busy
refusals, one active-builder 503), with `Builder errors: 0`, `Restart events: 0`,
`OOM events: 0`, rollback not requested. That pressure is the known #174/#433
host condition and is not attributable to this change.

## Validation (issue checklist)

All run on this branch head (`df9d993`, identical tree to `origin/main` minus
#466) on the Windows workstation, 2026-09-17.

| Check | Result | Evidence |
| --- | --- | --- |
| `go test -p 1 ./...` | PASS (52 `ok`, 0 FAIL, exit 0) | `.tmp/392/verify/go-test-p1.log` (this run) and `.tmp/392/go-test.log` (uncached run earlier the same day: serverstore 251.9 s, web 40.5 s, evidence 41.1 s) |
| `go test -count=1` on the feature packages (`internal/admin`, `internal/anonymousclient`, `internal/httpapi`, `internal/identity`, `cmd/csx-server`, `cmd/csx-deploy-gate`) | PASS via `run_observed_command` | tool output |
| `go vet ./...` | PASS via `run_observed_command` | tool output |
| Admin Playwright, production (`scripts/admin-playwright.py --production`) | PASS at 2026-09-17T09:30Z | `.tmp/392/verify/admin-prod/result.json` → `{"production": true, "passed": true, "pageErrors": []}` plus dashboard/reports screenshots |
| Admin Playwright, local anonymous fixture (`TestServeAnonymousBrowserFixture` + `scripts/anonymous-admin-playwright.py`) | PASS: authenticated analytics, credential-adoption and activity time-series, retention/cohort charts, tab persistence, mobile overflow, server-rendered charts | `.tmp/392/verify/anon-local/anonymous-{desktop,mobile}.png` |
| CLI/MCP identity persistence smoke | PASS | `.tmp/392/verify/identity-smoke.{sh,log}`: fresh `CSX_HOME` → first `csx version` creates `identity.json` (196 bytes); sha256 unchanged after repeated CLI commands and after a `csx mcp` stdio session (`initialize` + `tools/list` answered); a second scratch home receives a distinct identity |

A second production admin Playwright attempt at ~2026-09-17T10:10Z failed on
its 5-second wait for `#report-status` while `/healthz` itself answered 503
with a 6 s TTFB (host under builder pressure at that minute). That is a
production-load timing failure, not evidence about this feature; the
09:30Z PASS stands and the run was not repeated to avoid adding load.

## Finding for Chief: production anonymous panel exceeds its 3 s budget

Not fixed here — outside a deploy issue's scope, reported for a split.

On production the `/admin` anonymous panel rendered
`익명 클라이언트 통계를 불러올 수 없습니다 (0이 아님)` at 2026-09-17T09:31Z
(`.tmp/392/verify/anon-prod/panel.html`). `internal/admin/admin.go` gives
the anonymous section a 3-second context. Read-only `EXPLAIN (ANALYZE,
BUFFERS)` of the two panel queries against the live database
(`.tmp/392/verify/anon-explain.{sql,out}`, 42,996 clients / 43,011 day rows):

- daily series (6 correlated sub-selects per `generate_series` day):
  execution 13.2 s, of which **JIT 9.77 s** (inlining 2.07 s, optimization
  4.41 s, emission 3.28 s, 51 functions); `SubPlan 4–6` seq-scan
  `anonymous_client_days` once per day.
- cohort retention (D1/D7/D30 `EXISTS` filters): execution 6.96 s, of which
  **JIT 5.44 s**.

Timings are inflated by the CPU-starved host (load 2.14 on 2 vCPU; a 734-buffer
seq scan took 4.2 s), but the JIT share alone crosses the 3 s budget. The
repository already has the pattern for this: `authoring_pg.go` sets
`set_config('jit','off',true)` next to `statement_timeout` for its large
candidate query. Suggested follow-up: disable JIT for the anonymous analytics
reads and collapse the per-day sub-selects into a single grouped aggregate
over `anonymous_client_days`, then re-measure on the production clone.
The local fixture (small dataset) does not reproduce this, which is why the
feature's Playwright coverage passes.

## Deployment impact of this PR

None. This PR records delivery evidence only; it changes no runtime code and
no migration.
