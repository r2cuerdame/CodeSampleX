# #174 bounded incremental builder reads

Canonical incident: [#174](https://github.com/r2cuerdame/CodeSampleX/issues/174).
Implementation: [PR #254](https://github.com/r2cuerdame/CodeSampleX/pull/254), based on
main b0a2befa (#252/#253). Production recovery is not established by this PR.

## Change and attribution contract

Incremental PostgreSQL passes select samples by the union of declared packages,
receipt-resolved packages, and stated subjects. Selection uses expression indexes
on the original stored JSON, so index creation covers historical rows and all
subsequent writers, including CLI and direct SQL. There is no asynchronously
maintained projection or assumption that receipt names match declared names.

Selected samples retain their complete receipt histories. The existing Go
decoder still validates each signed resolved-package list as one unit. Invalid
lists never acquire partial package authority. Subject claims retain the full
reader's existing semantics, including subject precedence and malformed manifest
type rejection.

Global narrowest-symbol attribution is preserved by reading the competing claims
for each relevant symbol. Changed/quarantined claims expand the dirty package
scope, including packages whose previously suppressed symbols can reappear.
All majors and stored snapshots in scope participate in rebuild/retirement.

Unsupported percent-escaped identities, non-ASCII coordinate identities, and
case-insensitive/Unicode top-level JSON aliases fail the indexed preflight with
an explicit source identifier. They never silently become missing evidence.
Source data remains untouched. Full repair uses the original reader and can
still interpret these legacy rows. A wall-clock hourly repair remains due after
a failed/cancelled full attempt; its next deadline starts at successful completion,
so a long full pass cannot force an endless sequence of full passes.

The original periodic full pass, complete history validation, matrix generation,
stats refresh, source evidence, and pool/query timeout limits are preserved.
Full repair remains the backstop for existing untracked source mutations or
missing clean matrix jobs; this change does not add source deletion tombstones.

## Query cost and phase evidence

MATERIALIZED candidate IDs and parameterized LATERAL point reads prevent cached
generic plans from replacing small candidate lookups with whole-relation joins.
The three candidate GIN indexes use fastupdate=off so unrelated insertions cannot
grow a pending list that every scoped read must scan. The tradeoff is immediate
index maintenance on source writes. Timestamp discovery gets its own evidence
index and uses uncached parameterized planning for each watermark.

The #253 recorder now includes scope_claim_read: actual selected receipt-claim
rows and cumulative receipt/thin-manifest JSON bytes, including failed/cancelled
queries. Existing sample_page_read, receipt_page_read, list_targets and
snapshot_retire counters remain observable. Logical calls are not represented as
physical checkouts; tests separately measure the pool acquisition counter.

Managed PostgreSQL 17.11 measurements on runtime implementation fc3eeb61:

| Metric | 1,000 irrelevant rows per source | 10,000 |
| --- | ---: | ---: |
| Targets / samples / receipts / stored snapshots returned | 4 / 1 / 2 / 1 | 4 / 1 / 2 / 1 |
| Sample and receipt JSON bytes | 298 | 298 |
| Claim rows / claim JSON bytes | 8 / 1,312 | 8 / 1,312 |
| Pool checkouts for scoped acquisition | 10 | 10 |

Actual prepared queries passed both default and forced generic planning.
Claims-by-package visited 5 rows, shared blocks 14 to 15; claims-by-symbol visited
6 rows, blocks 16 to 16; sample acquisition visited 3 rows, blocks 11 to 12.
No source relation sequential scans or filtered corpus rows occurred. Tests
retain the adversarial freshly inserted GIN workload; no VACUUM or planner
disable was used to manufacture these bounds.

These bounds describe source acquisition for scope, samples, receipts and
snapshots. They do not claim the unchanged global NetworkCounts/stats aggregate,
full repair, or genuinely related package/symbol history has constant cost.

## Verification and release gate

DEVHOTEL_PREDEPLOY_V1 / TEST_ROUTING_V1 / NO_ORCA_FALLBACK_V1 apply.

The task's managed-git web room is jq1ecv2k (issue174-bounded), with PostgreSQL
17.11, Go 1.26.5 and Playwright 1.63.0 / Chromium 153. Initial empty room umbr1x4b
was slept after its transient workspace could not survive sleep/wake. No Orca,
physical device, production restart or production data mutation was used.

The real database suite compares full/incremental semantic snapshot, shard,
cluster and job output, retaining all fields except materialization clocks/row
IDs. It covers undeclared resolved packages, cross-major receipts, global symbol
ownership, quarantine retirement, Maven JDK boundaries, nonempty matrix history,
legacy ambiguity, cancellation and hourly full repair. Store tests also cover
historical index creation, explicit rollback/reapply, generic plans and repeated
idle change windows. CI requires named builder guards and rejects skipped PG
tests. The final head's exact CI and DevHotel HTTP/Playwright evidence is recorded
in PR #254 and the job release-evidence artifact.

Production remains the incident's recorded v0.1.149 / 3b6bb929 target until a
separate exact-SHA release/deploy is performed. Neither healthz alone nor this
synthetic managed fixture establishes sustained production recovery.

## Migration and rollback

One pending migration, 0036_builder_scope_indexes.sql, adds three immutable
read-only SQL helpers and nine indexes (two are sparse ambiguity guards).
It changes no source/materialized rows and requires no receipt-attribution
backfill program. The array helper uses a parsed SQL RETURN body so PostgreSQL
binds its helper dependency at definition time, including index maintenance's
restricted search path.

The deploy gate accepts only this filename and its exact reviewed file digest
(with CRLF normalized to LF). Any SQL, literal, index-option, ordering or comment
change requires a reviewed digest update; the general SQL allowlist is unchanged.
The new migration count is 37 (previous 36, latest 0035_recent_wanted_demand.sql).
Index creation is transactional and takes ordinary index-build locks: account
for one-time migration work before process startup; incremental read bounds do
not describe this initial operation.

Preferred rollback restores the prior binary/image and leaves additive schema
objects and the migration marker in place. Source and materialized data survive.
Optional schema cleanup after restoring the old binary drops the nine named
builder_* indexes, then csx_builder_unsafe_keys(jsonb), csx_builder_coords(jsonb),
csx_builder_coord(text), and removes only the marker whose version is exactly
0036_builder_scope_indexes.sql. This permits a later clean reapplication.
The PG lifecycle test proves both old-reader behavior and reapplication on
pre-existing data; cleanup is not part of automatic production rollout.

After merge, release/deploy must use the exact resulting main SHA, successful
same-SHA Test/Release/Farm runs, validated artifacts and served /version identity.
Retain existing rollback/source-ledger guards. Final production acceptance still
requires five active-builder rounds of real user routes, sustained HTTP 200 with
503=0, convergence, pool_busy=0, query_timeout=0, max DB wait <=3s, and no
OOM/restart/rollback. This PR does not close #174 before those measurements.
