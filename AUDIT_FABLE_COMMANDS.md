# AUDIT_FABLE_COMMANDS — exact, safe, reproducible

Every production read below runs inside `BEGIN; SET TRANSACTION READ ONLY; SET LOCAL statement_timeout=…`
through `scripts/audit-fable/prod-rosql.sh`, which pipes stdin SQL into `psql` inside the `codesamplex-db-1`
container over the pinned SSH host key. Nothing writes. Query text from `pg_stat_statements` is only ever
read with string literals masked (`regexp_replace(query, '''[^'']*''', '''?''', 'g')`) and truncated to 160
characters; no raw `query` column was exported off the host. The Farm node (`43.200.78.1`) was not reachable
from the auditing machine (SSH allowlist), so no farm-side command ran.

Prerequisites: `~/.ssh/lightsail-csx-r3` and the production host key in `~/.ssh/known_hosts`
(`docs/operations.md`), `python3`, `curl`, Git Bash.

```bash
# helper: read-only SQL runner (stdin → psql, READ ONLY tx, per-statement savepoints)
scripts/audit-fable/prod-rosql.sh [timeout_seconds] < query.sql
```

## A. Host, containers, logs

```bash
ssh -i ~/.ssh/lightsail-csx-r3 -o StrictHostKeyChecking=yes -o BatchMode=yes ubuntu@54.116.158.230 '
nproc; free -m | head -2; cat /proc/loadavg
docker ps --format "{{.Names}} {{.Status}}"
docker stats --no-stream --format "{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}"
for c in codesamplex-server-1 codesamplex-db-1 codesamplex-caddy-1; do docker inspect $c --format "{{.Name}} oom={{.State.OOMKilled}} restarts={{.RestartCount}} started={{.State.StartedAt}} mem={{.HostConfig.Memory}}"; done
# CSX_* env NAMES only; values of *SHA256/*SECRET/*PASSWORD/DSN/HASH_KEY are never printed
docker inspect codesamplex-server-1 --format "{{range .Config.Env}}{{println .}}{{end}}" | grep -E "^CSX_(DB_|SNAPSHOT_)" 
# server log families, pressure lines, builder timeline (container uptime window)
L=$(mktemp); docker logs --since 72h codesamplex-server-1 > $L 2>&1
grep "db pressure" $L | sed -E "s/.*class=([a-z]+) cause=([a-z_]+).*/\1 \2/" | sort | uniq -c | sort -rn
grep "db pressure" $L | sed -E "s/.*path=([^ ]+) .*/\1/" | sed -E "s#^/(v1/shards|npm|golang|pypi|cargo|gem|composer|hex|pub|maven)/.*#/\1/*#" | sort | uniq -c | sort -rn | head -30
grep "db pressure" $L | sed -E "s/.*class=([a-z]+).* waited=([0-9.]+)(ms|s) .*/\1 \2 \3/" | awk "{v=\$2; if(\$3==\"s\")v=v*1000; if(v>m[\$1])m[\$1]=v} END{for(k in m)print k, m[k], \"ms\"}"
grep -E "builder pass (start|complete)|builder run failed|ceiling" $L | sed -E "s/ targets=.*total=/ total=/"
grep -E "event=(enter|exit) phase" $L | tail -40
rm -f $L'
```

Privacy-safe Caddy log (route label + status only; no URI, no IP, no duration):

```bash
ssh … 'sudo nice -n 19 python3 - <<PY
import glob,gzip,json,collections,time
D="/var/lib/docker/volumes/codesamplex_caddy_safe_logs/_data/"
files=sorted(glob.glob(D+"access-safe-*.log.gz"))+[D+"access-safe.log"]
days=collections.Counter(); route=collections.Counter(); r5=collections.defaultdict(collections.Counter)
for f in files:
    op=gzip.open if f.endswith(".gz") else open
    with op(f,"rt",encoding="utf-8",errors="replace") as fh:
        for line in fh:
            try: d=json.loads(line)
            except Exception: continue
            day=time.strftime("%Y-%m-%d",time.gmtime(float(d.get("ts",0)))); r=d.get("csx_route","(web)"); s=int(d.get("status",0))
            days[day]+=1; route[r]+=1
            if s>=500: r5[r][s]+=1
print(sorted(days.items())); [print(k,v,dict(r5[k])) for k,v in route.most_common()]
PY'
```

## B. Corpus scans (all exhaustive over live samples unless marked heuristic)

Run each block with `scripts/audit-fable/prod-rosql.sh 300 <<'EOF' … EOF`.

### B0 population
```sql
select status, quarantined, count(*) from samples group by 1,2 order by 1,2;
select quarantine_reason, count(*) from samples where quarantined group by 1 order by 2 desc;
select contract_result, count(*) from receipts group by 1;
select manifest->'environment'->>'ecosystem' eco, status, count(*) from samples where not quarantined group by 1,2 order by 1,2;
select date_trunc('week', created_at)::date wk, count(*) from samples group by 1 order by 1;
```

### B1 duplicates / description
```sql
-- template goal share
select count(*) total, count(*) filter (where manifest->'case'->>'goal' ~ '^verify .* in pkg:') template_goal from samples where not quarantined;
-- identical packages+symbols
select count(*) dup_groups, sum(n) dup_samples from (select manifest->'packages' p, manifest->'symbols' s, count(*) n from samples where not quarantined group by 1,2 having count(*)>1) t;
-- identical contract (any subject / same subject)
select count(*) groups, sum(n) from (select manifest->'case'->'contract' c, count(*) n from samples where not quarantined group by 1 having count(*)>1) t;
select count(*) groups, sum(n) from (select manifest->'case'->'contract' c, manifest->>'subject' s, count(*) n from samples where not quarantined group by 1,2 having count(*)>1) t;
-- shared case_id
select count(*), sum(n) from (select case_id, count(*) n from samples where not quarantined group by 1 having count(*)>1) t;
-- symbols / subject / multi-package
select count(*) filter (where manifest->'symbols' is null or jsonb_array_length(manifest->'symbols')=0) no_symbols, count(*) filter (where manifest->>'subject' is null) no_subject, count(*) filter (where jsonb_array_length(manifest->'packages')>1) multi_pkg from samples where not quarantined;
-- contract line shape
select jsonb_array_length(manifest->'case'->'contract') n, count(*) from samples where not quarantined group by 1 order by 1;
select line, count(*) from (select jsonb_array_elements_text(manifest->'case'->'contract') line from samples where not quarantined) t group by 1 having count(*)>=8 order by 2 desc limit 25;
select count(*) filter (where length(line)<30) short_lines, count(*) total_lines, count(distinct line) distinct_lines from (select jsonb_array_elements_text(manifest->'case'->'contract') line from samples where not quarantined) t;
-- metadata-only contracts (regex heuristic)
with c as (select sample_id, manifest->'environment'->>'ecosystem' eco, manifest->'case'->'contract' contract from samples where not quarantined)
select eco, count(*) filter (where not exists (select 1 from jsonb_array_elements_text(contract) l where l !~* '(package\.json|manifest|\.d\.ts|type declarations|types entry|exports (map|field|mapping)|binary header|ELF|prebuild|Mach-O|PE header|\.node|version (is|as|field)|package name)')) metadata_only, count(*) total from c group by 1;
-- contract mentions none of its symbols (heuristic)
with s as (select sample_id, manifest from samples where not quarantined and jsonb_typeof(manifest->'symbols')='array' and jsonb_array_length(manifest->'symbols')>0)
select count(*) filter (where not exists (select 1 from jsonb_array_elements_text(manifest->'symbols') sym where (manifest->'case'->'contract')::text ilike '%' || regexp_replace(sym, '^.*[./]', '') || '%')) contract_mentions_no_symbol, count(*) total from s;
```

### B2 verification linkage
```sql
select count(*) receipts,
 count(*) filter (where r.receipt->>'sampleId' <> r.sample_id) sampleid_mismatch,
 count(*) filter (where r.receipt->>'caseId' is distinct from (s.manifest->'case'->>'caseId')) caseid_mismatch,
 count(*) filter (where (r.receipt->'environment'->>'ecosystem') is distinct from (s.manifest->'environment'->>'ecosystem')) eco_mismatch,
 count(*) filter (where r.contract_result='PASS' and (r.receipt->'stages'->>'contract') <> 'PASS') pass_but_stage_not_pass,
 count(*) filter (where r.contract_result='PASS' and (r.receipt->'verifierImage') is null) pass_no_image,
 count(*) filter (where (r.receipt->>'schemaVersion') <> '2') schema_not_2,
 count(*) filter (where r.contract_result='PASS' and not ((coalesce(r.receipt->'resolvedPackages','[]'::jsonb)) @> (s.manifest->'packages'))) pass_resolved_missing_manifest_pkg
from receipts r join samples s on s.sample_id=r.sample_id;
-- CROSS_PASS peers
select count(*) filter (where n_peers<2) single_peer, count(*) filter (where n_peers>=2) ok from (select s.sample_id, count(distinct r.peer_id) filter (where r.contract_result='PASS') n_peers from samples s left join receipts r on r.sample_id=s.sample_id where not s.quarantined and s.status='CROSS_PASS' group by 1) t;
select peer_id, count(*) from receipts where contract_result='PASS' group by 1 order by 2 desc;
select d.local_status, count(*) from (select s.sample_id from samples s where not s.quarantined and s.status='CROSS_PASS' and (select count(distinct r.peer_id) from receipts r where r.sample_id=s.sample_id and r.contract_result='PASS')<2) t left join authoring_drafts d on d.sample_id=t.sample_id group by 1;
-- image-less verification by ecosystem; newest receipt FAIL
select (s.manifest->'environment'->>'ecosystem') eco, s.status, count(*) from samples s where not s.quarantined and not exists (select 1 from receipts r where r.sample_id=s.sample_id and r.contract_result='PASS' and r.receipt->'verifierImage' is not null) group by 1,2 order by 3 desc;
select (s.manifest->'environment'->>'ecosystem') eco, s.status, count(*) from samples s join lateral (select contract_result from receipts r where r.sample_id=s.sample_id order by created_at desc limit 1) lr on true where not s.quarantined and lr.contract_result='FAIL' group by 1,2 order by 3 desc;
select (r.receipt->'verifierImage'->>'reference') ref, count(*) from receipts r where contract_result='PASS' group by 1 order by 2 desc limit 25;
```

### B3 coordinate spelling
```sql
select count(distinct s.sample_id) samples, count(*) purls from samples s cross join jsonb_array_elements_text(s.manifest->'packages') p where not s.quarantined and p like 'pkg:npm/@%';
select count(*) from sample_packages where purl like 'pkg:npm/@%';
select count(distinct s.sample_id) from samples s cross join jsonb_array_elements_text(s.manifest->'packages') p where not s.quarantined and p like 'pkg:golang/%' and p !~ '@v[0-9]';
select count(*) filter (where sym ~ '/') qualified, count(*) filter (where sym !~ '/') bare from (select jsonb_array_elements_text(manifest->'symbols') sym from samples where not quarantined and manifest->'environment'->>'ecosystem'='golang') t;
select count(*) from samples s where not s.quarantined and exists (select 1 from jsonb_array_elements_text(s.manifest->'packages') p where p ~ '^pkg:golang/[^./@]+(/|@)');
```

### B4 density, demand, usage
```sql
with sp as (select s.sample_id, split_part(p,'@',1) coord, p purl, s.manifest->'symbols' syms from samples s cross join jsonb_array_elements_text(s.manifest->'packages') p where not s.quarantined),
 h as (select sample_id, count(*) n from search_hits group by 1)
select coord, count(distinct sp.sample_id) samples, count(distinct purl) versions, count(distinct syms) distinct_symbol_sets, coalesce(sum(h.n),0) hits from sp left join h on h.sample_id=sp.sample_id group by 1 order by samples desc limit 30;
select n_hits_bucket, count(*) from (select s.sample_id, case when h.n is null then '0' when h.n<=2 then '1-2' when h.n<=10 then '3-10' else '>10' end n_hits_bucket from samples s left join (select sample_id, count(*) n from search_hits group by 1) h on h.sample_id=s.sample_id where not s.quarantined) t group by 1 order by 1;
select count(*) hits, count(distinct sample_id), count(distinct anon_id), min(created_at)::date, max(created_at)::date from search_hits;
select count(*) misses, count(distinct anon_id) from search_misses;
select (select count(*) from search_hits h join samples s on s.sample_id=h.sample_id where s.quarantined) hits_on_quarantined, (select count(*) from search_hits h where h.sample_id is not null and not exists (select 1 from samples s where s.sample_id=h.sample_id)) hits_unknown_sample;
with live as (select distinct split_part(p,'@',1) coord from (select jsonb_array_elements_text(manifest->'packages') p from samples where not quarantined) t)
select count(*) wanted_rows, sum(asks) asks, count(*) filter (where exists (select 1 from live l where l.coord='pkg:'||w.ecosystem||'/'||replace(w.name,'@','%40') or l.coord='pkg:'||w.ecosystem||'/'||w.name)) rows_with_any_live_sample from wanted w;
with live as (select distinct split_part(p,'@',1) coord from (select jsonb_array_elements_text(manifest->'packages') p from samples where not quarantined) t)
select split_part(e.purl,'@',1) coord, sum(observation_count) obs, count(distinct e.purl) versions from evidence_agg e where e.purl not like 'pkg:generic/%' and not exists (select 1 from live l where l.coord=split_part(e.purl,'@',1)) group by 1 order by obs desc limit 80;
```

### B5 JSON export (feeds `build_audit_data.py`)
The exact export statements (one `json_build_object` per class) are the ones in
`scripts/audit-fable/build_audit_data.py`'s docstring inputs; they were run with `\a \t` into `defects.jsonl`.
The class names in `AUDIT_FABLE_DATA.json → defects` map 1:1 to the predicates above.

### B6 registry version currency (outbound GETs of public package names only)
```bash
scripts/audit-fable/prod-rosql.sh 120 <<'EOF' > live_purls.tsv
\a \t \f '\t'
select p, count(*) from (select jsonb_array_elements_text(manifest->'packages') p from samples where not quarantined) t group by 1 order by 1;
EOF
python3 scripts/audit-fable/version_currency_check.py live_purls.tsv version_check.json
python3 scripts/audit-fable/build_audit_data.py defects.jsonl version_check.json AUDIT_FABLE_DATA.json
```

## C. Performance probes

### C1 PostgreSQL statistics (read-only)
```sql
select name, setting from pg_settings where name in ('shared_buffers','work_mem','effective_cache_size','max_connections','jit','statement_timeout','log_min_duration_statement');
select relname, pg_size_pretty(pg_total_relation_size(relid)) total, n_live_tup, n_dead_tup, seq_scan, seq_tup_read, idx_scan from pg_stat_user_tables order by pg_total_relation_size(relid) desc limit 25;
select indexrelname, relname, pg_size_pretty(pg_relation_size(indexrelid)) sz, idx_scan from pg_stat_user_indexes order by pg_relation_size(indexrelid) desc limit 40;
select round(100.0*sum(blks_hit)/nullif(sum(blks_hit)+sum(blks_read),0),2) cache_hit_pct, sum(temp_files), pg_size_pretty(sum(temp_bytes)), sum(deadlocks), xact_commit, xact_rollback from pg_stat_database where datname='csx' group by xact_commit, xact_rollback;
select round((sum(total_exec_time)/1000/3600)::numeric,1) total_hours, sum(calls), (select stats_reset from pg_stat_statements_info) from pg_stat_statements where dbid=(select oid from pg_database where datname='csx');
select queryid, calls, round((total_exec_time/1000)::numeric) tot_s, round(mean_exec_time::numeric) mean_ms, round(max_exec_time::numeric) max_ms, rows, shared_blks_read, temp_blks_written, round(shared_blk_read_time::numeric) rd_ms,
       left(regexp_replace(regexp_replace(query, '''[^'']*''', '''?''', 'g'),'\s+',' ','g'), 160) q
from pg_stat_statements where dbid=(select oid from pg_database where datname='csx') order by total_exec_time desc limit 30;
-- same with ORDER BY calls DESC, mean_exec_time DESC (calls>=20), temp_blks_written DESC
```

### C2 EXPLAIN probes (read-only; ANALYZE executes the SELECT once)
```sql
select proname, prolang::regtype, provolatile from pg_proc where proname='builder_purl_coord';
explain (analyze, buffers, timing off) select builder_purl_coord(purl) from evidence_agg limit 1000;
explain (analyze, buffers, timing off) select lower(split_part(purl,'@',1))||'@' from evidence_agg limit 1000;
explain (analyze, buffers, timing off) SELECT id, env_summary::text, hypotheses::text, versions::text, env_variants::text, evidence_breakdown::text, outer_commands::text FROM failure_clusters WHERE package_name='golang.org/x/sys' AND ((error_fp = '' AND evidence_quality IN ('missing','legacy-evidence-incomplete')) OR (error_fp <> '' AND termination_kind <> '' AND error_summary <> '')) ORDER BY observation_count DESC, id;
explain (analyze, buffers, timing off) SELECT purl, symbol FROM compatibility_snapshots ORDER BY purl, symbol;
explain (analyze, buffers, timing off) SELECT sample_id, count(*) OVER() FROM samples WHERE NOT quarantined ORDER BY created_at DESC, sample_id LIMIT 24 OFFSET 0;
```

### C3 payload sizes
```sql
select count(*), pg_size_pretty(sum(pg_column_size(snapshot))::bigint), pg_size_pretty(avg(pg_column_size(snapshot))::bigint), pg_size_pretty(max(pg_column_size(snapshot))::bigint) from compatibility_snapshots;
select count(*), pg_size_pretty(sum(pg_column_size(json))::bigint), pg_size_pretty(max(pg_column_size(json))::bigint) from shards;
select key, pg_size_pretty(pg_column_size(json)::bigint) from shards order by pg_column_size(json) desc limit 8;
select count(*) filter (where error_fp='') evidence_gap_rows, count(*) filter (where error_fp<>'') fingerprinted_rows, sum(observation_count) from failure_clusters;
select package_name, count(*) from failure_clusters group by 1 order by 2 desc limit 8;
```

### C4 public page timing (GET only, from the auditing client)
```bash
scripts/audit-fable/probe-public-pages.sh          # 21 URLs × 2 rounds: ttfb, total, bytes, status
for i in 1 2 3; do for u in https://codesamplex.dev/npm/hono https://codesamplex.dev/golang/github.com%2Fjackc%2Fpgx%2Fv5 https://codesamplex.dev/pypi/sqlalchemy https://codesamplex.dev/npm/react/18.3.1; do curl -s -o /dev/null -w "%{http_code} ttfb=%{time_starttransfer}s\n" --max-time 70 "$u"; done; sleep 8; done
curl -s -D - -o /dev/null -H "Accept-Encoding: gzip, br, zstd" https://codesamplex.dev/static/site.css | grep -iE "content-encoding|cache-control|etag"
```

### C5 code map used for attribution
- Route → store fan-out: `cmd/csx-server/mux.go`, `cmd/csx-server/webstore.go` (cache TTLs at :380-385,
  :432-437, :650, :2041, :2274), `cmd/csx-server/dbclass.go` (class per path), `internal/serverstore/pool.go`
  (defaults :144-169).
- Hot queries: `internal/serverstore/pg.go:700` (`GetSnapshotsForPURL`), `:725` (`ListSnapshots`), `:795`
  (`SnapshotKeys`), `:838` (`SnapshotUpdatedAt`), `:876-949` (`ListSnapshotTargets`), `:2705-2790`
  (`HotShardKeys`), `:2940` (`upsertFailureClusterSQL`), `:3093` (`listFailureClusters`), `:3451` (stats
  refresh); `internal/serverstore/pg_builder_prestage.go:13-33` (`builder_purl_coord`);
  `internal/serverstore/dependencyclosure_pg.go:16` (`authoringCoverageCTE`) with callers
  `authoring_pg.go:298`, `farmbacklog_pg.go:42`; `internal/admin/farm_http.go:107`; `internal/admin/static/admin.js:569,658`.
- Status rules: `internal/httpapi/verifications.go:596-651`; draft promotion `internal/serverstore/pg.go:1865-1877`.
- Builder cadence: `internal/compatibility/builder.go:56` (`fullPassEvery = 12`), `:391` (full-pass predicate).
