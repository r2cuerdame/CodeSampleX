# PM_AUTO_REFILL_V1 result

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/80
- Branch: `feat/80-cli-structured-evidence`
- Implementation commit: `5f95050`
- Draft PR: https://github.com/r2cuerdame/CodeSampleX/pull/215
- Deployment/merge: not performed

## Canonical-source audit

Issue #80 was open, had no comments, no linked or cross-referenced PR, and no
`blockedBy`/`blocking` dependency. Adjacent merged PRs #42, #47, and #208 were
reviewed to avoid repeating their stdout/stderr selection, failure-fingerprint,
termination, and CLI-coordinate work. The remaining gap was structured
per-execution evidence and its safe persistence.

## Changes

- Probe the exact allowlisted CLI executable version before untimed execution;
  timed caller contexts skip the best-effort probe rather than losing command
  budget and record partial evidence quality.
- Record canonical tool/subcommand/flag patterns, direct-or-shell execution,
  OS/architecture/runtime environment identity, structured termination, and
  start/finish timestamps.
- Store local-only stdout/stderr fingerprints, bounded diagnostic-shape
  excerpts, and explicit capture/excerpt truncation.
- Replace positional/flag values with structural placeholders. Excerpts keep
  only error codes, fixed diagnostic keywords, and sanitizer placeholders;
  arbitrary Unicode identifiers become `<text>`.
- Add `cli_execution_evidence`, aggregating identical outcomes while separating
  termination, stream signature, truncation, quality, and field/Farm source.
  Its write is atomic with the existing anonymous daily observation aggregate.
- Add `schemas/v1/cli-execution-evidence.json`, documentation, migration,
  privacy regressions, structured-storage aggregation tests, and runner tests.

## Verification

- `go test ./internal/domain ./internal/environment ./internal/sanitizer ./internal/evidence ./internal/storage/localdb -count=1` — PASS
- `go vet ./...` — PASS
- `go build ./...` — PASS
- `git diff --check` — PASS
- PowerShell JSON parse of `schemas/v1/cli-execution-evidence.json` — PASS
- Independent blocker-only code review — PASS; no remaining P1/blocker
- Full `go test ./... -count=1 -timeout 15m` — all executed packages passed,
  but Windows security blocked the temporary `internal/launcher` test binary as
  a potential unwanted application. A focused launcher retry hit the same
  external block. An earlier full run in this worktree passed including that
  package.

## DevHotel and blockers

DevHotel was not applicable because this change is backend/CLI local evidence
persistence and does not alter deployable web, Android, or desktop UI behavior.
No deployment was attempted. The only remaining verification blocker is the
host security product preventing a fresh `internal/launcher` test-binary run;
no security setting was bypassed or disabled.
