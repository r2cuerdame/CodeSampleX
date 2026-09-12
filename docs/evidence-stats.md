# Read-only evidence statistics

`csx stats --evidence-only` reads the existing local evidence/upload counters
without the normal dashboard startup path. `--json` is optional and can appear
before or after `--evidence-only`. The global `--debug` prefix is also supported.
Other combinations remain invalid; ordinary `csx stats` and `csx stats --json`
retain their existing activation, daemon and dashboard behavior.

This is an intentional activation exception for a read-only diagnostic. Only
these exact valid invocations skip the first-run stamp. They do not initialize
a profile, read configuration, contact a daemon/server, migrate/backfill the
store, inspect CAS/Docker, or upload anything. A missing or incompatible store
is unavailable; the diagnostic does not repair it or create its parent path.

The output includes the existing three text rows: pending evidence batches,
pending upload reports, and their sum as pending queue depth. The two source
counts retain their existing 1000-row caps. Evidence uses the pending-observation
index; typed uploads use the existing retryable-attempt filter and queue index.
The terminal-refusal total is durable history, separate from the pending queue.
No observation, queued payload, coordinate, sample ID or refusal reason is read
for output. The narrow JSON document contains these queue fields, terminal total
and optional evidence-upload metadata; it is not the full dashboard document.

`lastUpload` and `lastUploadAttempt` are evidence-uploader timestamps, not all
typed-report delivery timestamps. Missing metadata remains unmeasured. Stored
timestamps must parse; metadata values are bounded to the producer's 512-rune
limit before being returned by SQLite. A known uploader refusal message is
projected to the fixed text `evidence: the server refused N batch(es)` without
its raw reason. Other nonempty errors become `upload failed`. This count still
describes the last uploader attempt and does not prove membership in the current
pending queue. Invalid refusal counts, invalid encoding, malformed timestamps
or any required query failure make the entire command fail with empty stdout
and a fixed unavailable diagnostic. Failed reads never become measured zeros.

The accessor opens an escaped `mode=ro` SQLite URI, with `query_only`, a 250 ms
busy timeout, one private connection and a deferred read transaction. Existing
typed accessors share that connection, so the snapshot stays consistent while
a WAL writer continues. An overall five-second context bounds the database
work; it does not promise an operating-system scheduling/filesystem/cleanup
deadline. There are no retries. Compatibility is established by the actual
required tables, columns and indexes, never just `schema_version`, whose value
does not distinguish all additive migrations.

Read-only does not mean absence of SQLite locking or WAL sidecar bookkeeping.
SQLite may maintain/create its normal `-wal`/`-shm` files when permissions allow;
the command does not change database data, schema, journal mode or activation.
It deliberately does not use `immutable=1`: that would incorrectly assume the
live database cannot change. See [SQLite WAL read-only databases](https://www.sqlite.org/wal.html#read_only_databases).

Tests cover a separate full CLI-dispatch process while a synthetic initialized
WAL profile has a competing `BEGIN IMMEDIATE` writer, exact committed counts,
unchanged activation, both formats, missing/legacy/corrupt stores, lost required
indexes, metadata corruption/privacy, queue caps, exclusive-lock failure,
caller cancellation, escaped profile paths, and SQLite sidecar behavior.
All database fixtures are disposable local test data.

Refs #377 and #364. This provides an opt-in narrow path; the broader default
dashboard's startup/error behavior remains tracked separately. Farm adoption
must follow publication and canonical installation of a signed CLI release
supporting this flag; an older CLI must not receive it early.
