# PM_AUTO_REFILL_V1 result

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/307
- Branch: `herder/job_01M27F3YD2HSWJ28FZJ26EQ39P-pm-refill-c31f3d5cb8ec5fe9-df6acfbea0c17079`
- Implementation commit: `b08aeecd`
- Draft PR: https://github.com/r2cuerdame/CodeSampleX/pull/355
- Deployment/merge: not performed

## Canonical-source audit

Issue #307 was open, unassigned, and had no comments, related implementation PR,
or formal `blocked_by`/`blocking` dependency. Related issue #303 remains open with
draft PR #348, but GitHub does not declare it as a dependency and that PR does not
modify `internal/storage/localdb/observations.go`. Fresh `origin/main` at
`ba6a29fd` retained `obsCoord := canon`, confirming the reported query-filter bleed
was present and not already handled.

## Changes

- Reconstruct each observation coordinate from the stored row rather than copying
  the canonical query target.
- Retain only the safe tool-name fallback when a CLI PURL cannot provide the tool.
- Assign decoded subcommand and argument fields even when empty, preventing query
  subcommand/argument filters from leaking into stored observations.
- Parse `outer_command` whenever either decoded field is missing, filling only the
  missing field so valid symbol data remains authoritative.
- Add regression coverage proving empty args/subcommands do not count as exact
  flagged-command matches and empty-symbol legacy rows use `outer_command` without
  producing false positives.

## Verification evidence

- RDC device/session: `recuerdame` (`0efa8b03-2586-42e6-9f67-8b586dfb33ab`)
- Build identity: commit `b08aeecd`
- `go test ./internal/storage/localdb -count=1` — PASS
- `git diff --check` — PASS
- `go test ./... -count=1` — FAIL outside the changed package:
  - `deploy/lightsail` Unix-oriented `flock`/timeout fixtures fail under this Windows host.
  - `internal/web` headless Chrome layout tests report no browser measurement.
  - All emitted package results outside those environment-sensitive suites passed,
    including `internal/storage/localdb`.

## DevHotel, tooling, and blockers

DevHotel verification is not applicable because this is backend-only Go storage/query
logic with no deployable Android, web, or desktop UI change. No DevHotel room was
created. RDC was used for repository diagnostics, formatting, tests, Git operations,
and evidence collection. A CodeSampleX/CSX MCP capability was not available in this
job, so no CSX MCP query was possible.

No implementation or dependency blocker remains. The focused test suite passes; the
two unrelated Windows-host failures above remain recorded for transparency. Draft PR
#355 is ready for review and CI. No deployment, merge, approval bypass, or physical
device action was attempted.
