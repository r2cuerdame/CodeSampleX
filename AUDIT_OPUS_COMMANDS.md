# AUDIT_OPUS_COMMANDS — exact commands and queries used

Every command below is read-only against production. No `INSERT`, `UPDATE`, `DELETE`, `ALTER`, `CREATE` or `DROP` was issued, no container was restarted, and no configuration was changed. The one write-shaped construct used is `COPY (SELECT ...) TO STDOUT`, which reads.

Conventions used throughout:

```bash
KEY=~/.ssh/lightsail-csx-r3
IP=54.116.158.230
PSQL="docker exec -i codesamplex-db-1 psql -U csx -d csx"
```

---

## 1. Orientation

```bash
cat README.md docs/architecture.md docs/schema.md docs/operations.md
cat docs/authoring-quarantine.md
cat ../CodeSampleX-Farm-audit/README.md ../CodeSampleX-Farm-audit/goal.md
git log --oneline -5
git worktree list
```

---

## 2. Host resource state

```bash
ssh -i $KEY ubuntu@$IP 'nproc; cat /proc/loadavg; free -m; vmstat 1 5; df -h /; top -bn1 | head -25'
ssh -i $KEY ubuntu@$IP 'grep -E "pswpin|pswpout" /proc/vmstat'
ssh -i $KEY ubuntu@$IP 'ps -o pid,etimes,times,pcpu,rss,comm -p <csx-server-pid>'
ssh -i $KEY ubuntu@$IP 'docker stats --no-stream'
ssh -i $KEY ubuntu@$IP 'docker inspect codesamplex-server-1 --format "RestartCount={{.RestartCount}} OOMKilled={{.State.OOMKilled}}"'
ssh -i $KEY ubuntu@$IP 'docker inspect codesamplex-server-1 --format "{{range .State.Health.Log}}{{.Start}} exit={{.ExitCode}}{{\"\n\"}}{{end}}"'
ssh -i $KEY ubuntu@$IP 'docker inspect codesamplex-server-1 --format "MemLimit={{.HostConfig.Memory}}"'
ssh -i $KEY ubuntu@$IP 'sudo dmesg -T | grep -i -E "oom|killed process"'
```

Production environment (secrets redacted before printing):

```bash
ssh -i $KEY ubuntu@$IP \
  'docker inspect codesamplex-server-1 --format "{{range .Config.Env}}{{println .}}{{end}}" \
   | sed -E "s/(PASSWORD|SECRET|TOKEN|VERIFIER|KEY|DSN)[^=]*=.*/\1=<redacted>/I"'
```

---

## 3. Lightsail CPU throttling (AWS CloudWatch, read-only)

```bash
aws lightsail get-instance-metric-data \
  --instance-name csx-prod-1 --metric-name BurstCapacityPercentage \
  --period 21600 --start-time 2026-09-08T00:00:00Z --end-time 2026-09-15T14:00:00Z \
  --unit Percent --statistics Average Maximum \
  --region ap-northeast-2 --profile r2cuerdame --output json

aws lightsail get-instance-metric-data \
  --instance-name csx-prod-1 --metric-name CPUUtilization \
  --period 21600 --start-time 2026-09-08T00:00:00Z --end-time 2026-09-15T14:00:00Z \
  --unit Percent --statistics Average Maximum \
  --region ap-northeast-2 --profile r2cuerdame --output json
```

---

## 4. PostgreSQL configuration and size

```sql
SHOW shared_buffers;  SHOW work_mem;  SHOW effective_cache_size;  SHOW max_connections;
SHOW pg_stat_statements.track;
SELECT pg_size_pretty(pg_database_size(current_database()));
SELECT extname FROM pg_extension;
```

## 5. Top queries by total execution time

```sql
SELECT stats_reset, now()-stats_reset AS window FROM pg_stat_statements_info;

SELECT round((total_exec_time/1000)::numeric,1) AS total_s,
       calls,
       round(mean_exec_time::numeric,2) AS mean_ms,
       round(max_exec_time::numeric,1)  AS max_ms,
       rows,
       round((100.0*shared_blks_read/NULLIF(shared_blks_hit+shared_blks_read,0))::numeric,1) AS miss_pct,
       substr(regexp_replace(query, '\s+', ' ', 'g'),1,120) AS q
FROM pg_stat_statements
ORDER BY total_exec_time DESC LIMIT 30;
```

## 6. Table, index and vacuum health

```sql
SELECT relname, n_live_tup, seq_scan, seq_tup_read, idx_scan,
       pg_size_pretty(pg_total_relation_size(relid)) AS total
FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 25;

SELECT relname, indexrelname, idx_scan, pg_size_pretty(pg_relation_size(indexrelid)) sz
FROM pg_stat_user_indexes WHERE idx_scan < 50
ORDER BY pg_relation_size(indexrelid) DESC LIMIT 20;

SELECT relname, indexrelname, idx_scan, idx_tup_read,
       pg_size_pretty(pg_relation_size(indexrelid)) sz
FROM pg_stat_user_indexes
WHERE indexrelname LIKE '%builder%' OR indexrelname LIKE '%coord%';

SELECT round(100.0*sum(heap_blks_hit)/NULLIF(sum(heap_blks_hit+heap_blks_read),0),2) AS heap_hit_pct
FROM pg_statio_user_tables;

SELECT relname, n_dead_tup, n_live_tup, last_autovacuum, last_autoanalyze, autovacuum_count
FROM pg_stat_user_tables
WHERE relname IN ('evidence_agg','failure_clusters','compatibility_snapshots',
                  'samples','receipts','sample_packages');

SELECT wait_event_type, wait_event, count(*) FROM pg_stat_activity
WHERE state='active' GROUP BY 1,2;

SELECT pid, now()-query_start AS dur, state, wait_event_type,
       substr(regexp_replace(query,'\s+',' ','g'),1,90) q
FROM pg_stat_activity WHERE state <> 'idle' AND pid <> pg_backend_pid()
ORDER BY query_start LIMIT 10;
```

## 7. Direct cost measurement of `builder_purl_coord` (P1-6)

Read-only: no table is touched, `generate_series` supplies the input.

```sql
-- 10,000 evaluations of the function under audit
SELECT count(builder_purl_coord('pkg:npm/%40babel/core@'||g::text))
FROM generate_series(1,10000) g;          -- measured 165,432.134 ms

-- the same shape with a trivial function, as the load-independent baseline
SELECT count(lower('pkg:npm/%40babel/core@'||g::text))
FROM generate_series(1,10000) g;          -- measured 246.373 ms
```

Run with `\timing on`. The ratio (672x) is what the finding rests on; the absolute
figures are inflated by the host throttling described in P0-2, and both halves are
inflated equally.

---

## 8. Read-only corpus dump

The base64 wrapper is the pattern `docs/operations.md` already documents for the receipt
audit: `COPY`'s text format escapes backslashes and corrupts JSON string literals inside
manifests.

```bash
cat > /tmp/dump_samples.sql <<'SQL'
COPY (
  SELECT replace(encode(convert_to(
      json_build_object(
        'sampleId', sample_id,
        'quarantined', quarantined,
        'createdAt', to_char(created_at at time zone 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
        'manifest', manifest
      )::text,'UTF8'),'base64'), chr(10),'')
  FROM samples ORDER BY created_at
) TO STDOUT;
SQL
ssh -i $KEY ubuntu@$IP "$PSQL -q" < /tmp/dump_samples.sql > samples.b64     # 8,345 rows

cat > /tmp/dump_receipts.sql <<'SQL'
COPY (
  SELECT replace(encode(convert_to(
      json_build_object('receiptId',receipt_id,'peerId',peer_id,'sampleId',sample_id,
        'envHash',env_hash,'contractResult',contract_result,
        'createdAt', to_char(created_at at time zone 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
        'receipt',receipt)::text,'UTF8'),'base64'), chr(10),'')
  FROM receipts ORDER BY created_at
) TO STDOUT;
SQL
ssh -i $KEY ubuntu@$IP "$PSQL -q" < /tmp/dump_receipts.sql > receipts.b64   # 10,965 rows

printf "COPY (SELECT sample_id||' '||purl FROM sample_packages) TO STDOUT;\n" > /tmp/dump_sp.sql
ssh -i $KEY ubuntu@$IP "$PSQL -q" < /tmp/dump_sp.sql > sample_packages.txt  # 8,676 rows
```

Decode locally:

```python
import base64, json
rows = [json.loads(base64.b64decode(l).decode()) for l in open('samples.b64') if l.strip()]
```

## 9. Quarantine reasons

```sql
SELECT quarantine_reason, count(*) FROM samples WHERE quarantined
GROUP BY 1 ORDER BY 2 DESC LIMIT 20;
```

## 10. Corpus checks run offline on the dump

Scripts live under the audit scratchpad; the checks they perform are:

- **D1 placeholder goals** — `re.match(r'^verify\s+pkg:', case.goal, re.I)` over live samples;
  equivalent SQL:
  ```sql
  SELECT count(*) FROM samples
  WHERE NOT quarantined AND manifest->'case'->>'goal' ~* '^verify\s+pkg:';
  ```
- **D2 symbol-less samples** — empty `manifest.symbols` over live samples.
- **D3 duplicates** — group live samples by `(sorted(packages), sorted(symbols))`; groups
  of size > 1 are redundant. Recency taken from `created_at` against the `2026-08-19`
  dedup pass named in the quarantine reason.
- **D4 purl canonicalization** — canonical form = lowercase percent-decoded name, `v`-prefixed
  golang version, `-`-normalised pypi name; a release with more than one raw spelling mapping
  to one canonical form is a split. Cross-checked three ways: manifest vs `sample_packages`,
  manifest vs receipt `resolvedPackages`, and manifest vs manifest.
- **D5 Go stdlib** — `pkg:golang/<name>@...` where the first path element contains no dot
  (i.e. it is not a module domain).
- **D7 evidence linkage** — set algebra over `{live sample ids}`, `{receipt sample ids}` and
  `{sample ids with a PASS receipt}`.

## 11. npm version plausibility (D6)

Abbreviated packuments only — a fraction of the full document, and read-only:

```bash
curl -s -H 'Accept: application/vnd.npm.install-v1+json' \
     https://registry.npmjs.org/<name>
```

936 packuments fetched with 12 concurrent workers and 3 retries, then each corpus
`name@version` tested for membership in `versions` and compared against `dist-tags.latest`.

## 12. Coverage priority

```sql
-- top observed coordinates with no live sample
WITH ev AS (
  SELECT purl, SUM(observation_count) AS obs
  FROM evidence_agg GROUP BY purl
), verified AS (
  SELECT DISTINCT sp.purl FROM sample_packages sp
  JOIN samples s ON s.sample_id=sp.sample_id AND NOT s.quarantined
)
SELECT ev.purl, ev.obs
FROM ev LEFT JOIN verified v ON v.purl=ev.purl
WHERE v.purl IS NULL
ORDER BY ev.obs DESC LIMIT 40;                     -- took 23.7 s on production
```

Bounded dump for offline ranking (2,898 rows returned):

```sql
COPY (
  WITH ev AS (
    SELECT purl, SUM(observation_count) AS obs,
           COUNT(DISTINCT symbol) AS symbols,
           COUNT(*) FILTER (WHERE result='FAIL') AS fails
    FROM evidence_agg GROUP BY purl
  )
  SELECT purl||' '||obs||' '||symbols||' '||fails
  FROM ev WHERE obs >= 50 ORDER BY obs DESC LIMIT 4000
) TO STDOUT;
```

Platform-shim classification regex (applied offline, to the package name only):

```
(linux|darwin|win32|windows|android|freebsd|openbsd|sunos|aix|netbsd)[-_](x64|arm64|ia32|arm|riscv64|s390x|ppc64|loong64|mips64el|universal)
| [-_](musl|gnu|msvc|gnueabihf|musleabihf|eabi)$
| ^fsevents$
```

Ask-derived demand queue, for contrast with the observation-derived ranking:

```bash
curl -s 'https://codesamplex.dev/v1/wanted?limit=25'
```

---

## 13. Server log analysis

```bash
ssh -i $KEY ubuntu@$IP 'docker logs codesamplex-server-1 --since 24h 2>&1 | grep "db pressure"' > pressure.log
grep -o "class=[a-z]*"     pressure.log | sort | uniq -c
grep -o "cause=[a-z_+]*"   pressure.log | sort | uniq -c
grep -o "path=[^ ]*"       pressure.log | sed 's/[0-9a-f]\{16,\}/HASH/g' | sort | uniq -c | sort -rn | head -20
grep -c "admin/api/farm.*query_timeout" pressure.log

ssh -i $KEY ubuntu@$IP 'docker logs codesamplex-server-1 --since 12h 2>&1 \
  | grep "builder pass complete\|builder pass start"'

ssh -i $KEY ubuntu@$IP 'docker logs codesamplex-server-1 --since 4h 2>&1 \
  | grep -c "anonymous analytics write unavailable"'

ssh -i $KEY ubuntu@$IP 'docker logs codesamplex-caddy-1 --since 4h 2>&1 | head -2'
```

---

## 14. Public HTTP probes (samples, not census)

Availability and latency, 6–12 requests per path with a 0.4–1 s gap:

```bash
for i in $(seq 1 6); do
  curl -s -o /dev/null -w "%{http_code} %{time_starttransfer}\n" --max-time 40 \
       https://codesamplex.dev/npm/axios
  sleep 0.5
done
```

Paths probed: `/`, `/compatibility`, `/gaps`, `/dependencies`, `/v1/stats`,
`/v1/samples/{id}` (12 random live ids), `/npm/axios`, `/npm/react`,
`/golang/github.com/google/uuid`, `/pypi/sqlalchemy`.

Server-vs-transfer split (shows the cost is TTFB, not payload):

```bash
curl -s -o /dev/null --compressed -w \
 "status=%{http_code} ttfb=%{time_starttransfer}s total=%{time_total}s dns=%{time_namelookup}s connect=%{time_connect}s tls=%{time_appconnect}s size=%{size_download}B\n" \
 https://codesamplex.dev/compatibility
```

Cache-fill starvation test (P0-1) — hammer one uncached package page until it succeeds,
then measure the follow-ups that should be served from the 30-minute cache:

```bash
U=https://codesamplex.dev/golang/github.com/google/uuid
for i in $(seq 1 25); do
  r=$(curl -s -o /dev/null -w "%{http_code}:%{time_starttransfer}" --max-time 45 "$U")
  echo "try$i $r"
  [ "${r%%:*}" = "200" ] && break
  sleep 1
done
```

Result: 25/25 `503`; the loop never reached the follow-up phase.

Defective-coordinate confirmation (D4, D5):

```bash
curl -s https://codesamplex.dev/v1/samples/sha256:c91f08ecdca80e07e9c6f61d1977aeb82108d10b1ccce2d574ebabde6eb606ce
curl -s -o /dev/null -w "%{http_code}\n" 'https://codesamplex.dev/v1/registry/packages/pkg:golang/net%2Fhttp@1.26.5'
```

---

## 15. Code references consulted

```bash
grep -rn "verified_samples AS MATERIALIZED" --include=*.go
grep -rn "authoringCoverageCTE" --include=*.go
grep -rn "FarmBacklogNow" --include=*.go
grep -rn "setInterval" internal/admin/static/admin.js
grep -rn "MaxConns\|ReadWait\|InteractiveConns" internal/serverstore/pool.go
grep -rn "hotPackagesTTL\|packageLoadSlotCount\|snapshotLoadRetryDefer" cmd/csx-server/webstore.go
grep -rn "SnapshotInterval" --include=*.go cmd/ internal/serverstore/config.go
sed -n '1,80p' internal/serverstore/pg_builder_prestage.go
cat cmd/csx-server/dbclass.go
cat deploy/docker-compose.yml
```

---

## What was deliberately not run

- No `EXPLAIN ANALYZE` on the multi-minute production queries. `EXPLAIN ANALYZE` executes
  the statement; on a host at 0.4 effective vCPU that would have added minutes of load to a
  box already refusing user requests. The `pg_stat_statements` figures answer the same
  question without the cost.
- No `pg_stat_statements_reset()`. It would have destroyed the six-day window this audit
  reads, for everyone.
- No `VACUUM`, `ANALYZE` or `REINDEX`, despite the dead-tuple counts — all three are writes.
- No admin-dashboard authentication. The admin failure (P1-5) is established from the server
  log and `pg_stat_statements`, so the credential was never needed and was never revealed.
- No load test. The site is already failing under organic traffic; adding synthetic load
  would have degraded a live service to measure something the logs already show.
