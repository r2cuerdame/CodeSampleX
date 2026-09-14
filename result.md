# Issue #398 Result: Web PurplePulse Daily Telemetry

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/398
- Branch: `r2cuerdame/pulse-web-398`
- PR: (to be created)

## Summary

Implemented PurplePulse web-only daily telemetry on `codesamplex.dev` for issue #398.

## Implementation Details

1. **Dedicated Static Asset (`internal/web/static/pulse.js`):**
   - Implemented client-side telemetry in a dedicated same-origin static script served by `staticFS` under `/static/pulse.js`.
   - Generates persistent random UUID `install_id` stored in `localStorage` with a 1-year first-party cookie fallback (`pp_install_id`).
   - Limits pings to at most once per local day (`pp_last_ping`).
   - Marks the day sent only after receiving an HTTP 200 or 202 response. On network error or non-200/202 status, leaves unsent so a subsequent page load that day may try once.
   - Enforces closed OS vocabulary: `windows | android | ios | macos | linux | other` (detecting Android before generic Linux, and iOS before macOS).
   - Enforces ~2s network timeout (`2000ms`) using `AbortSignal.timeout(2000)` with `AbortController` fallback.
   - No PII: payload contains strictly `project_id`, `install_id`, `version`, `os`, and `platform: 'web'`.
   - Production telemetry protection: in production, `environment` is omitted from payload; prod telemetry is guarded against non-production hosts and automated test runners (`navigator.webdriver`).

2. **Template Integration (`internal/web/templates/base.html`):**
   - Added minimal script tag to `base.html` before `</body>`:
     `<script async src="/static/pulse.js{{if .AssetVersion}}?v={{.AssetVersion}}{{end}}" id="purple-pulse" data-project-id="pp_codesamplex_f2f2ab10"{{with .Build}} data-version="{{.Version}}" data-env="{{.Environment}}"{{end}}></script>`
   - Uses the repository's established immutable cache-busting convention (`?v={{.AssetVersion}}`).

3. **Regression Test Coverage:**
   - `internal/web/pulse_test.js`: Node.js test suite verifying UUID persistence, localStorage/cookie fallback, rate limiting on 200/202, unsent state on failure, OS classification, and test-environment guards.
   - `internal/web/pulse_test.go`: Go regression tests covering static asset serving with 1-year immutable caching on versioned requests and ETags on plain requests, script presence across all collection routes, and build/environment attribute rendering.

## Verification

- `go test ./internal/web -run TestPurplePulse -count=1 -v` — PASS
- `node internal/web/pulse_test.js` — PASS
- `go test ./internal/web -run "TestCardsFitNarrowViewports|TestTheCompatibilityAxesAreReadableAndDoNotOverflow|TestFailureIssueFitsNarrowViewports|TestTheGapListDoesNotScrollSidewaysOnAPhone|TestTheHomePageHasNoDeadSpaceBetweenSections|TestOpeningTheLanguagePickerDoesNotGrowTheHeader|TestSampleDetailFitsNarrowViewports|TestTheSampleMarksSurviveAPhone|TestTheSourcePaneIsWiderThanTheFileList" -p 1` — PASS
- `go vet ./...` — PASS
- `go build ./...` — PASS
- `git diff --check` — PASS
- DevHotel health check: `saby9l19` checked out with status healthy.
