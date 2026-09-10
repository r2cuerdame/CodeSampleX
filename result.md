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
- `go test ./internal/domain -run TestSchemaFixtures -v` — PASS (includes `schemas/v1/cli-execution-evidence.json` fixture)
- `go vet ./...` — PASS
- `go build ./...` — PASS
- `git diff --check` — PASS
- PowerShell JSON parse of `schemas/v1/cli-execution-evidence.json` — PASS
- Independent blocker-only code review — PASS; no remaining P1/blocker
- `internal/launcher` verification — PASS; all 35 tests passed cleanly via precompiled test binary (`go test -c -o "$env:TEMP\launcher.test.exe" ./internal/launcher && & "$env:TEMP\launcher.test.exe" -test.v`). Windows security block on the temporary `go-build*\b001\launcher.test.exe` path is confirmed to be an external Defender heuristic false positive (`Bearfoos.B!ml`, documented in `docs/operations.md`) with no code regression.
- Full non-serverstore `go test` suite (48 packages) — all packages passed; `internal/launcher` verified clean separately.

## DevHotel and blockers

DevHotel was not applicable because this change is backend/CLI local evidence
persistence and does not alter deployable web, Android, or desktop UI behavior.
No deployment was attempted. No blockers remain for PR #215.
