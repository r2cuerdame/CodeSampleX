# Anonymous free-client analytics

This measures retained anonymous **client installations**, not unique humans,
accounts, paid users, downloads, or every local CLI/MCP invocation. No signup
is required. NRU means **newly observed anonymous clients**, not new registered
users. DAU and MAU on this panel are anonymous-only. Registered/account activity
is not measured by these counters.

## Identity and transport

On first CLI execution, the existing race-safe `$CSX_HOME/identity.json` is
created locally (0700 directory / 0600 file on POSIX; use a private user profile
and ACLs on Windows). CLI, MCP and daemon reuse that home. A domain-separated
HMAC of its secret seed and configured server origin/base path produces the
64-hex `X-CSX-Anonymous-ID`. The seed and signing key never leave the machine.
This new identifier is intentionally stable across days. It does **not** change
or reuse the rotating evidence/presence tokens, signing public key, or project
buckets. Different server origins receive different IDs.

Community-mode API requests automatically include this ID and the configured
`X-CSX-Client-Class`. The transport reloads consent/configuration before each
request, limits attachment to the configured server's `/v1/` and `/v2/` paths,
strips identity headers on redirects to other origins/peers/registries, and
does not attach them to Authorization-bearing requests. Local-only mode keeps
its existing no-network behavior; generating a local identity is not a network
operation. No CodeSampleX-service analytics heartbeat is added. Separately,
community-mode public CLI/MCP clients may send the once-daily PurplePulse
activation documented in `PRIVACY.md` §4.10; it is not part of the anonymous
client counters below. Local cache-only work and offline use therefore do not
imply CodeSampleX API activity. Background community sync requests
can count even when no person is interacting with the CLI.

On a direct unauthenticated API request without an ID, the server issues a
cryptographically random 256-bit ID in `X-CSX-Anonymous-ID` and an HttpOnly,
SameSite=Lax, one-year `csx_anonymous` cookie (Secure on HTTPS deployments).
Cookie-aware API clients persist and replay the cookie. Other API clients must
persist the response header and send it as a request header. For example:

```sh
# Restrict new cookie-file permissions; this request needs no account.
umask 077
curl -c ~/.csx-api-cookies -b ~/.csx-api-cookies https://codesamplex.dev/v1/adapters
```

The ID is an analytics pseudonym, **not an authentication or authorization
credential**. It does not permit publishing, accessing admin functions, or
impersonating an account. The server stores only domain-separated SHA-256
hashes of the high-entropy ID. Do not log the header or cookie at a proxy.
API responses bearing identity headers are `private, no-store`, even if the
underlying data handler would normally allow public caching. Conditional ETags
still work for clients that explicitly retain them.

Clearing the local identity, changing CSX_HOME/server URL, deleting cookies,
or clients that fail to replay an ID can increase observed clients/NRU. Cloning
an identity merges installations. These are pseudonymous installations, not
verified independent people. Arbitrary clients can manufacture IDs or spoof
their class; analytics cannot be used as a billing/security authority.

## Counted activity and metric definitions

Only registered product API routes with successful 2xx or 304 responses count.
HEAD/OPTIONS, unknown routes, failures (including 429), health/version checks,
GitHub auth, authoring, verifier/job polling, peers and rotating presence
heartbeats are excluded. Authorization-bearing requests and non-public client
classes (anything except absent/ordinary/external) are excluded. Configure
farm, CI, verifier and operator nodes using the existing client-class setting.
Credentials sent outside Authorization are not an alternative supported
account identity mechanism.

Each counted request atomically updates `anonymous_clients.first_seen`
(minimum server timestamp), `last_seen` (maximum), and lifetime `request_count`,
and increments a unique `(UTC day, client_hash)` activity row. That daily row
also increments one rollout-readiness counter: `credential_present_count` when
the request arrived with a valid `X-CSX-Anonymous-ID`, or
`credential_issued_count` when the header was absent or malformed and the
server returned an ID. A cookie-only request is in the latter group because it
still needs the response header issued. A retry which again succeeds is another
request, but never another active client that day. All timestamps come from the
server; caller timestamps are not accepted.

* **NRU:** clients whose retained first_seen falls on the specified UTC day.
* **DAU / anonymous active users:** distinct IDs active on that UTC calendar day.
* **MAU:** distinct IDs active on that day or the previous 29 UTC calendar days;
  neither a calendar month nor an aligned rotating-token epoch.
* **Anonymous activity:** successful recorded request volume per UTC day.
* **Anonymous credential adoption:** requests arriving with a valid ID header
  divided by valid-header plus server-issued requests. This is rollout
  telemetry, not authentication; missing credentials continue to receive free
  access and a newly issued or cookie-reused ID.
* **Retention D1/D7/D30:** within a first-seen-day cohort, the fraction active
  on exactly the first day plus 1/7/30 days. The return day must be complete;
  today and future cells display `—`, not zero. Overall retention sums returning
  clients and eligible cohort sizes, rather than averaging cohort percentages.

The admin's existing authenticated, private dashboard shows the activity
time-series plus daily credential-present, server-issued and adoption-rate
series, weighted retention bars, daily cohort bars and accessible data tables. Reads
cover at most 90 days of series/cohorts plus the 29-day MAU lookback. Collection
starts at migration 0040; no fabricated history is backfilled from IPs,
rotating tokens, access logs or accounts. Pre-collection days are omitted,
today is partial, and initial MAU is a partial observation of its 30-day window.
Zero means **no recorded activity**, not proof that no one used the product.

Analytics writes use the background database class, have a 250ms ceiling and
fail open with a fixed, identifier-free
server warning. Database outages, older clients and lost requests can undercount;
there is no durable retry queue or claim of complete collection. A failed admin
read displays unavailable rather than zeros. Reads are bounded but are separate
queries; concurrent arrivals can make adjacent metrics slightly inconsistent.

## Privacy, retention and deployment

IP is only a secondary abuse/rate-limit signal. The server no longer wraps
requests with the old network-fingerprint activity tracker, and no longer
passes it to the dashboard to label or exclude an operator network. Existing
IP-derived analytics rows remain subject to their existing expiry maintenance;
new anonymous analytics never join to those rows. The new tables contain no
IP, URL, user-agent, raw ID, account, package, project or source text.

Hourly bounded maintenance keeps 120 UTC days of daily activity and deletes
client summaries after 365 days of inactivity. The 120-day window covers the
90-day chart plus 29-day MAU lookback. Active summaries retain their original
first_seen and cumulative request_count; inactive deleted clients count as new
if they later return. Maintenance logs failure without identifying clients.
Deletion is eventually enforced by the server background maintenance loop; a
stopped server or persistent database failure delays expiry. Backups have their
own retention policy. To remove one installation's records, an operator can
delete its computed client_hash from anonymous_clients; daily rows cascade.

Apply embedded migrations through `0041_anonymous_credential_adoption.sql`
using the existing `csx-server migrate` process **before** starting the new
server. Migration 0040 creates the empty indexed analytics tables; migration
0041 adds two zero-defaulted daily counters and a separate adoption collection
start marker. Existing daily request history is deliberately not classified or
backfilled. The offline migration acceptance map pins both analytics migrations;
the ledger count is 42 and all six analytics indexes remain unchanged. Deploy the server before distributing the new CLI to avoid
early clients sending IDs to an old server that cannot count them. Old cookie
clients still work; stateless older clients can inflate NRU on each request.
Migration reruns use the existing migration ledger. Rollback the binary while
leaving additive tables intact; do not drop them during rollback. A rollback
to the previous server can resume legacy IP analytics if its old key is set,
so remove `CSX_ACTIVITY_HASH_KEY` from that rollback configuration if needed.

Deployment was intentionally not performed by this implementation. Verify the
admin panel, private API caching, proxy log redaction, maintenance scheduling
and query latency on the deployment's actual PostgreSQL workload before release.

## Verification

Run `gofmt` on changed Go files, `go test ./...` and `go vet ./...`. Set
`CSX_TEST_DSN` to a disposable PostgreSQL database with C collation and
`CSX_REQUIRE_TEST_DSN=1` so the migration, concurrency, retention and Fake/PG
contract tests cannot silently skip. The production mux's cold-restart test
also verifies analytics does not acquire interactive database connections.

On Windows, the existing POSIX deployment tests require Git's `usr/bin` ahead
of System32 in PATH so `timeout` resolves to GNU timeout. Set `CSX_CHROME` to
Playwright's `chrome-headless-shell.exe` and `CSX_REQUIRE_CHROME=1` for the
existing browser geometry tests. Full Chromium's Windows binary did not
produce their required `--dump-dom` output in this environment.

For the new admin browser regression, set `CSX_ANONYMOUS_BROWSER=1` and run
`go test ./internal/admin -run '^TestServeAnonymousBrowserFixture$' -count=1`
in one terminal, then `python scripts/anonymous-admin-playwright.py --out <dir>`
in another. Stop the fixture afterwards; it uses only loopback and a public
test password. The normal test suite skips this intentional serving fixture.
