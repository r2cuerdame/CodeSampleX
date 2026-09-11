# PM_AUTO_REFILL_V1 result

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/304
- Branch: `herder/job_01M275NKRV64ZA51S66TCDVKBA-pm-refill-8b1b2da29c13934a-8d9071136fe16332`
- Implementation commit: `4999e262`
- Draft PR: https://github.com/r2cuerdame/CodeSampleX/pull/351
- Deployment/merge: not performed

## Canonical-source audit

Issue #304 was open with no comments or related PR. Related issue #82 was
closed and PR #208 was merged, so no open dependency remained. Fresh
`origin/main` at `4b08ed50` still contained the reported guard in
`attachCLIExperience`; the work had not already been completed.

## Changes

- Always parse a non-empty recognized CLI query in `attachCLIExperience`.
- Preserve the tool and version supplied by a generic CLI package PURL.
- When the parsed query tool matches the package tool, enrich the target with
  the parsed subcommand and sanitized argument pattern.
- Extend the search regression test to cover query-only recall and combined
  `pkg:generic/cli/docker@27.1.0` plus `docker compose up -d` recall, asserting
  the exact combined coordinate and field PASS/FAIL counts.

## Verification

- Pre-fix regression reproduction — FAIL as expected: combined package/query
  case returned a nil CLI experience.
- `go test ./internal/search -count=1` — PASS.
- `go test ./internal/domain ./internal/storage/localdb ./internal/search -count=1` — PASS.
- `go vet ./...` — PASS.
- `go build ./...` — PASS.
- `git diff --check` — PASS.
- Independent read-only code audit — PASS; no correctness finding.
- `go test ./... -count=1` — relevant and most repository packages PASS, but
  the aggregate command FAILS on unrelated Windows environment constraints:
  `deploy/lightsail` resolves a GNU-style timeout fixture to Windows `timeout`,
  `internal/cli` and `internal/update` encounter executable access denials, and
  `scripts` cannot write its isolated HKCU registry test key.

## DevHotel, tooling, and blockers

DevHotel was not applicable because this is a backend Go search/recall change
with no deployable Android, web, or desktop UI surface. RDC and CSX MCP/CLI were
not exposed in this job environment, so scoped execution used the assigned
worktree's Git and Go tools. No deployment or merge was attempted. The focused
change has no remaining blocker; the unrelated full-suite Windows environment
failures are recorded above for transparency.
