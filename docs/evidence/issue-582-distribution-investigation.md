# Issue #582: distribution investigation (2026-10-16 UTC)

The reported change is reproducible, but the comparison is not between two
complete seven-day windows. No installation regression or cause of lost
presence has been established. This is investigation evidence, not an
implemented growth action, a completion decision, or a claim of recovery.

## Reproduce the reported comparison

The [captured evidence](issue-582-growth-baseline.json) contains only this
project's relevant observations and public endpoint checks. Its source is the
[GitHub Growth document at commit 268e86b](https://github.com/r2cuerdame/LoopOffice/blob/268e86b48b29632a866d576323eb748f07072149/.loopoffice/work/growth.json),
not a local projection. The nine available observation dates for
`installs.active_1d` are September 17 and October 9 through October 16.

| Group | Observation dates (UTC) | Daily aggregate values | Mean |
|---|---|---|---|
| Previous observations | September 17, October 9 | 5, 3 | 4 |
| Latest seven observations | October 10–16 | 3, 3, 3, 3, 2.6666666666666665, 2.7777777777777777, 3 | 2.9206349206349205 |

`(2.9206349206349205 / 4 - 1) * 100 = -26.984126984126988%`, which rounds
to the Issue's -27.0%. The prior group has two observations, separated by
22 days, rather than seven consecutive days. The September 17 source
observation was collected on September 25; it is still dated September 17 in
the source. There are no observations for September 18 through October 8 in
this series. Missing dates are not zero activity. October 16 is also a
partial UTC day, and the stored daily values average multiple intraday
snapshots of a counter that accumulates during the UTC day.

The October 9 daily value is already 3. The existing README distribution
change, [PR #566](https://github.com/r2cuerdame/CodeSampleX/pull/566), merged on
October 12 at 16:37:25 UTC. That change therefore cannot explain the earlier
5-to-3 difference. The target of 4 is the Issue's stipulated target; this
investigation does not silently replace it with 3.

## Follow the metric contract

At the inspected CodeSampleX baseline
`62aa84b98476c39898376b6db6b36ec538f6eb9e`:

1. `internal/daemon/presence.go:reportPresenceIfNeeded` derives three aligned
   epoch tokens from the installation identity, sends a schema-version-1
   `PresencePayload` to `POST /v1/presence`, and suppresses another successful
   report on the same UTC day. Community mode, a local identity and database
   are required. Errors fail open and do not mark a successful daily report.
2. `internal/domain/presence.go:PresencePayload.Validate` checks the version,
   class, client version, epochs and token shapes.
   `internal/httpapi/presence.go:handlePresence` decodes and validates that
   payload, then calls `RecordPresence` before returning accepted.
3. `internal/serverstore/pg.go:RecordPresence` upserts each
   `(interval_kind, epoch, token)`; its public count query includes only
   `ordinary` and `external` classes for the current epoch.
4. The public `activeInstallations1d` field is the source for the Growth
   series. The live response captured during this investigation reports 3
   for each of the 1d, 7d and 30d fields. Its generation time is recorded in
   the JSON evidence; this is one snapshot, not a completed follow-up window.

This contract counts anonymous presence tokens for the current UTC day,
not successful installs or verified people. Explicit farm/CI/operator classes
are excluded; the correctness of installations' self-declared classes is not
established. The separate anonymous-client analytics in
`docs/anonymous-analytics.md` use a different identity and request contract.
Neither should be substituted for this Issue's metric.

No edits to the presence producer, payload validator, endpoint, identity
derivation or PostgreSQL implementation appear in the inspected October
8–16 history. This does not rule out deployment failures, offline clients,
class changes or lost presence requests. No historical client-delivery
evidence here distinguishes those hypotheses from an actual reach decline.

## Existing distribution checks

Five read-only HTTP checks returned 200; their timestamps are in the JSON:
public stats, both platform installation scripts, `skill.md`, and the
existing MCP Registry's latest version endpoint. The Registry latest version
is `0.2.23`, matching the latest public GitHub release `v0.2.23`, published
October 14 at 08:27:49 UTC. The committed `server.json` version `0.1.0` is a
release template, not evidence that the live listing is stuck at that version.

These checks establish availability and matching version metadata only. They
do not execute an installer, prove historical availability, or identify the
cause of a counter change. No paid channel, new service, listing submission,
synthetic installation, identity reset or production change was performed.

## Local validation

- `go build ./...` passed through CSX `run_observed_command` (exit 0,
  `PROJECT_COMPILE`, `PASS`).
- `go test ./internal/identity ./internal/domain ./internal/daemon ./internal/httpapi -run Presence -count=1 -v`
  passed through the same tool (exit 0, `PROJECT_TEST`, `PASS`). These existing
  tests cover token rotation, payload validation, daily suppression,
  fail-open behavior and public-class exclusion; they do not prove the
  historical production delivery or PostgreSQL state.
- Offline Python assertions checked the captured dates, means, rounded
  percentage, five HTTP statuses, matching release versions and the local
  evidence link. `git diff --check` passed.

No product code changed and no regression fix is claimed. Installer execution,
historical production delivery, browser flows and post-closure recovery were
not verified by these checks.

## Decision needed before a growth action

The Issue requires identifying what changed before selecting a distribution
lever. The evidence identifies a misleading window comparison, but cannot
name a causal loss of installations. Repeating PR #566 or changing a working
listing would be an untested hypothesis rather than the requested causal fix.

Luna needs to decide whether to rebaseline the comparison using fresh,
comparable, completed UTC days and then authorize a named zero-spend
experiment, or decline the proposal as not planned. Any change to the Growth
producer/comparison in LoopOffice needs separately scoped authorization;
Worker #582 cannot modify that repository. This checkpoint neither changes
the success target nor closes the Issue.

Unmet delivery conditions are a supported cause and selected implemented
growth action, verification of that action, and a closing PR. Merge/closure
and the seven post-closure readings remain the loop's responsibility once a
valid action is delivered. A single live count of 3 is not a recovery result.
