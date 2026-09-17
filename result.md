# Issue #440 Result: Deploy PurplePulse telemetry v2

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/440
- Branch: `issue/440-deploy-purplepulse-telemetry-v2`
- Milestone: v0.1.195
- Related: #439 (the change), #398 (Web PurplePulse v1), #174 / #433 (host
  pressure during builder convergence)

## Deployment outcome

PurplePulse telemetry v2 (`71f3b433577fd71783be4c299a1d28dfc3963740`, PR #439)
is live in production. The exact-SHA deploy this issue asked for ran on
2026-09-15 and passed every host phase; production has since moved forward
through v0.1.194-v0.1.197, every one of which contains the commit. Nothing
was re-dispatched by this lane: dispatching `71f3b433` again today would roll
production back from `ca6480e9` to an ancestor, which is not what the issue
asks for.

| Fact | Value | Source |
| --- | --- | --- |
| Previous production SHA | `dd9bdfd94fe201b2db90d9e7c9e8bf4477faae13` | production-deploy-evidence.json `previousProductionSha` (matches the issue) |
| Deployed SHA | `71f3b433577fd71783be4c299a1d28dfc3963740` (`deployedSha` = `targetSha` = `servedRevision`) | Production deploy run [35012410926](https://github.com/r2cuerdame/CodeSampleX/actions/runs/35012410926), `workflow_dispatch`, conclusion `success`, 2026-09-15T19:14:15Z-19:21:46Z; jobs `Production eligibility` and `Roll out production` both `success` |
| Release tag | `v0.1.193` (points at `71f3b433`) | `git tag --points-at`; migration ledger `releaseTag` |
| Release run | [35009867138](https://github.com/r2cuerdame/CodeSampleX/actions/runs/35009867138), `success`: Validate release tag ref, windows-test, build, sign, defender-scan, Clean Windows signed bootstrap, publish, Roll the farm | release.yml for `71f3b433` |
| Image digest | `sha256:819d52122a5ab9bedc4a77b6a6331d6dca04457696c0cc39e50220f3699a89b5` | deploy evidence |
| Migration | none. Ledger `0042_failure_cluster_page_idx.sql` (43) before and after; `migrationVerification: pass`; migration phase 6.169 s was verification only | offline-migration host ledger (matches the issue's "No database migration") |
| Offline migration phases | preflight 17.0 s, quiescence 7.9 s, migration 6.2 s, helperCleanup 8.1 s, migrationVerification, readiness, proxyReadiness, activation: all `pass`; host window 2026-09-15T19:19:58Z-19:21:26Z | host phase timings |
| Activation | `health: ok`, `smoke: pass`, `failureClass: none`, server started 2026-09-15T19:20:46Z | host acceptance |
| Rollback | `not-needed` | deploy evidence |
| Current production | `ca6480e9b706c47f756d8916e61d38a580261493` (`v0.1.197`, deploy run 35144143729, 2026-09-16T20:02Z), a descendant of `71f3b433` (11 commits later) | `/version` on 2026-09-17: `{"service":"csx-server","version":"v0.1.197","revision":"ca6480e9...","environment":"production","builtAt":"2026-09-16T20:03:39Z"}`; page footer commit |

### What is provably live

- Web: `https://codesamplex.dev/static/pulse.js?v=ca6480e` served HTTP 200 on
  2026-09-17 with sha256 `ac8d0d8d7dfea7cd45f584a920403f25a17d72e748d28bd382e9862cc28b06eb`,
  byte-identical to `internal/web/static/pulse.js` at `ca6480e9`, and line 165
  is `schema_version: 2` - the exact hunk #439 added. No commit after
  `71f3b433` touches `internal/web/static/pulse.js` or `internal/purplepulse/`.
- CLI/MCP: the v2 client ships in every release tag from `v0.1.193` onward;
  `v0.1.194`-`v0.1.198` all contain `71f3b433` (`git merge-base
  --is-ancestor`). The published stable update manifest
  (`csx-update-stable.json` on the `v0.1.197` release) advertises `v0.1.197`,
  so every self-updating install receives the v2 client. The `71f3b433`
  Release run's `Roll the farm` job passed, so the farm runs it too.

### Post-deploy observation: FAILURE, attributed to the next deploy

Observation run [35013168289](https://github.com/r2cuerdame/CodeSampleX/actions/runs/35013168289)
(2026-09-15T19:21:52Z-20:32:36Z, 103 samples) posted `FAILURE` /
`incident-only` on this issue with `Rollback requested: False`. Its
anomalies split into two groups:

1. Host pressure during builder convergence - pool-busy 8, query-timeout 43
   log lines, max DB-pressure wait 12.2 s, peak load 10.5, max active-builder
   TTFB 7.93 s, 0 active-builder 503s, `Builder errors: 0`, `OOM events: 0`,
   `Restart events: 0`. This is the known #174 / #433 post-restart condition
   on the 2-vCPU host and is not attributable to a telemetry client change.
2. `server container is not running`, `served /version revision does not match
   the deployed SHA`, `health is not ok`, `Container exit events: 1`. These
   come from the *next* deploy: run
   [35019545402](https://github.com/r2cuerdame/CodeSampleX/actions/runs/35019545402)
   (target `9116765a`, v0.1.194) started its offline migration at
   2026-09-15T20:31:05Z, stopped the `71f3b433` server
   (`serverStopStarted: true`, `quiescentAt: 20:31:34Z`,
   `previousProductionSha: 71f3b433`) and then failed in `helperCleanup` /
   `recoveryCleanup` (`failureClass: controller-unresolved`,
   `rollback: unknown-host-outcome`) - all inside the last two minutes of the
   #440 observation window. The `71f3b433` server ran uninterrupted from
   19:20:46Z until that stop. `9116765a` then deployed cleanly at 22:55Z
   (run 35033295563).

So the FAILURE verdict is real for the window but describes the #433 host
condition and an overlapping deploy, not a defect in this change. No
incident issue is needed for #440.

## Validation

All run on this branch head (identical tree to `origin/main` at `0c1b57e`
plus this `result.md`) on the Windows workstation, 2026-09-17.

| Check | Result | Evidence |
| --- | --- | --- |
| `go test -count=1 ./internal/purplepulse/... ./internal/web/ ./cmd/csx/...` via `run_observed_command` | PASS (`purplepulse` 0.53 s, `web` 41.1 s, exit 0) | tool output, `PROJECT_TEST` / `PASS` |
| `go vet ./internal/purplepulse/... ./cmd/csx/...` via `run_observed_command` | PASS | tool output, `PROJECT_COMPILE` / `PASS` |
| Web PurplePulse suite (`TestPurplePulseScriptRenderedOnAllPages`, `StaticAssetServed`, `BuildAttributes`, `UnstampedBuildRendersCleanly`, `ClientJSExecution` which runs `internal/web/pulse_test.js` under node v24.13.1) | PASS | `go test -run 'PurplePulse|Pulse' -v ./internal/web/` |
| PurplePulse unit tests: `TrackOncePerUTCDay`, `DisabledNetworkStillPersistsInstallID`, `ReleasePayloadV2OmitsEnvironment`, `FailedAttemptDoesNotRetrySameUTCDay`, `V1StateIsReusedWithoutChangingSameDayValues`, `OptOutAndEphemeralGuards`, `HelperPayloadValidation`, `HelperInvocation`, `PlatformForArgs`, `EnvironmentForVersion`, `OSMapping` | PASS | part of the package run above |
| CLI smoke on a `go build ./cmd/csx` binary from this head, every run under `DO_NOT_TRACK=1` so no ping can leave the machine | PASS (6/6) | `.tmp/440/cli-smoke.{sh,log}` (local, not committed) |
| Live asset check | PASS | served `pulse.js` sha256 equals the `ca6480e9` tree copy, `schema_version: 2` present |

The CLI smoke checks, in order: a fresh `CSX_HOME` gets `purplepulse.json`
on the first command with a v4-UUID `install_id` and, because telemetry is
opted out, no `last_attempt`; the file's sha256 is unchanged across further
commands; no `.purplepulse.lock.*` file is left behind; a pre-seeded v1
`purplepulse.json` (`install_id` + `last_attempt`) is byte-identical after
the v2 binary runs; the detached helper (`csx __purplepulse_send`) exits 0
and sends nothing when handed a non-v2 payload; the binary reports
`csx dev (git)`. The PurplePulse endpoint is a compile-time constant, so a
live send was deliberately not exercised here - the same choice #439 made
("No prod validation ping sent").

## Observations outside this issue's scope (no action taken)

- Eight `production-deploy.yml` runs for `2fcce190` (v0.1.198) failed on
  2026-09-17 between 11:09Z and 12:10Z. They belong to whichever lane is
  shipping v0.1.198 and do not affect the #440 outcome; production stayed on
  `ca6480e9` throughout.
- The `csx` installed on this workstation reports `v0.1.179` while the stable
  manifest advertises `v0.1.197`; if the launcher's self-update is expected
  to have moved it, that is a separate observation about this machine, not
  about the deploy.

## Deployment impact of this PR

None. This PR records delivery evidence only; it changes no runtime code and
no migration.
