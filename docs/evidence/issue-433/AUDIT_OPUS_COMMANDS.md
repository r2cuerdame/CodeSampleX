# Reproduction commands — AUDIT_OPUS (issue #433)

Every command below is read-only: `SELECT`, `EXPLAIN`, a log read, or an HTTP
`GET`. Nothing here writes, migrates, restarts or deploys. Run them from a
shell that can reach production over SSH.

Two conventions used throughout:

- **Pipe SQL over stdin, never through `-c`.** Three layers of quoting
  (shell → ssh → docker → psql) corrupt anything non-trivial:
  ```sh
  ssh -i "$HOME/.ssh/lightsail-csx-r3" ubuntu@54.116.158.230 \
    "docker exec -i codesamplex-db-1 psql -U csx -d csx -q" < query.sql
  ```
- **`docker exec -i` consumes stdin.** A multi-statement `bash -s` script that
  contains several `docker exec` calls dies after the first one, and the output
  looks like a complete answer. Either send one SQL file per `ssh` call (what
  this audit did) or redirect each exec's stdin from `/dev/null`.

Shorthands for the rest of this file:

```sh
SSH='ssh -o ConnectTimeout=30 -i /c/Users/recue/.ssh/lightsail-csx-r3 ubuntu@54.116.158.230'
PSQL="docker exec -i codesamplex-db-1 psql -U csx -d csx -q"
# usage: $SSH "$PSQL" < some.sql
```

---

## 0. Establish the measurement context first

Do this before anything else. Two of this audit's numbers are only
interpretable against it, and an in-progress deploy invalidates all HTTP
measurements.

```sh
# host load, steal, memory, container ages
$SSH 'uptime; nproc; vmstat 5 6; free -m; top -bn1 | head -20'

# is a deploy running? (see the confound note in AUDIT_OPUS.md)
$SSH 'ls -d /opt/codesamplex/.deploy-lock 2>/dev/null && echo LOCK-HELD || echo lock-free
      docker ps --format "{{.Names}} {{.Status}}"
      docker inspect codesamplex-server-1 --format \
        "started={{.State.StartedAt}} restarts={{.RestartCount}} oom={{.State.OOMKilled}}"
      docker inspect codesamplex/csx-server:latest --format "image_created={{.Created}}"'

# rule out container CPU limits, so %st is provably hypervisor steal (P0-3)
$SSH 'cat /sys/fs/cgroup/cpu.stat
      for c in codesamplex-server-1 codesamplex-db-1 codesamplex-caddy-1; do
        docker inspect $c --format "'"'"'{{.Name}} cpus={{.HostConfig.NanoCpus}} quota={{.HostConfig.CpuQuota}} mem={{.HostConfig.Memory}}'"'"'"
      done
      iostat -x 2 2'

# effective pool + interval configuration (P0-1a, P0-2)
$SSH 'docker inspect codesamplex-server-1 --format "{{range .Config.Env}}{{println .}}{{end}}" \
      | grep -iE "CSX_DB|CSX_SNAPSHOT|CSX_VERSION|CSX_BUILD" | sed "s/=.*PASS.*/=<redacted>/i"'
```

Expected at audit time: `load 7–13` on `nproc=2`, `st` column 76–80,
`nr_throttled 0`, `CSX_DB_READ_CONNS=2`, `CSX_DB_READ_WAIT=250ms`,
`CSX_DB_MAX_CONNS` empty (→ 8), `CSX_SNAPSHOT_INTERVAL=5m`.

---

## R1. The 94% package-page 503 rate (P0-1)

**Draw the sample.** Deterministic pseudo-random over all 3,415 distinct
package coordinates — rerunning gives the identical 50 so the measurement is
repeatable:

```sql
-- rand50.sql
\pset pager off
\pset format unaligned
\pset fieldsep '|'
\t
SELECT p.ecosystem||'|'||p.name
  FROM (SELECT DISTINCT ecosystem, name FROM packages) p
 ORDER BY md5(p.ecosystem||p.name)
 LIMIT 50;
```

```sh
$SSH "$PSQL" < rand50.sql | grep '|' > rand50.txt
wc -l < rand50.txt      # 50

while IFS='|' read -r eco name; do
  [ -z "$name" ] && continue
  code=$(curl -s -o /dev/null -w "%{http_code} %{time_starttransfer}" \
         --max-time 70 "https://codesamplex.dev/$eco/$name")
  echo "$code $eco/$name"
done < rand50.txt | tee probe_rand50.txt | awk '{print $1}' | sort | uniq -c
```

Observed 15:03–15:06Z: `3 200`, `47 503`. Re-run on the first 20 rows at
15:25Z against a 4-minute-old healthy container: `1 200`, `19 503`.

**Confirm it is a rendered error page, not an edge timeout:**

```sh
curl -s -D - -o /dev/null --max-time 40 "https://codesamplex.dev/npm/react" | head -20
curl -s --max-time 40 "https://codesamplex.dev/npm/react" | grep -oE "<title>[^<]*</title>"
for i in 1 2 3 4 5 6; do
  curl -s -o /dev/null -w "%{http_code} ttfb=%{time_starttransfer}\n" \
    --max-time 60 "https://codesamplex.dev/npm/react"
done
```

Expect `503`, `Retry-After: 2`, `<title>Data unavailable — CodeSampleX</title>`,
`Vary: Accept-Language`, and 6/6 failures.

**Show it is not cluster-size dependent** (the A/B that killed the obvious
hypothesis). Build the two lists, then probe both with the loop above:

```sql
-- high.sql
\pset pager off
\pset format unaligned
\pset fieldsep '|'
\t
WITH cl AS (SELECT ecosystem, package_name, count(*) AS clusters
              FROM failure_clusters GROUP BY 1,2)
SELECT p.ecosystem||'|'||p.name||'|'||COALESCE(c.clusters,0)
  FROM packages p
  LEFT JOIN cl c ON c.ecosystem=p.ecosystem AND c.package_name=p.name
 GROUP BY p.ecosystem, p.name, c.clusters
 ORDER BY COALESCE(c.clusters,0) DESC
 LIMIT 15;

-- low.sql: same, with
--   WHERE COALESCE(c.clusters,0) BETWEEN 0 AND 3  and  ORDER BY 3 DESC, 1, 2
```

Observed: HIGH 12/15 503, LOW 13/15 503.

**The controls that must return 200 in the same minutes** — this is what makes
it a package-page finding rather than an outage:

```sh
for u in /version /robots.txt / /dependencies /findings; do
  curl -s -o /dev/null -w "%{http_code} ttfb=%{time_starttransfer} $u\n" \
    --max-time 70 "https://codesamplex.dev$u"
done
```

`/version` and `/robots.txt` touch no store; `/` `/dependencies` `/findings` are
DB-backed and still answered 200 at ~0.6 s TTFB.

**The pool-busy line, server-side (mechanism a):**

```sh
$SSH 'docker logs --since 30m codesamplex-server-1 2>&1 | grep "db pressure" | tail -40'
# read pool_busy_total at two timestamps to get a rate:
# 14:34Z -> 8, 14:44Z -> 102, 15:08Z -> 316
```

**The 76-second query (mechanism c).** This is the statement at
`internal/serverstore/pg.go:3090-3109` with
`CurrentFailureClusterPredicateSQL` (`internal/serverstore/currentclusters.go:26`)
substituted in. It is a plain `EXPLAIN ANALYZE` of a `SELECT`:

```sql
-- explain_clusters.sql
\pset pager off
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING ON)
SELECT id, COALESCE(ecosystem,''), COALESCE(package_name,''),
       COALESCE(symbol,''), COALESCE(stage,''), COALESCE(error_fp,''),
       COALESCE(error_code,''), COALESCE(observation_count,0),
       COALESCE(env_summary::text,''), COALESCE(hypotheses::text,''),
       COALESCE(regression_candidate,false), COALESCE(versions::text,''),
       COALESCE(env_variants::text,'[]'), COALESCE(evidence_breakdown::text,'{}'),
       COALESCE(outer_commands::text,'[]'),
       first_seen, last_seen
  FROM failure_clusters
 WHERE package_name='golang.org/x/sys'
   AND (COALESCE(evidence_quality,'legacy-evidence-incomplete')
          NOT IN ('missing','legacy-evidence-incomplete')
        OR COALESCE(error_fp,'') = '')
 ORDER BY observation_count DESC, id;
```

```sh
$SSH "$PSQL" < explain_clusters.sql
```

Expect `Execution Time: ~76000 ms`, `I/O Timings: shared read=~68700`,
`Buffers: shared hit=396 read=6261`, `rows=9352`. It will be slower on a cold
cache and faster right after a full pass; the `hit`/`read` ratio is the stable
part of the finding.

> Cost note: this one statement reads ~49 MB and takes over a minute on a host
> already at 80% steal. Run it once, not in a loop.

---

## R2. The builder never finishes between passes (P0-2)

```sh
$SSH 'docker logs --since 6h codesamplex-server-1 2>&1 \
      | grep -E "builder pass (start|complete)"'
```

Look for two things:

1. `total=` on a `complete` line exceeding `CSX_SNAPSHOT_INTERVAL`. Observed:
   `total=8m36.4145111s` against `5m`.
2. A `start` with no matching `complete`, or two `start` lines with the same
   `since=` and one completion between them. Observed `14:32:04` and `14:35:13`
   both `since=2026-09-15T13:45:31Z`, one `complete` at `14:43:49`; then
   `14:48:50` start with nothing by `15:09`.

The N+1 inside the pass is on the phase lines from the same log:

```sh
$SSH 'docker logs codesamplex-server-1 2>&1 \
      | grep -E "phase=(target_evidence|snapshot_write)" | grep "event=exit"'
```

Compare `logical_calls=721 items=12433` (target_evidence) against
`logical_calls=12 items=721` (snapshot_write) — same cardinality of work, one
batched and one not.

Container logs only cover the life of the current container, so after a
recreate this history is gone. Capture it while the container is up.

---

## R3. The CROSS_PASS label test (P0-4)

The rule under test is `sampleStatusFromReceipts` in
`internal/httpapi/verifications.go:592-650`. `crossPass` can only be true when
at least two **distinct** peers filed a PASS, so "≥2 distinct PASS peer_ids" is
the exact SQL equivalent.

```sql
-- crosspass.sql
\pset pager off
WITH pp AS (
  SELECT s.sample_id, s.status, s.quarantined,
         count(DISTINCT r.peer_id) AS pass_peers,
         count(*)                  AS pass_receipts,
         count(DISTINCT r.env_hash) AS pass_envs
    FROM samples s
    JOIN receipts r ON r.sample_id=s.sample_id AND r.contract_result='PASS'
   GROUP BY 1,2,3)
SELECT 'C1 live_CROSS_PASS_total' AS metric, count(*)::text AS v
  FROM pp WHERE NOT quarantined AND status='CROSS_PASS'
UNION ALL SELECT 'C2 rule_FAILS (1 pass peer)', count(*)::text
  FROM pp WHERE NOT quarantined AND status='CROSS_PASS' AND pass_peers<2
UNION ALL SELECT 'C3 rule_holds (>=2 pass peers)', count(*)::text
  FROM pp WHERE NOT quarantined AND status='CROSS_PASS' AND pass_peers>=2
UNION ALL SELECT 'C4 live_STABLE_total', count(*)::text
  FROM pp WHERE NOT quarantined AND status='STABLE'
UNION ALL SELECT 'C5 STABLE_rule_FAILS (<3 pass peers)', count(*)::text
  FROM pp WHERE NOT quarantined AND status='STABLE' AND pass_peers<3
UNION ALL SELECT 'C7 distinct_pass_peer_ids_total',
  (SELECT count(DISTINCT peer_id)::text FROM receipts WHERE contract_result='PASS');

SELECT peer_id, count(*) AS pass_receipts, count(DISTINCT sample_id) AS samples,
       min(created_at)::date AS first, max(created_at)::date AS last
  FROM receipts WHERE contract_result='PASS'
 GROUP BY 1 ORDER BY 2 DESC;
```

Expect `C1=6621`, `C2=5713` (86.3%), `C3=908`, `C4=355`, `C5=0`, `C7=5`, and one
peer key holding 6,373 of 10,467 PASS receipts.

**Show which code path wrote them** — a single receipt cannot satisfy
`peer ≠ origin`, so any sample with one receipt was promoted elsewhere
(`internal/serverstore/pg.go:1871-1878`):

```sql
-- crosspass_why.sql
\pset pager off
WITH r AS (SELECT sample_id, peer_id, contract_result FROM receipts),
agg AS (SELECT s.sample_id,
          count(DISTINCT CASE WHEN r.contract_result='PASS' THEN r.peer_id END) AS pass_peers,
          count(DISTINCT r.peer_id) AS all_peers,
          count(*) AS receipts
     FROM samples s JOIN r ON r.sample_id=s.sample_id
    WHERE NOT s.quarantined AND s.status='CROSS_PASS'
    GROUP BY 1)
SELECT receipts, all_peers, count(*)
  FROM agg WHERE pass_peers<2 GROUP BY 1,2 ORDER BY 3 DESC;
```

Expect the top row `receipts=1 all_peers=1 count=5117`.

**Is it live or legacy?** Bucket the same failure by week:

```sql
WITH pp AS (
  SELECT s.sample_id, s.created_at, count(DISTINCT r.peer_id) AS pass_peers
    FROM samples s JOIN receipts r ON r.sample_id=s.sample_id AND r.contract_result='PASS'
   WHERE NOT s.quarantined AND s.status='CROSS_PASS' GROUP BY 1,2)
SELECT to_char(date_trunc('week',created_at),'YYYY-MM-DD') AS week,
       count(*) AS cross_pass,
       count(*) FILTER (WHERE pass_peers<2) AS rule_fails,
       round(100.0*count(*) FILTER (WHERE pass_peers<2)/count(*),1) AS pct_fail
  FROM pp GROUP BY 1 ORDER BY 1;
```

Expect 100.0% from the week of 2026-08-24 onward.

**The environment monoculture:**

```sql
SELECT lower(COALESCE(r.receipt->'environment'->>'os','(none)')) AS os,
       lower(COALESCE(r.receipt->'environment'->>'arch','(none)')) AS arch,
       count(*) AS pass_receipts, count(DISTINCT r.sample_id) AS samples
  FROM receipts r WHERE r.contract_result='PASS' GROUP BY 1,2 ORDER BY 3 DESC;

SELECT count(DISTINCT env_hash) FROM receipts WHERE contract_result='PASS';
SELECT status, quarantined, count(*) FROM samples GROUP BY 1,2 ORDER BY 3 DESC;
SELECT status, reason, count(*), min(created_at)::date, max(created_at)::date
  FROM verification_jobs GROUP BY 1,2 ORDER BY 3 DESC;
```

Expect one row `linux|x64|10467|8301`, `57` distinct env hashes, **no
`MATRIX_PASS` row at all** in the status table, and 3,251 `done|matrix` jobs.

---

## R4. Database time attribution (P1-5, P1-6, P1-7, P1-8, P1-9)

`pg_stat_statements` is preloaded (`shared_preload_libraries=pg_stat_statements`)
and has never been reset since `2026-09-09 05:49:06Z`. **Do not reset it** — it
is the only historical record of DB cost on this host.

Note for PostgreSQL 17: the column is `shared_blk_read_time`, not
`blk_read_time`. Using the old name fails the whole statement.

```sql
-- top_queries.sql
\pset pager off
\pset format unaligned
\pset fieldsep ' | '
SELECT queryid,
       round(total_exec_time::numeric/1000,1) AS tot_s,
       calls,
       round(mean_exec_time::numeric,2) AS mean_ms,
       round(max_exec_time::numeric,1)  AS max_ms,
       rows,
       shared_blks_read AS blk_rd,
       round(shared_blk_read_time::numeric/1000,1) AS rd_s,
       round(jit_generation_time::numeric/1000,1)  AS jit_s,
       left(regexp_replace(query,'\s+',' ','g'),150) AS q
  FROM pg_stat_statements
 WHERE dbid=(SELECT oid FROM pg_database WHERE datname='csx')
 ORDER BY total_exec_time DESC
 LIMIT 25;

SELECT stats_reset FROM pg_stat_statements_info;
```

The findings' queryids, so they can be matched without re-ranking:

| queryid | finding |
| --- | --- |
| `-8919612395518868614` | P1-5, `failure_clusters` per-package read |
| `5126368844612351286` | P1-6, `builder_purl_coord` body |
| `-1034863950460726248` | P1-7, `failure_clusters` upsert |
| `-4616489319632358898` | P1-7, `compatibility_snapshots` upsert |
| `-1838375435808251149` | P1-8, admin farm aggregate (mean 183 s) |
| `-4544060618235083690` | P1-9, unbounded `SnapshotKeys` |
| `-1717588399536768741` | P1-9, full `evidence_agg` aggregate for a top-N |
| `-7089594382960164887` | P1-9, `COUNT(DISTINCT bucket)`, 6 s of JIT |

Fetch one full statement body at a time (a multi-row `SELECT query` gets
truncated by output caps):

```sql
\pset pager off
\pset format unaligned
\t
SELECT query FROM pg_stat_statements WHERE queryid = -8919612395518868614;
```

**Calls are still accumulating** — read P1-6's counter twice, minutes apart, to
get a rate:

```sql
SELECT calls, round(total_exec_time::numeric/1000,1) AS tot_s
  FROM pg_stat_statements WHERE queryid = 5126368844612351286;
```

Observed `10,416,825 → 10,444,795` over ~40 minutes (~700/min).

**Confirm the P1-6 index is used on reads**, so the 10.4 M calls are write-path
index maintenance and not per-row predicate evaluation:

```sql
\pset pager off
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF)
SELECT DISTINCT purl, symbol FROM evidence_agg
 WHERE builder_purl_coord(purl) = ANY(ARRAY[
   'pkg:golang/golang.org/x/sys@','pkg:npm/react@','pkg:npm/semver@']::text[]);
```

Expect `Index Only Scan using evidence_agg_builder_coord_idx`, and note
`Heap Fetches: 5433` — the visibility map never settles on that table.

**Cache, configuration, scans and indexes (P1-9, P2-18):**

```sql
-- storage.sql
\pset pager off
SELECT name, setting, unit FROM pg_settings WHERE name IN
 ('shared_buffers','work_mem','maintenance_work_mem','effective_cache_size',
  'max_connections','random_page_cost','seq_page_cost','effective_io_concurrency',
  'jit','statement_timeout','track_io_timing','shared_preload_libraries')
 ORDER BY name;

SELECT pg_size_pretty(pg_database_size('csx')) AS db_size;

SELECT c.relname, pg_size_pretty(pg_total_relation_size(c.oid)) AS total
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relkind='r'
 ORDER BY pg_total_relation_size(c.oid) DESC LIMIT 15;

SELECT sum(heap_blks_read) AS heap_read, sum(heap_blks_hit) AS heap_hit,
       round(100.0*sum(heap_blks_hit)/nullif(sum(heap_blks_hit)+sum(heap_blks_read),0),2) AS hit_pct
  FROM pg_statio_user_tables;

SELECT relname, heap_blks_read, heap_blks_hit,
       round(100.0*heap_blks_hit/nullif(heap_blks_hit+heap_blks_read,0),2) AS hit_pct
  FROM pg_statio_user_tables ORDER BY heap_blks_read DESC LIMIT 12;

SELECT relname, seq_scan, seq_tup_read, idx_scan,
       CASE WHEN seq_scan>0 THEN seq_tup_read/seq_scan END AS rows_per_seqscan,
       n_tup_ins, n_tup_upd, n_tup_hot_upd
  FROM pg_stat_user_tables WHERE seq_tup_read > 1000000
 ORDER BY seq_tup_read DESC LIMIT 20;

SELECT relname, indexrelname, pg_size_pretty(pg_relation_size(indexrelid)) AS size
  FROM pg_stat_user_indexes WHERE idx_scan=0
 ORDER BY pg_relation_size(indexrelid) DESC LIMIT 25;

SELECT relname, indexrelname, idx_scan,
       pg_size_pretty(pg_relation_size(indexrelid)) AS size
  FROM pg_stat_user_indexes WHERE relname='samples' ORDER BY idx_scan DESC;
```

`samples_manifest_lower_trgm_idx` shows `26 MB` at `idx_scan=1` (P2-18).

**Container memory limits — the reason the hit ratio cannot improve:**

```sh
$SSH 'for c in codesamplex-db-1 codesamplex-server-1 codesamplex-caddy-1; do
        docker inspect $c --format "{{.Name}} mem_limit={{.HostConfig.Memory}}"; done'
```

`codesamplex-db-1` is limited to `671088640` (640 MB) for a 1,430 MB database.

**Admin timeouts (P1-8):**

```sh
$SSH 'docker logs --since 40m codesamplex-server-1 2>&1 \
      | grep -E "path=/admin/api/farm|preload wanted snapshot"'
```

One `cause=query_timeout` per minute is the 60 s admin poll failing every time.
The unauthenticated shape of the surface (this audit held no admin credential):

```sh
for u in /admin /admin/api/farm; do
  curl -s -o /dev/null -w "%{http_code} ttfb=%{time_starttransfer} $u\n" \
    --max-time 90 "https://codesamplex.dev$u"
done      # both 401
```

---

## R5. Corpus integrity scans (P0-4 support, P2-13 … P2-17)

All exhaustive. `contract_result` values are **`PASS` / `FAIL` / `SKIPPED`,
uppercase** — a lowercase `'pass'` predicate silently matches nothing and makes
every linkage metric read as 100% broken. Confirm the vocabulary first:

```sql
SELECT contract_result, count(*), count(DISTINCT sample_id) FROM receipts GROUP BY 1;
SELECT k, count(*) FROM samples, jsonb_object_keys(manifest) k GROUP BY k ORDER BY 2 DESC;
SELECT quarantine_reason, count(*) FROM samples WHERE quarantined GROUP BY 1 ORDER BY 2 DESC;
```

**Verification linkage and manifest structure:**

```sql
-- integrity.sql
\pset pager off
SELECT 'POP total_samples' AS metric, count(*)::text AS v FROM samples
UNION ALL SELECT 'POP live', count(*)::text FROM samples WHERE NOT quarantined
UNION ALL SELECT 'POP quarantined', count(*)::text FROM samples WHERE quarantined
UNION ALL SELECT 'A1 live_no_receipt_at_all', count(*)::text FROM samples s
  WHERE NOT s.quarantined AND NOT EXISTS (SELECT 1 FROM receipts r WHERE r.sample_id=s.sample_id)
UNION ALL SELECT 'A2 live_no_PASS_receipt', count(*)::text FROM samples s
  WHERE NOT s.quarantined AND NOT EXISTS (
    SELECT 1 FROM receipts r WHERE r.sample_id=s.sample_id AND r.contract_result='PASS')
UNION ALL SELECT 'A4 receipts_on_quarantined', count(*)::text
  FROM receipts r JOIN samples s ON s.sample_id=r.sample_id WHERE s.quarantined
UNION ALL SELECT 'A5 empty_contract', count(*)::text FROM samples s WHERE NOT s.quarantined
  AND COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(s.manifest->'case'->'contract')='array'
       THEN s.manifest->'case'->'contract' ELSE '[]'::jsonb END),0)=0
UNION ALL SELECT 'A6 missing_contractCommand', count(*)::text FROM samples s WHERE NOT s.quarantined
  AND COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(s.manifest->'contractCommand')='array'
       THEN s.manifest->'contractCommand' ELSE '[]'::jsonb END),0)=0
UNION ALL SELECT 'A7 no_case_row', count(*)::text FROM samples s WHERE NOT s.quarantined
  AND (s.case_id IS NULL OR NOT EXISTS (SELECT 1 FROM cases c WHERE c.case_id=s.case_id))
UNION ALL SELECT 'A10 live_with_FAIL_receipt_too', count(*)::text FROM samples s
  WHERE NOT s.quarantined AND EXISTS (
    SELECT 1 FROM receipts r WHERE r.sample_id=s.sample_id AND r.contract_result='FAIL')
UNION ALL SELECT 'A12 orphan_receipts', count(*)::text FROM receipts r
  WHERE NOT EXISTS (SELECT 1 FROM samples s WHERE s.sample_id=r.sample_id)
UNION ALL SELECT 'A13 sample_packages_vs_manifest_mismatch', count(*)::text FROM samples s
  WHERE NOT s.quarantined
    AND (SELECT count(*) FROM sample_packages sp WHERE sp.sample_id=s.sample_id)
        <> COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(s.manifest->'packages')='array'
             THEN s.manifest->'packages' ELSE '[]'::jsonb END),0)
UNION ALL SELECT 'B1 no_symbols', count(*)::text FROM samples s WHERE NOT s.quarantined
  AND COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(s.manifest->'symbols')='array'
       THEN s.manifest->'symbols' ELSE '[]'::jsonb END),0)=0
UNION ALL SELECT 'B2 no_subject', count(*)::text FROM samples s
  WHERE NOT s.quarantined AND COALESCE(s.manifest->>'subject','')=''
UNION ALL SELECT 'B3 case_vs_manifest_symbols_differ', count(*)::text FROM samples s
  WHERE NOT s.quarantined
    AND COALESCE(s.manifest->'symbols','[]'::jsonb) <> COALESCE(s.manifest->'case'->'symbols','[]'::jsonb)
UNION ALL SELECT 'B4 case_vs_manifest_packages_differ', count(*)::text FROM samples s
  WHERE NOT s.quarantined
    AND COALESCE(s.manifest->'packages','[]'::jsonb) <> COALESCE(s.manifest->'case'->'packages','[]'::jsonb)
UNION ALL SELECT 'B5 subject_not_in_packages', count(*)::text FROM samples s
  WHERE NOT s.quarantined AND COALESCE(s.manifest->>'subject','')<>''
    AND NOT (COALESCE(s.manifest->'packages','[]'::jsonb) ? (s.manifest->>'subject'))
UNION ALL SELECT 'E1 goal_template_verify_prefix', count(*)::text FROM samples s
  WHERE NOT s.quarantined AND COALESCE(s.manifest->'case'->>'goal','') ~ '^verify '
UNION ALL SELECT 'E1b goal_template_bare_purl', count(*)::text FROM samples s
  WHERE NOT s.quarantined AND COALESCE(s.manifest->'case'->>'goal','') ~ '^verify pkg:'
UNION ALL SELECT 'E3 subject_is_raw_purl', count(*)::text FROM samples s
  WHERE NOT s.quarantined AND COALESCE(s.manifest->>'subject','') ~ '^pkg:';
```

Expected: `A1=0 A2=0 A5=0 A6=0 A7=0 A12=0 A13=0`, `A4=1419`, `A10=166`,
`B1=1228`, `B2=1809`, `B3=49`, `B4=1`, `B5=0`, `E1=5406`, `E1b=1871`,
`E3=5261`.

**Symbol support (P2-13).** The matcher is deliberately permissive — it splits
the symbol on the last `.`, `#`, `/` or `:` and looks for that tail
case-insensitively anywhere in the concatenated contract text. Without the `#`
and `/` in the split set, member spellings like `LRUCache#fetch` are
false positives, which inflates the count by ~2×:

```sql
-- symbols.sql
\pset pager off
WITH s AS (SELECT sample_id, manifest FROM samples WHERE NOT quarantined),
 sym AS (
   SELECT s.sample_id, sy.value AS symbol,
     (SELECT string_agg(c.value,' ') FROM jsonb_array_elements_text(
        CASE WHEN jsonb_typeof(s.manifest->'case'->'contract')='array'
             THEN s.manifest->'case'->'contract' ELSE '[]'::jsonb END) c(value)) AS ctext,
     (SELECT string_agg(pk.value,' ') FROM jsonb_array_elements_text(
        CASE WHEN jsonb_typeof(s.manifest->'packages')='array'
             THEN s.manifest->'packages' ELSE '[]'::jsonb END) pk(value)) AS pkgs
     FROM s CROSS JOIN LATERAL jsonb_array_elements_text(
       CASE WHEN jsonb_typeof(s.manifest->'symbols')='array'
            THEN s.manifest->'symbols' ELSE '[]'::jsonb END) sy(value)),
 base AS (
   SELECT sample_id, symbol, ctext, pkgs,
          regexp_replace(symbol,'^.*[.#/:]','') AS tail,
          (position(lower(replace(symbol,'@','%40')) in lower(COALESCE(pkgs,'')))>0) AS sym_is_pkg
     FROM sym)
SELECT 'J1 declared_symbol_slots' AS metric, count(*)::text AS v FROM base
UNION ALL SELECT 'J2 slots_tail_absent_from_contract', count(*)::text
  FROM base WHERE position(lower(tail) in lower(COALESCE(ctext,'')))=0
UNION ALL SELECT 'J3 samples_no_supported_symbol', count(*)::text FROM (
  SELECT sample_id FROM base GROUP BY sample_id
   HAVING count(*) FILTER (WHERE position(lower(tail) in lower(COALESCE(ctext,'')))>0)=0) z
UNION ALL SELECT 'J4 slots_symbol_IS_package_name', count(*)::text FROM base WHERE sym_is_pkg
UNION ALL SELECT 'J5 samples_every_symbol_is_pkg_name', count(*)::text FROM (
  SELECT sample_id FROM base GROUP BY sample_id
   HAVING count(*)=count(*) FILTER (WHERE sym_is_pkg)) z
UNION ALL SELECT 'J6 tightest_floor', count(*)::text FROM (
  SELECT sample_id FROM base GROUP BY sample_id
   HAVING count(*) FILTER (WHERE position(lower(tail) in lower(COALESCE(ctext,'')))>0)=0
      AND count(*) FILTER (WHERE sym_is_pkg)=0) z;
```

Expected: `J1=11385 J2=1250 J3=157 J4=397 J5=213 J6=105`.

**Duplicates (P2-15):**

```sql
-- duplicates.sql
\pset pager off
WITH k AS (
  SELECT s.sample_id,
    md5(COALESCE(s.manifest->'packages','[]'::jsonb)::text||'|'||
        COALESCE(s.manifest->'symbols','[]'::jsonb)::text||'|'||
        COALESCE(s.manifest->'case'->'contract','[]'::jsonb)::text) AS ck,
    md5(COALESCE(s.manifest->'packages','[]'::jsonb)::text||'|'||
        COALESCE(s.manifest->'symbols','[]'::jsonb)::text) AS coordk,
    md5(COALESCE(s.manifest->'case'->'contract','[]'::jsonb)::text) AS contractk
    FROM samples s WHERE NOT s.quarantined)
SELECT 'F1 live_samples' AS metric, count(*)::text AS v FROM k
UNION ALL SELECT 'F2 exact_dup_groups', count(*)::text
  FROM (SELECT ck FROM k GROUP BY ck HAVING count(*)>1) x
UNION ALL SELECT 'F3 exact_dup_redundant', COALESCE(sum(c-1),0)::text
  FROM (SELECT count(*) AS c FROM k GROUP BY ck HAVING count(*)>1) x
UNION ALL SELECT 'F4 near_dup_groups', count(*)::text
  FROM (SELECT coordk FROM k GROUP BY coordk HAVING count(*)>1) x
UNION ALL SELECT 'F5 near_dup_redundant', COALESCE(sum(c-1),0)::text
  FROM (SELECT count(*) AS c FROM k GROUP BY coordk HAVING count(*)>1) x
UNION ALL SELECT 'F6 identical_contract_groups', count(*)::text
  FROM (SELECT contractk FROM k GROUP BY contractk HAVING count(*)>1) x
UNION ALL SELECT 'F7 identical_contract_redundant', COALESCE(sum(c-1),0)::text
  FROM (SELECT count(*) AS c FROM k GROUP BY contractk HAVING count(*)>1) x
UNION ALL SELECT 'F8 shared_case_id_groups', count(*)::text
  FROM (SELECT case_id FROM samples WHERE NOT quarantined
         GROUP BY case_id HAVING count(*)>1) x;
```

Expected: `F1=7070 F2=82 F3=82 F4=204 F5=213 F6=518 F7=677 F8=44`.

**Staleness (the null result that matters):**

```sql
-- staleness.sql
\pset pager off
CREATE TEMP TABLE sv AS
  SELECT s.sample_id, p.ecosystem, p.name, p.version, s.created_at,
         (SELECT max(p2.last_seen) FROM packages p2
           WHERE p2.ecosystem=p.ecosystem AND p2.name=p.name) AS pkg_last_seen,
         p.last_seen AS this_version_last_seen
    FROM samples s
    JOIN sample_packages sp ON sp.sample_id=s.sample_id
    JOIN packages p ON p.purl=sp.purl
   WHERE NOT s.quarantined;
SELECT 'H1 live_pairs' AS metric, count(*)::text AS v FROM sv
UNION ALL SELECT 'H2 version_unseen_30d', count(*)::text
  FROM sv WHERE this_version_last_seen < now()-interval '30 days'
UNION ALL SELECT 'H3 version_unseen_14d', count(*)::text
  FROM sv WHERE this_version_last_seen < now()-interval '14 days'
UNION ALL SELECT 'H4 version_superseded_7d', count(*)::text
  FROM sv WHERE this_version_last_seen < pkg_last_seen - interval '7 days'
UNION ALL SELECT 'H5 sample_older_than_60d', count(*)::text
  FROM sv WHERE created_at < now()-interval '60 days';

SELECT to_char(date_trunc('week',created_at),'YYYY-MM-DD') AS week, count(*)
  FROM samples WHERE NOT quarantined GROUP BY 1 ORDER BY 1;
```

Expected: `H1=7237 H2=1 H3=131 H4=668 H5=0`, and the weekly series that shows
the 9× growth collapse (P1-11).

**Evidence quality, against the doc's claim (P2-17):**

```sql
SELECT COALESCE(evidence_quality,'(null)') AS q, count(*), sum(observation_count)
  FROM evidence_agg WHERE result='FAIL' GROUP BY 1 ORDER BY 2 DESC;

SELECT COALESCE(evidence_quality,'(null)') AS q, count(*), sum(observation_count),
       count(*) FILTER (WHERE COALESCE(error_fp,'')='')  AS empty_fp,
       count(*) FILTER (WHERE diagnostic_candidate)      AS diag_candidates
  FROM failure_clusters GROUP BY 1 ORDER BY 2 DESC;
```

Expected `complete = 229,517` clusters and `diag_candidates = 128,590` —
against `docs/schema.md`'s "the modern cluster count is legitimately zero".

**Orphan tables (P2-16).** The SQL half:

```sql
SELECT relname, last_autovacuum, last_autoanalyze, n_live_tup, seq_scan, idx_scan
  FROM pg_stat_user_tables
 WHERE relname IN ('evidence_dedup_before','target_dedup','failure_clusters_before',
                   'evidence_agg_before','target_pairs','target_agg_ids');
```

…and the half that makes them orphans, run in the repository:

```sh
for t in evidence_dedup_before target_dedup failure_clusters_before \
         evidence_agg_before target_pairs target_agg_ids; do
  echo "--- $t ---"
  grep -rl "$t" internal/ cmd/ scripts/ deploy/ 2>/dev/null
done      # all six produce no output
```

**Stranded pipeline state:**

```sql
SELECT status, reason, count(*), min(created_at)::date, max(created_at)::date
  FROM verification_jobs GROUP BY 1,2 ORDER BY 3 DESC;

SELECT count(*) FROM authoring_drafts d
 WHERE NOT EXISTS (SELECT 1 FROM samples s WHERE s.sample_id=d.sample_id);

SELECT count(*) AS total,
       count(*) FILTER (WHERE quarantined_at IS NOT NULL) AS quarantined,
       count(*) FILTER (WHERE reopens_at > now())         AS still_closed
  FROM authoring_attempts;
```

---

## R6. Coverage, value and demand (P1-12)

Demand proxy is `evidence_agg.unique_project_buckets` — distinct anonymous real
projects observed using a package. `wanted.asks` is also queried, and its
maximum of 22 across the whole board is the reason it is not used to rank.

```sql
-- coverage.sql
\pset pager off
CREATE TEMP TABLE cover AS
  SELECT p.ecosystem, p.name,
         count(DISTINCT sp.sample_id) AS samples,
         count(DISTINCT sp.purl)      AS covered_versions
    FROM packages p
    JOIN sample_packages sp ON sp.purl=p.purl
    JOIN samples s ON s.sample_id=sp.sample_id AND NOT s.quarantined
   GROUP BY 1,2;

CREATE TEMP TABLE usage AS
  SELECT p.ecosystem, p.name,
         SUM(e.observation_count)      AS obs,
         SUM(e.unique_project_buckets) AS proj
    FROM packages p JOIN evidence_agg e ON e.purl=p.purl
   GROUP BY 1,2;

CREATE TEMP TABLE j AS
  SELECT COALESCE(c.ecosystem,u.ecosystem) AS eco,
         COALESCE(c.name,u.name)           AS nm,
         COALESCE(c.samples,0)             AS samples,
         COALESCE(u.proj,0)                AS proj,
         COALESCE(u.obs,0)                 AS obs
    FROM cover c FULL OUTER JOIN usage u
      ON u.ecosystem=c.ecosystem AND u.name=c.name;

-- global: 379673 buckets, 324615 covered, 85.5%
SELECT sum(proj) AS total_proj_buckets,
       sum(proj) FILTER (WHERE samples>0) AS covered_proj_buckets,
       round(100.0*sum(proj) FILTER (WHERE samples>0)/NULLIF(sum(proj),0),1) AS pct
  FROM j;

-- per ecosystem. COALESCE every FILTER aggregate: a NULL sum turns the
-- whole concatenated row NULL and the ecosystem silently vanishes.
SELECT eco,
       sum(samples) AS live_samples,
       count(*) FILTER (WHERE samples>0)             AS covered_pkgs,
       count(*) FILTER (WHERE proj>0)                AS observed_pkgs,
       count(*) FILTER (WHERE proj>0 AND samples=0)  AS uncovered_observed,
       sum(proj)                                     AS proj_buckets,
       COALESCE(sum(proj) FILTER (WHERE samples=0),0) AS proj_uncovered,
       round(100.0*COALESCE(sum(proj) FILTER (WHERE samples>0),0)/NULLIF(sum(proj),0),1) AS pct_covered
  FROM j GROUP BY eco ORDER BY sum(samples) DESC;

-- (2) high demand, zero coverage
SELECT eco, nm, obs, proj FROM j
 WHERE samples=0 AND proj>=8 ORDER BY proj DESC, obs DESC LIMIT 40;

-- (1) high density, low value
SELECT eco, nm, samples, proj, obs,
       round(samples::numeric/GREATEST(proj,1),3) AS samples_per_proj
  FROM j WHERE samples>=5
 ORDER BY samples::numeric/GREATEST(proj,1) DESC LIMIT 25;

-- densest packages in absolute terms (which turn out to be well targeted)
SELECT eco, nm, samples, proj, obs FROM j ORDER BY samples DESC LIMIT 15;

-- redundancy: identical contract text across releases of one package
WITH k AS (
  SELECT p.ecosystem, p.name,
         md5(COALESCE(s.manifest->'case'->'contract','[]'::jsonb)::text) AS ck,
         s.sample_id
    FROM samples s
    JOIN sample_packages sp ON sp.sample_id=s.sample_id
    JOIN packages p ON p.purl=sp.purl
   WHERE NOT s.quarantined)
SELECT ecosystem, name, count(*) AS dup_contract_groups, sum(c-1) AS redundant_samples
  FROM (SELECT ecosystem, name, ck, count(DISTINCT sample_id) AS c
          FROM k GROUP BY 1,2,3 HAVING count(DISTINCT sample_id)>1) y
 GROUP BY ecosystem, name ORDER BY sum(c-1) DESC LIMIT 20;

-- the demand signal that is too small to rank with
SELECT w.ecosystem, w.name, sum(w.asks) AS asks, count(*) AS wanted_rows,
       COALESCE(max(c.samples),0) AS live_samples
  FROM wanted w
  LEFT JOIN cover c ON c.ecosystem=w.ecosystem AND c.name=w.name
 GROUP BY w.ecosystem, w.name
HAVING COALESCE(max(c.samples),0)=0
 ORDER BY sum(w.asks) DESC LIMIT 20;
```

Read the "(2) high demand, zero coverage" output with the split from P1-12 in
mind: 25 of the top 40 are platform-specific optional binaries
(`@esbuild/*`, `@rollup/rollup-*`, `fsevents`) with no callable API.

---

## R7. Edge log analysis (P1-10)

The privacy-safe access log is a Docker volume on the host. It records
**`/v1/*` API routes only** — `deploy/caddy/Caddyfile:46` `log_skip`s web pages,
which is P1-10. Each line carries `status`, `csx_method` and `csx_route`, and
nothing else: no URI, no duration, no IP.

```sh
$SSH 'sudo ls -la /var/lib/docker/volumes/codesamplex_caddy_safe_logs/_data/ | tail -20
      sudo du -sh /var/lib/docker/volumes/codesamplex_caddy_safe_logs/_data/'

# copy out rather than gunzip on the host: it is at 80% steal (P0-3)
mkdir -p logs
$SSH 'sudo tar -C /var/lib/docker/volumes/codesamplex_caddy_safe_logs/_data -cf - \
        access-safe.log $(cd /var/lib/docker/volumes/codesamplex_caddy_safe_logs/_data \
                          && ls -1 access-safe-2026-09-1*.gz | tail -8)' > logs/caddy.tar
tar -C logs -xf logs/caddy.tar && ls -la logs/
```

```python
# logs/status_by_route.py  — run: python status_by_route.py  (from logs/)
import json, glob, gzip, collections
rows, tot, n = collections.Counter(), collections.Counter(), 0
for f in sorted(glob.glob("access-safe*")):
    op = gzip.open if f.endswith(".gz") else open
    with op(f, "rt", errors="replace") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                d = json.loads(line)
            except Exception:
                continue
            n += 1
            r, s = d.get("csx_route", "?"), d.get("status", "?")
            rows[(r, s)] += 1
            tot[r] += 1
print("total lines:", n)
for r, t in tot.most_common(40):
    fivex = sum(v for (rr, s), v in rows.items()
                if rr == r and isinstance(s, int) and 500 <= s < 600)
    other = ", ".join(f"{s}:{v}" for (rr, s), v in
                      sorted(rows.items(), key=lambda x: -x[1])
                      if rr == r and s not in (200, 503, 404))
    print(f"{r:30s} {t:8d} 200={rows[(r,200)]:8d} 503={rows[(r,503)]:6d} "
          f"5xx%={100.0*fivex/t:6.2f} 404={rows[(r,404)]:5d} {other[:60]}")
```

Observed over 429,084 lines: `authoring_work` 16.96% 5xx, `search_hit` 15.27%
(271×500), `peers` 21.24%, `registry` 50.00%, `shards` 11,421×502 and
6,828×429. Cross-check the analytics loss against the server log:

```sh
$SSH 'docker logs codesamplex-server-1 2>&1 \
      | grep -c "anonymous analytics write unavailable"'
```

---

## R8. Sitemap (P2-19)

```sh
# cold (first request in a 15-minute window) vs warm
curl -s -o /dev/null -w "cold %{http_code} ttfb=%{time_starttransfer}\n" \
  --max-time 180 "https://codesamplex.dev/sitemap.xml"
curl -s -o /dev/null -w "warm %{http_code} ttfb=%{time_starttransfer}\n" \
  --max-time 180 "https://codesamplex.dev/sitemap.xml"
curl -s --max-time 180 "https://codesamplex.dev/sitemap.xml"

for u in /sitemaps/static-1.xml /sitemaps/packages-1.xml /sitemaps/samples-1.xml; do
  curl -s -o /tmp/sm.xml -w "%{http_code} ttfb=%{time_starttransfer} size=%{size_download} $u " \
    --max-time 180 "https://codesamplex.dev$u"
  echo "urls=$(grep -c '<loc>' /tmp/sm.xml)"
done

# the divergence ledger docs/operations.md refers to
$SSH 'docker logs codesamplex-server-1 2>&1 | grep -iE "sitemap" | tail -10'
```

Observed: cold 48.84 s / warm 1.23 s; shards 15 + 3,120 + 7,070 URLs;
`sitemap rebuilt urls=10205 shards=3 packages=3120/3135 samples=7070/7070
unroutable_packages=15 malformed_sample_ids=0 sample_bound_hit=false`, and one
`sitemap rebuild failed: sitemap: samples: context canceled` right after the
14:32Z restart.

---

## Source locations cited by the findings

Read-only, in this worktree at `8e4f4fc`:

| finding | file:line |
| --- | --- |
| P0-1a pool policy and defaults | `internal/serverstore/pool.go:110-210` |
| P0-1b page-wide 503 on one failed read | `internal/web/explorer.go:1049-1052`, `1110`, `1264-1288` (21 `s.unavailable` sites across `explorer.go` 15, `dependencies.go` 2, `failureissuepage.go` 2, `gaps.go` 1, `samples.go` 1) |
| P0-1b the 503 renderer | `internal/web/web.go:1077-1084` |
| P0-1c the unbounded cluster read | `internal/serverstore/pg.go:3071-3110` |
| P0-1c the predicate | `internal/serverstore/currentclusters.go:26` |
| P0-1c Go-side ecosystem filter and 500 cap | `cmd/csx-server/webstore.go:2137-2225`, `2271` |
| P0-4 documented promotion rule | `internal/httpapi/verifications.go:363-370`, `592-650` |
| P0-4 the second promotion path | `internal/serverstore/pg.go:1856-1878` |
| P0-4 the public badge | `internal/web/explorer.go:2137-2152` |
| P0-4 the MCP suppression and its "half" claim | `internal/mcp/tools.go:987-993`, `1043-1050` |
| P1-6 the SQL function | `internal/serverstore/pg_builder_prestage.go:13-30` |
| P1-6 the expression indexes | `internal/serverstore/migrations/0036_builder_projections.sql:50-53` |
| P1-6 the reads that use them | `internal/serverstore/pg_builder_reads.go:360-365` |
| P1-7 unguarded snapshot upsert | `internal/serverstore/pg.go:748-752` |
| P1-7 guarded cluster upsert | `internal/serverstore/pg.go:2939-2990` |
| P1-8 farm aggregate timeout and JIT-off | `internal/serverstore/farmbacklog_pg.go:11-36` |
| P1-8 the 4-minute read, documented | `internal/serverstore/authoring_pg.go:19-27`, `269-292` |
| P1-8 the shared coverage CTE | `internal/serverstore/dependencyclosure_pg.go:3-16` |
| P1-10 the log_skip | `deploy/caddy/Caddyfile:31-98` |
| P2-17 the contradicted claim | `docs/schema.md`, "`failure_clusters` is derived" section |
| P2-18 the trigram index | `internal/serverstore/migrations/0034_samples_manifest_trgm_idx.sql` |
| P2-19 sitemap shards and cache | `internal/web/sitemap.go` |

Farm repository, read-only (`C:\_Project\CodeSampleX-Farm`, `864c5e5`) —
`README.md` for the author/verify separation quoted in P0-4,
`docs/recovery-2026-09-14.md` for the open author-starvation incident referenced
in P1-11, `docs/verifier-identity-measurement.md` for how a verify process is
bound to a peer key. No file in that repository was modified.

## Things that cost this audit time, recorded so they cost the next one less

- `contract_result` is uppercase `PASS`/`FAIL`/`SKIPPED`. A lowercase predicate
  returns zero matches, which reads as "every sample is unverified" — a
  spectacular false positive. Enumerate the column before filtering on it.
- PostgreSQL 17 renamed `pg_stat_statements.blk_read_time` to
  `shared_blk_read_time`; the old name aborts the statement.
- `COPY (...) TO STDOUT` corrupts JSON (its text format escapes backslashes, so
  every `\"` in a manifest comes back broken). Base64 the row instead, or use
  `json_agg(...)::text` with `\t` and unaligned output as this audit did.
- A `FILTER` aggregate that matches no rows returns NULL, and `NULL || text` is
  NULL — so a concatenated report row silently disappears rather than showing a
  zero. `COALESCE` every filtered aggregate.
- `CROSS_PASS` does **not** mean cross-environment. It means "a peer other than
  the origin filed a PASS". Testing it as environment diversity produces a
  91.7% failure rate that is not a defect at all; §P0-4 is the correct test.
- Container logs vanish on recreate. Two recreations happened inside this
  46-minute window, and the builder-pass history from before 14:31:59Z is
  unrecoverable. Capture logs early.
