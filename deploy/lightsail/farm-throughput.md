# Farm scalar throughput evidence

The manual `farm-throughput.yml` workflow reads one fixed SQL statement and
retains scalar counts. It does not call a runtime admin endpoint, claim work,
change server state, execute the observer, or establish release readiness.
Refs: CodeSampleX #364 and CodeSampleX-Farm #27.

The workflow must run from canonical main with successful actual main CI for
that exact operational commit. It uses the existing protected production SSH
identity and pinned host key, and shares the noncanceling production concurrency
group. The expected server revision must match narrow container metadata before
and after the read. Public verifier identity and the three slot-label SHA256
values must come from reviewed Farm identity evidence. Missing, duplicate, or
malformed inputs fail before SSH. A label hash binds an expected label; it is
not evidence that the slot is active.

The query uses one database statement timestamp for a `[start, end)` window of
exactly one hour. Every count is over committed rows visible to that statement,
classified by their stored server timestamps. It does not reconstruct receipt
commit times or HTTP acknowledgement times. The database command has a 1 second
statement timeout, 500 millisecond lock timeout, read-only transaction default,
and JIT disabled. Each command attempts a 3 second read/client deadline and an
8 KiB output limit. The inherited PR372 helper kills its direct client only;
a descendant retaining stdout can delay cleanup beyond that deadline. The
remote program also runs under an external 15 second timeout with a 2 second
kill grace, and the runner uses a 22 second read deadline plus a 1 minute
workflow step timeout. The database statement/lock limits are independent.
An error, timeout, identity change, or invalid result retains unavailable/null
evidence, never a measured zero. It does not retry or raise these limits.

The artifact contains these distinct measurements:

- `receipts.accepted` and `receipts.pass`: distinct stored receipt IDs for the
  bound public verifier peer in the window. Repeated submissions of the same
  receipt ID do not add rows. PASS means `contract_result = 'PASS'`.
- `sampleFirstPass.unambiguousNodeSamples`: distinct samples whose earliest
  historical PASS timestamp falls in the window and belongs only to the bound
  peer. Candidate samples come from that peer's window PASS receipts, but the
  earliest timestamp is computed over their complete PASS history across all
  peers. This counts samples, not network PURLs or `/admin/api/farm` firstProven.
- `sampleFirstPass.sharedFirstTimestampSamples`: samples for which the node and
  another peer share the earliest timestamp. They are separated from exclusive
  first-PASS attribution. `unknownHistoricalTimestampSamples` identifies
  candidate samples with a NULL historical PASS timestamp and excludes them
  from both first-PASS claims.
- `gen.currentSlotAttributedDrafts`: distinct draft sample IDs created in the
  window whose current worker label matches one of the three bound labels.
  Per-slot counts and their sum are retained. There is no live-session join,
  so session renewal alone does not hide these rows.
- `gen.createdAndUpdatedTimestampEqual` and
  `gen.postcreationTimestampChanged`: the two timestamp categories within the
  same current-label cohort. A draft upsert preserves `created_at` but can
  replace `session_id`, `worker_label`, and `updated_at`. Timestamp equality
  is not an immutable audit trail, and these counts do not prove the original
  writer. An edit after the window end still belongs to the original creation
  window.

`cleanWindowEligible` says only whether the current server container started
at or before the window start. A warmup read remains available with this flag
false and all valid counts intact. A true flag still requires separate canonical
Farm health evidence proving the three author slots, session renewal, verifier
activity, version compatibility, and a clean matching hour. No artifact from
this workflow alone closes #27.

Only aggregate counts, UTC bounds, approved revision/image metadata, fixed scope
strings, and an input-binding hash leave the collector. The binding hash is
SHA256 of ASCII `peer + "\n" + slot1Hash + "\n" + slot2Hash + "\n" + slot3Hash + "\n"`.
Raw labels, hostnames, session IDs, sample IDs, receipt IDs, SQL errors, manifests,
receipt JSON, credentials, and query text are not emitted.
