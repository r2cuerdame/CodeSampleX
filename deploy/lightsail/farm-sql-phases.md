# Bounded Farm SQL-family diagnostic

Refs r2cuerdame/CodeSampleX-Farm#27 and CodeSampleX#364; acceptance remains pending.

`farm-sql-phases.yml` is a manual operations workflow for a single overlap
capture when a separately dispatched Farm measurement reports an unavailable
aggregate. It does not call the admin API, issue work, inspect receipts, or
dispatch another workflow. It does not alter or run the existing observer.

The catalog and permitted server revision are exactly
`2b859134e5a9708a76be6e148af2034d9a57cc5e` (v0.1.161). A different
`expected_revision`, even valid hexadecimal, is rejected before SSH. The
artifact records the catalog revision separately from the operational workflow
SHA. A future SQL change requires an explicit catalog/test update and review.

## Running the reviewed workflow

Only a canonical manual `main` dispatch may reach production. The checked-out
SHA must have successful canonical `ci.yml` evidence for that exact main SHA.
The job uses the existing `codesamplex-production` environment, dedicated SSH
identity and pinned host key, and the shared non-canceling production concurrency
group. The operator coordinates the separate Farm counter dispatch after the
**Sample active SQL phases for one bounded window** step is in progress and the
Farm runner is idle. Job status is coordination evidence; sample timestamps in
the resulting artifact determine actual overlap.

```powershell
gh workflow run farm-sql-phases.yml --repo r2cuerdame/CodeSampleX --ref main `
  -f expected_revision=2b859134e5a9708a76be6e148af2034d9a57cc5e
```

This command is an operator instruction, not part of a collector or test.
Dispatch only after review, PR CI, merge, and successful exact main CI.

## Read and output bounds

The SSH payload executes Python with `-I -B` from stdin. It reuses PR372's
bounded pipe and container-identity helpers by loading that module under a
non-main name in memory; its authoring-log collector is not run. There are no
host file writes, restarts, service operations, admin credentials, or privilege
changes. Runner-only SSH material is removed and the JSON artifact is retained
on failure.

The existing route is fixed: `/opt/codesamplex/deploy/docker-compose.yml`,
service `db`, local `psql -X -U csx -d csx`. Fixed process-local `PGOPTIONS`
requires read-only transactions, a 1,000 ms statement deadline, a 500 ms lock
deadline, and JIT off. No extension, permanent setting, table, or schema is
created or changed by the collector. A single fixed capability SELECT chooses
between two fixed sampling SELECTs. SQL stderr is discarded, including errors
that could contain query text. Only strict, whitelisted JSON reaches the runner.

After bounded identity/capability preflight, sampling covers 90 seconds at a
minimum two-second start interval, at most 45 samples. It leaves the final three
seconds for an in-flight read rather than starting a read that could exceed the
window (normally 44 samples). Slow reads do not trigger catch-up bursts. The
collector has a 105-second total budget, individual commands have three-second
and 16 KiB output limits, the remote process has a 108-second outer timeout, and
transport has a 115-second/768 KiB ceiling. Any SQL or command failure stops the
capture without retry. Metadata is checked before and after; changed identity,
invalid output, clock discontinuity, or other failure yields unavailable with
all measurements null. This is a failed gate, never a zero-work observation.

## Interpretation

Only active queries in the current database are sampled, excluding the
sampler's own backend. Complete query text is lexed and reduced to a full
statement-family fingerprint inside PostgreSQL. The fixed catalog includes
FarmWorkers, seven FarmHealth queries, backlog stocks, matrix, claims,
first-PASS aggregate, completeness, and coverage. Quoted constants, parameter
positions, and numeric constants do not distinguish families. Consequently,
another caller or different literal values may share a family. A family hit
does not prove a specific HTTP request, phase ordering, or its root cause.

`trackActivityQuerySize` is reported as a bounded integer. At its common
1,024-byte setting, long coverage/backlog/completeness SQL is truncated. A
truncated prefix never earns a phase label. The four-byte boundary band is
conservatively treated as possibly truncated, including UTF-8 clipping.
If already installed and preloaded
`pg_stat_statements` and query IDs are available, the sampler can match an
existing full representative inside PostgreSQL by database, user, and query ID.
Duplicate representatives do not multiply active counts; conflicting or null
representatives remain ambiguous. An active first execution may have no completed
entry. Otherwise truncated queries remain explicitly counted as truncated.
Disabled or absent capabilities select the activity-only variant; no extension
is installed to improve coverage.

Statement text is requested at most once per sample, and only when an active
truncated query needs it. PostgreSQL may still read the complete statement-text
file for that call; it is not assumed to push the filter into that read. The
one-second deadline and first-failure stop apply to this cost too.

Known families emit a fixed wait-class enum, active count, minimum/maximum query
age in milliseconds, and a flag if age is capped at 24 hours. Unknown, truncated,
and ambiguous queries emit counts only. No SQL, statement fingerprints, query
IDs, PIDs, users, application names, session IDs, or credentials leave the
database. Local request start/end timestamps and monotonic offsets are retained
for overlap analysis; clock alignment with the separately collected counter
must still be checked. The polling cadence can miss short queries, and no hit
does not mean zero execution time. This diagnostic measures no drafts, node
VERIFY receipts, or node first-PASS results and cannot satisfy the clean-hour
acceptance requirement by itself.

PostgreSQL's [activity documentation](https://www.postgresql.org/docs/17/monitoring-stats.html)
describes query truncation and active-state semantics. Its
[statement-statistics documentation](https://www.postgresql.org/docs/17/pgstatstatements.html)
describes representative text, entry keys, completion updates, and text-read cost.

## Validation

`go test ./deploy/lightsail` runs strict offline schema/clock/transport tests.
On Linux it also requires a disposable `postgres:17-alpine` Docker fixture,
created with a unique owned name, no host mounts, no published ports, and no
network. That fixture alone creates an extension and empty synthetic schema;
production code cannot enter that setup path and no DSN is accepted by the test.
The fixture executes all 14 pinned source statements, checks their actual
PostgreSQL normalized representatives, and checks truncation, missing
capabilities, duplicate/conflicting matches, identity binding, and self-exclusion.
It is explicitly removed after the test.
