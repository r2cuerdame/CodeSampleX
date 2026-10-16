# Issue #583: active-installation investigation

Investigation date: 2026-10-16. This is an evidence checkpoint, not an
implemented growth action, completion record, or claim of recovery.

## Sources and revision

- [Issue #583](https://github.com/r2cuerdame/CodeSampleX/issues/583).
- CodeSampleX baseline: `62aa84b98476c39898376b6db6b36ec538f6eb9e`, branch
  `issue/583`; the checkout was clean before this document was added.
- [GitHub growth ledger](https://github.com/r2cuerdame/LoopOffice/blob/loopoffice-work-state/.loopoffice/work/growth.json),
  read live through the GitHub API. Selected records:
  `state.signals`, project `CodeSampleX`, metric `installs.active_1d`.
  These are recorded observations; this document does not become a work ledger.
- [Project decision ledger](https://github.com/r2cuerdame/LoopOffice/blob/loopoffice-work-state/.loopoffice/work/pipeline-436f646553616d706c6558.json),
  read live: dispatch receipt `["receipt","CodeSampleX",583,"implement",978]`
  is on `issue/583`; classification is standard / standard QA / feature / growth.
- [Luna operating harness v18](https://github.com/r2cuerdame/LoopOffice/blob/7cbe6b6e923df1032f060874408c21e3d85a3b33/skills/luna/SKILL.md):
  downloaded at that immutable commit; SHA-256 verified as
  `653288f6882b7ae87eb9d6bd255d83ed2bf77f33eb1e434ffbaee309d5456fdd`.

## Reproduction of the proposal's numbers

Group observations by their source `at` UTC date, average values within each
date, then average those daily means. There are 94 observations across nine
observed dates:

| Source UTC date | Observations | Daily mean | Minimum | Maximum |
|---|---:|---:|---:|---:|
| 2026-09-17 | 1 | 5 | 5 | 5 |
| 2026-10-09 | 4 | 3 | 3 | 3 |
| 2026-10-10 | 8 | 3 | 3 | 3 |
| 2026-10-11 | 12 | 3 | 3 | 3 |
| 2026-10-12 | 11 | 3 | 3 | 3 |
| 2026-10-13 | 20 | 3 | 3 | 3 |
| 2026-10-14 | 15 | 2.666666667 | 0 | 3 |
| 2026-10-15 | 18 | 2.777777778 | 0 | 3 |
| 2026-10-16 | 5 | 3 | 3 | 3 |

- Latest seven observed-date means (October 10-16):
  `(3 + 3 + 3 + 3 + 40/15 + 50/18 + 3) / 7 = 2.92063492063492`.
- Remaining prior observed-date means: `(5 + 3) / 2 = 4`.
- Relative change: `(2.92063492063492 / 4 - 1) * 100 = -26.9841269841%`.

Thus the reported 27% is reproducible, but its prior mean is supported by
**two observed dates, September 17 and October 9**, rather than seven daily
readings in a preceding contiguous seven-day window. Missing dates are unknown.
The September 17 source snapshot was collected on September 25; its source
timestamp must not be read as September 25 activity.

These are averages of snapshots of an accumulating UTC-day counter, not final
daily totals. On October 14 the source recorded 0 at 00:18:57Z, 2 at
01:24:45Z and 02:30:37Z, and 3 from 03:36:44Z onward. On October 15 it
recorded 0 at 00:18:10Z, 2 at 01:23:44Z, and 3 from 02:29:18Z onward.
This explains why those dates' snapshot means are below 3; it does not explain
the earlier difference between 5 and 3 or prove why an installation stopped
reporting. No channel attribution or retention cause can be recovered from
these aggregate records.

The aggregation above was recalculated from the live ledger with PowerShell:
filter by project and metric, group `at` by UTC `yyyy-MM-dd`, and use
`Measure-Object -Property value -Average -Minimum -Maximum` per group. No
missing date was filled with zero or carried forward.

## Installation and distribution observations

Read-only checks against existing public surfaces returned:

| Surface | Observed result |
|---|---|
| `https://codesamplex.dev/v1/stats` | `activeInstallations1d=3`, `activeInstallations7d=3`, `activeInstallations30d=3`, `generatedAt=2026-10-16T06:04:05Z` |
| `https://codesamplex.dev/install.sh` | HTTP 200 |
| `https://codesamplex.dev/install.ps1` | HTTP 200 |
| `https://codesamplex.dev/dl/csx-update-stable.json` | HTTP 200 |
| `https://codesamplex.dev/dl/SHA256SUMS.txt` | HTTP 200 |
| GitHub latest public release | `v0.2.23`, published 2026-10-14T08:27:49Z |
| Official MCP Registry exact server's latest version | `0.2.23`, active, `isLatest=true`, published 2026-10-14T08:27:55.245359Z |

The exact registry read was
`https://registry.modelcontextprotocol.io/v0.1/servers/io.github.r2cuerdame%2Fcodesamplex/versions/latest`.
The search endpoint is paginated; the last item of its first page is not
evidence of the latest version.

HTTP availability does not prove a fresh installation succeeds. No installer
was executed, no account or listing was changed, and no synthetic presence
was sent. Existing README entry links were already shipped by PR #566 for
Issue #565. That PR's QA notes mention untranslated README entry links;
there is no observation tying that omission to the reported decline.

## Presence contract inspected

At the baseline, `internal/daemon/presence.go` produces a version-1
`domain.PresencePayload` from rotating identity tokens, on a community-enabled
network path. A successful same-day report suppresses further reports that
day. `internal/httpapi/presence.go` decodes bounded JSON and validates the
payload; `PG.RecordPresence` in `internal/serverstore/pg.go` validates again
and upserts the three interval/epoch/token keys. The public counts select
only `ordinary` and `external` classes for each current aligned epoch.
This trace describes the code; it is not evidence of a failed production ping.

Git history since October 1 contains no changes to the presence producer,
HTTP handler, payload schema, identity token code, or presence migration.
There is no observed failing producer/consumer fixture to justify changing
that contract. Existing tests cover presence validation and fake-store epoch
and class behavior; production PostgreSQL execution is a separate check.

## Local verification

Builds and tests were invoked through the installed CSX MCP server's
`run_observed_command` tool, using the Issue workspace as `cwd`.

- `go build ./...`: exit 0, `PROJECT_COMPILE / PASS`.
- `go test ./internal/domain ./internal/identity ./internal/daemon ./internal/httpapi ./internal/serverstore -run Presence -count=1`:
  domain, identity, daemon, and HTTP API packages passed. The server-store
  package failed because `TestIntegrationPGPresenceStoreMatchesTheFake` could
  not connect to the configured PostgreSQL endpoint at `127.0.0.1:5433`
  (connection refused). This is an unmet database prerequisite, not a passing
  production-store check or an observed presence assertion failure.
- `git diff --check`: passed for the evidence document.

No production installation flow, real production presence write, deployment,
or seven-day post-close outcome was tested. No runtime code changed.

## Decision needed before a growth implementation

The proposed action requires naming the cause before choosing a lever. The
available aggregate evidence reproduces the statistic but cannot establish
that cause. No broken distribution route was demonstrated by the checks
above. Changing the Growth collector in LoopOffice would be outside this
Worker's repository and Issue scope.

Luna must decide whether to decline this proposal as inconclusive, or issue
revised acceptance criteria for a named zero-spend distribution experiment
that explicitly preserves the unknown cause and measures its result later.
This is an evidence/product-direction decision, not a request to waive tests
or QA. The harness says: "Scope expansion is not yours to approve: report it
as blocked with a `decision` need; Luna decides it."

Unmet delivery conditions: no cause-grounded growth action is implemented,
so no closing PR or recovery claim is warranted. The Issue stays open for
Luna's decision; the Worker does not merge, close, or deploy anything.
