# Admin operations and report review

The private `/admin` console has four views: dashboard (service health and
outstanding work), reports (review queue and evidence), demand/diagnostics
(coverage and historical metrics), and farm/tokens (worker controls and
credentials). Bookmarked tabs survive reloads. Browsing a report is read-only.

The report queue reads the entire retention period, defaults to unresolved
product reports, and returns 25 rows per page. Channel counts do not change
with search or status filters; the result count does. `no-replay-lane` and
`unsupported` reports without a verdict remain visible as unresolved work.
The older 30-day channel trends are separately labelled and folded away.

Open a report to inspect its submitted behavior, expected behavior,
hypothesis, environment, public coordinates, timestamps, and job/sample IDs.
The hypothesis is a submission, not proof. Reporter buckets and deduplication
fingerprints are excluded from the private API projection as well as the UI.
Submitted strings are rendered as text, including the full evidence JSON.

Product decisions require a valid verdict and a written evidence note. The
new `POST /admin/api/reports/review` atomically stores the verdict, note,
timestamp and optional canonical reference. Existing decisions return 409;
neither verdicts nor existing canonical references can be silently replaced.
Confirmed defects can be linked later if the bug does not yet have an ID.
Anomaly verdicts remain exclusively controlled by independent verification
receipts; the report view offers no manual resolve, deletion, or bulk action.

All report reads require admin authentication and private/no-store headers.
Browser writes additionally require the configured same origin, JSON, and
the CSRF marker. Existing admin bearer credentials keep their existing API
authority. The additive `0039_report_review_notes.sql` migration preserves
legacy reports and decisions, with an empty note for older decisions.

## Verification

`go test ./internal/admin ./internal/serverstore` covers authentication,
CSRF, evidence projection, input validation, immutable decisions, retained
long notes, pagination beyond 25 rows, all-time counts, empty pages, literal
search, and Fake/PostgreSQL parity. Set `CSX_TEST_DSN` to a disposable database
to execute the PostgreSQL contract; canonical CI supplies PostgreSQL 17.

For browser regression, compile `go test -c -o <temporary executable>
./internal/admin`, set `CSX_ADMIN_BROWSER_ADDR=127.0.0.1:18986`, and run that
executable with `-test.run=^TestServeReportBrowserFixture$`. Then run
`python scripts/admin-playwright.py --out <artifact directory>`. The fixture
contains only fake data and uses the fixed non-production test password.
The browser check exercises paging, search, cancel/confirm, persisted review,
read-only anomalies, failure recovery, tab navigation and narrow layouts.

`python scripts/admin-playwright.py --production --out <private directory>`
performs read-only production verification using the existing current-user
Windows DPAPI credential. Credentials never enter argv or artifacts. Keep
production report snapshots and screenshots private; do not commit them.
