# #174: incremental builder source selection

Canonical incident: https://github.com/r2cuerdame/CodeSampleX/issues/174

This candidate removes the exhaustive sample, receipt-claim and stored-snapshot
key reads from PostgreSQL incremental passes. Full passes, the hourly repair
cadence, overlap/resume clocks, and pool/query guards remain in place. It is a
structural reduction; it does not by itself establish production recovery.

## Selection and attribution

An incremental pass selects whole package identities across all major versions.
It needs those versions for clusters, receipt regressions, and JDK boundaries.
Samples are selected by the union of their declared packages, their stated
subject, and the packages established by their receipts. The latter are derived
with the existing Go whole-list validator; builder and CLI receipts are never
assumed to obey HTTP ingest's declared-name restriction.

The sample and receipt projections are stored beside their source JSON, with
a fingerprint of PostgreSQL's JSON serialization. Every current sample/receipt
writer updates source and projection in the same transaction. A receipt-ID
conflict never replaces the saved receipt's projection with the retry's input.
Sample replacements retain previous package/symbol invalidations.

The projection stores each receipt's complete validated package set. Target
selection reads compact claims sharing symbols with the selected packages,
then invokes the unchanged global narrowest-claim/subject algorithm. It does
not compute widths from a package-filtered set. A narrow or subject claim in
another package or ecosystem still wins. Changes to a sample invalidate its
symbol competitors, so quarantine can restore the previously losing claims.

Sample page ordering, complete per-sample receipt histories, top-sample limits,
pre-limit shard counts, and receipt evidence contents are unchanged. Stored
snapshot retirement uses an indexed package-coordinate query. Source-only and
retired majors are included when rebuilding/retiring affected shards.

## Migration and failure behavior

There is one additive migration: `0036_builder_projections.sql` (37 recorded
migrations including the existing two differently named `0034` migrations).
It adds source projections and their lookup/staleness indexes, target-coordinate
expression indexes, and timestamp indexes for changed-source selection.

After SQL migrations, Go backfills stale projections in transactions of at most
256 source rows. Partial staleness indexes find only the unfinished subset on
restart. Successfully committed pages are reusable after cancellation; a failed
page rolls back. Initial backfill deliberately invalidates source samples, so
the first pass after this migration may have a large legitimate change set.
Index creation and this one-time backfill require a measured migration window.

Each scoped read checks projection readiness in the same repeatable-read
transaction as its selection. Direct SQL or a rolled-back old binary can write
unprojected rows, but cannot silently hide them from that reader. Such rows block
incremental aggregation until migration/backfill repairs them. Typed JSON that
cannot be safely projected blocks backfill with a row ID and error. A modified
previously projected immutable receipt also blocks repair: its removed evidence
must be reconciled explicitly. A v2 receipt whose package list fails the existing
whole-list validator deterministically establishes no packages, exactly as before.

The exhaustive full/hourly path retains its existing parser semantics; it is
also the reference implementation used for output parity. No source read error
is converted into an empty result or an implicit exhaustive incremental fallback.
Failed/cancelled passes do not advance `lastRun`, pass count, or the persisted
completion clock. Earlier committed output chunks can be retried idempotently.

The SQL target coordinate function follows the last-`@` split, escaped/raw
scoped names and percent decoding used by `ParsePURL`. Non-UTF8 decoded text
cannot be indexed as PostgreSQL text and fails closed during migration/write;
operators must reconcile such invalid legacy coordinates, not skip their rows.

## Validation contract

The canonical PR records the exact tested commit, CI run, DevHotel room/session,
build/archive identity, commands, JSON no-skip counts, screenshots and outcomes.
Working-tree runs are labeled separately from exact-commit acceptance.

Required PostgreSQL 17 checks include:

- Full/incremental snapshot, shard, cluster, package and job content equality.
  Only untouched-document generation clocks are normalized for comparison.
- Undeclared resolved packages, multiple versions/majors, global competing
  symbols, subjects, malformed lists, quarantine and source replacement.
- Stale legacy/backfill behavior, transactional writer rollback, cancellation,
  completion-clock preservation and successful retry.
- 1,000 to 10,000 irrelevant samples/receipts/targets with fixed selected rows,
  payload bytes and checkouts, plus `EXPLAIN (ANALYZE, BUFFERS)` of actual queries.
- Whole-builder phase measurements at 10x irrelevant sample growth. Existing
  `sample_page_read`, `receipt_page_read`, and `snapshot_retire` units stay intact;
  `target_projection_read` records compact projection rows and constructed JSON
  bytes. These bytes are not PostgreSQL heap IO or wire bytes. Plan buffers and
  actual acquired-connection deltas are measured separately in tests.

CI requires the builder PostgreSQL parity and failure tests to appear as PASS
in JSON output and rejects skipped PostgreSQL tests. DevHotel web acceptance
covers real seeded sample code, package and symbol pages, desktop/mobile
viewports, navigation/search, console and network failures, and HTTP smoke.
The managed room must be slept after final verification.

## Release/deploy evidence to carry forward

Baseline production is v0.1.149 / `3b6bb9292488d6e9fc2b62ff0db1d13e177ebc61`.
Its deployment passed but sustained incident recovery failed. Health alone is
not evidence of recovery. Refresh the actual serving identity before a rollout.

For a later production release, retain the final PR head and green CI run,
independent review verdict, DevHotel PASS evidence, selected release tag/SHA,
artifact checksums, exact previous production SHA, migration count/version and
the migration/backfill duration. The production workflow input is
`side_effect_class=additive-migration`, `tracking_issue=174`; its normal gates
still apply. This implementation task does not dispatch production deployment.

After a permitted deployment, verify `/version` against the immutable target,
real `/samples` and sample/package/symbol details, and the builder phase logs.
Retain the production and post-deploy observation workflow evidence. #174 stays
open until sustained real user routes have HTTP 200/503=0, at least five
active-builder rounds, bounded latency, zero pool-busy/query-timeout events,
maximum DB wait <=3s, convergence, and no OOM/restart/rollback. Existing stats,
dependency-axis aggregation, full repair costs and unrelated serving-path tails
still require live measurement; this PR does not claim to eliminate them.

## Reversal

An application rollback can retain the additive schema. Old-binary writes are
found by the stale indexes when the current binary returns. To remove the
migration, first stop every binary using the scoped APIs, retain a backup and
the release evidence, then execute in one operator-controlled transaction:

```sql
BEGIN;
DROP INDEX IF EXISTS evidence_agg_builder_coord_idx;
DROP INDEX IF EXISTS snapshots_builder_coord_idx;
DROP INDEX IF EXISTS evidence_agg_builder_changed_idx;
DROP INDEX IF EXISTS samples_builder_created_idx;
DROP FUNCTION IF EXISTS builder_purl_coord(text);
ALTER TABLE samples
  DROP COLUMN builder_coords, DROP COLUMN builder_purls,
  DROP COLUMN builder_symbols, DROP COLUMN builder_subject,
  DROP COLUMN builder_source_hash, DROP COLUMN builder_previous_purls,
  DROP COLUMN builder_previous_symbols;
ALTER TABLE receipts
  DROP COLUMN builder_packages, DROP COLUMN builder_coords,
  DROP COLUMN builder_claim, DROP COLUMN builder_source_hash;
DELETE FROM schema_migrations WHERE version='0036_builder_projections.sql';
COMMIT;
```

Dropping projected columns removes their dependent indexes. Raw evidence,
manifests, receipts, materialized outputs and pre-existing indexes are retained.
