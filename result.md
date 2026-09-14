# Issue #398 Result: Web PurplePulse Daily Telemetry

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/398
- Branch: `r2cuerdame/pulse-web-398`
- PR: https://github.com/r2cuerdame/CodeSampleX/pull/400

## Summary

Implemented PurplePulse web-only daily telemetry on `codesamplex.dev` for issue #398 adhering to all privacy, architecture, and operational constraints.

## Implementation Details

1. **Dedicated Same-Origin Static Asset (`internal/web/static/pulse.js`):**
   - Telemetry logic is encapsulated in a dedicated static script embedded via `staticFS` and served under `/static/pulse.js`.
   - Generates persistent random UUID `install_id` stored in `localStorage` with a 1-year first-party cookie fallback (`pp_install_id`).
   - Rate-limits network attempts to at most one per local day: persists `pp_last_attempt` immediately before the single `fetch` call so that even if the network fails or returns an error, subsequent page loads that day do not retry (guaranteeing against retry storms).
   - Fixed endpoint: `https://pulse-api.purpleshiphub.workers.dev/api/v1/ping` without configurable runtime overrides.
   - Non-production environment whitelist: sends only for exact environments `'test'` or `'dev'`. Other values (e.g. `'staging'`, `'unknown'`, `'local'`) do not send.
   - Production telemetry protection: in production (`''`, `'production'`, `'prod'`), `environment` is omitted from payload, and sending is permitted only when `location.hostname` is `codesamplex.dev` or `www.codesamplex.dev` and `navigator.webdriver` is false.
   - Enforces closed OS vocabulary: `windows | android | ios | macos | linux | other` (detecting Android before generic Linux, and iOS before macOS).
   - Network timeout target ~2s (`2000ms`) via `AbortSignal.timeout(2000)` with `AbortController` fallback.
   - Zero PII: payload strictly schema-bounded to `project_id`, `install_id`, `version`, `os`, and `platform: 'web'`.

2. **Template Wiring (`internal/web/templates/base.html`):**
   - Emits a minimal script tag before `</body>` matching existing static asset cache-busting conventions:
     `<script async src="/static/pulse.js{{if .AssetVersion}}?v={{.AssetVersion}}{{end}}" id="purple-pulse" data-project-id="pp_codesamplex_f2f2ab10"{{with .Build}} data-version="{{.Version}}" data-env="{{.Environment}}"{{end}}></script>`

3. **Regression Test Coverage:**
   - `internal/web/pulse_test.js`: Node.js test suite verifying pre-fetch `pp_last_attempt` persistence, retry suppression across page loads after network failure, non-prod environment whitelist (`test` and `dev` only), exact OS vocabulary mapping, and production hostname/webdriver guards.
   - `internal/web/pulse_test.go`: Go regression tests covering static asset serving with 1-year immutable caching on versioned requests and ETags on plain requests, script presence across all collection routes, and build/environment attribute rendering.

## Verification

- `go test ./internal/web -run TestPurplePulse -count=1 -v` — PASS
- `node internal/web/pulse_test.js` — PASS
- `go test ./internal/web -run "TestCardsFitNarrowViewports|TestTheCompatibilityAxesAreReadableAndDoNotOverflow|TestFailureIssueFitsNarrowViewports|TestTheGapListDoesNotScrollSidewaysOnAPhone|TestTheHomePageHasNoDeadSpaceBetweenSections|TestOpeningTheLanguagePickerDoesNotGrowTheHeader|TestSampleDetailFitsNarrowViewports|TestTheSampleMarksSurviveAPhone|TestTheSourcePaneIsWiderThanTheFileList" -p 1` — PASS
- `go vet ./...` — PASS
- `go build ./...` — PASS
- `git diff --check` — PASS
- DevHotel health check: `saby9l19` checked out with status healthy.
