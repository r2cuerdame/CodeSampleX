\echo ===NONPASS_RECEIPTS_STAGEFAILURES
select contract_result, (receipt ? 'stageFailures') has_sf, receipt->>'schemaVersion' sv, count(*) from receipts where contract_result<>'PASS' group by 1,2,3 order by 4 desc;
\echo ===LIVE_LATEST_NONPASS_WITHOUT_SF
with lr as (select distinct on (sample_id) sample_id, contract_result, receipt from receipts order by sample_id, created_at desc)
select lr.contract_result, (lr.receipt ? 'stageFailures') has_sf, count(*) from samples s join lr on lr.sample_id=s.sample_id where not s.quarantined and lr.contract_result<>'PASS' group by 1,2;
\echo ===RECEIPT_SEQ
select seq, count(*) from (select r.sample_id, string_agg(r.contract_result, '>' order by r.created_at) seq from receipts r join samples s on s.sample_id=r.sample_id where not s.quarantined group by 1) d group by 1 order by 2 desc limit 12;
\echo ===WANTED_COVERED_NORMALIZED
with cov as (select split_part(sp.purl,'@',1) pkg, count(distinct sp.sample_id) n from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1),
w as (select ecosystem, name, sum(asks) asks from wanted group by 1,2),
j as (select w.*, cov.n from w left join cov on cov.pkg = 'pkg:'||w.ecosystem||'/'||replace(w.name,'@','%40') or cov.pkg='pkg:'||w.ecosystem||'/'||w.name)
select count(*) wanted_pkgs, count(*) filter (where n is null) wanted_pkgs_no_sample, sum(asks) asks_total, sum(asks) filter (where n is null) asks_no_sample from j;
\echo ===TOP_WANTED_NO_SAMPLE_NORMALIZED
with cov as (select split_part(sp.purl,'@',1) pkg, count(distinct sp.sample_id) n from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1),
w as (select ecosystem, name, sum(asks) asks, count(*) tuples, max(last_seen)::date last_seen from wanted group by 1,2),
j as (select w.*, cov.n from w left join cov on cov.pkg = 'pkg:'||w.ecosystem||'/'||replace(w.name,'@','%40') or cov.pkg='pkg:'||w.ecosystem||'/'||w.name)
select ecosystem, name, asks, tuples, last_seen from j where n is null order by asks desc limit 40;
\echo ===WANTED_VERSION_COVERED_NORMALIZED
with covv as (select distinct sp.purl from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined)
select count(*) tuples_with_version, sum(asks) asks, count(*) filter (where exists (select 1 from covv where covv.purl='pkg:'||w.ecosystem||'/'||replace(w.name,'@','%40')||'@'||w.version or covv.purl='pkg:'||w.ecosystem||'/'||w.name||'@'||w.version)) exact_version_covered, sum(asks) filter (where exists (select 1 from covv where covv.purl='pkg:'||w.ecosystem||'/'||replace(w.name,'@','%40')||'@'||w.version or covv.purl='pkg:'||w.ecosystem||'/'||w.name||'@'||w.version)) asks_exact_covered from wanted w where w.version<>'';
\echo ===WANTED_SYMBOL_COVERED
with covs as (select distinct split_part(sp.purl,'@',1) pkg, y.sym from sample_packages sp join samples s on s.sample_id=sp.sample_id cross join lateral jsonb_array_elements_text(case when jsonb_typeof(s.manifest->'symbols')='array' then s.manifest->'symbols' else '[]'::jsonb end) y(sym) where not s.quarantined)
select count(*) symbol_tuples, sum(asks) asks, count(*) filter (where exists (select 1 from covs where (covs.pkg='pkg:'||w.ecosystem||'/'||replace(w.name,'@','%40') or covs.pkg='pkg:'||w.ecosystem||'/'||w.name) and (covs.sym=w.symbol or covs.sym like '%'||w.symbol))) symbol_covered from wanted w where w.symbol<>'';
\echo ===STALENESS_VS_OBSERVED
with sv as (select distinct sp.purl, split_part(sp.purl,'@',1) pkg, regexp_replace(split_part(sp.purl,'@',2),'^v','') ver from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined),
obs as (select split_part(purl,'@',1) pkg, regexp_replace(split_part(purl,'@',2),'^v','') ver, sum(observation_count) obs from evidence_agg where regexp_replace(split_part(purl,'@',2),'^v','') ~ '^[0-9]+\.[0-9]+(\.[0-9]+)?$' group by 1,2),
newest as (select pkg, (array_agg(ver order by string_to_array(ver,'.')::int[] desc))[1] newest_ver from obs group by 1)
select count(*) sample_purls, count(*) filter (where newest.pkg is not null) with_observed_versions, count(*) filter (where newest.pkg is not null and sv.ver <> newest.newest_ver) not_newest_observed, count(*) filter (where newest.pkg is not null and split_part(sv.ver,'.',1) <> split_part(newest.newest_ver,'.',1)) older_major_than_newest_observed, count(*) filter (where newest.pkg is not null and split_part(sv.ver,'.',1)::int < split_part(newest.newest_ver,'.',1)::int) strictly_older_major from sv left join newest on newest.pkg=sv.pkg where sv.ver ~ '^[0-9]+\.[0-9]+(\.[0-9]+)?$';
\echo ===STALE_MAJOR_EXAMPLES
with sv as (select sp.purl, split_part(sp.purl,'@',1) pkg, regexp_replace(split_part(sp.purl,'@',2),'^v','') ver, count(distinct sp.sample_id) samples from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1,2,3),
obs as (select split_part(purl,'@',1) pkg, regexp_replace(split_part(purl,'@',2),'^v','') ver, sum(observation_count) obs from evidence_agg where regexp_replace(split_part(purl,'@',2),'^v','') ~ '^[0-9]+\.[0-9]+(\.[0-9]+)?$' group by 1,2),
newest as (select pkg, (array_agg(ver order by string_to_array(ver,'.')::int[] desc))[1] newest_ver from obs group by 1)
select sv.purl, newest.newest_ver, sv.samples from sv join newest on newest.pkg=sv.pkg where sv.ver ~ '^[0-9]+\.[0-9]+(\.[0-9]+)?$' and split_part(sv.ver,'.',1)::int < split_part(newest.newest_ver,'.',1)::int order by sv.samples desc limit 25;
\echo ===SAMPLE_VERSION_OBSERVED_AT_ALL
with sv as (select distinct sp.purl from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined)
select count(*) sample_purls, count(*) filter (where exists (select 1 from evidence_agg e where e.purl=sv.purl)) observed_exact_version, count(*) filter (where exists (select 1 from packages p where p.purl=sv.purl)) known_in_packages from sv;
\echo ===TABLE_BLOAT
select relname, n_live_tup, n_dead_tup, pg_size_pretty(pg_relation_size(relid)) heap, pg_size_pretty(pg_total_relation_size(relid)-pg_relation_size(relid)) idx_toast, round(pg_relation_size(relid)::numeric/nullif(n_live_tup,0)) bytes_per_row, n_tup_upd, n_tup_hot_upd, n_tup_ins from pg_stat_user_tables where relname in ('failure_clusters','compatibility_snapshots','evidence_agg','samples','receipts','shards','wanted','stats_daily','dependency_edge','evidence_dedup') order by pg_relation_size(relid) desc;
\echo ===FC_ROWS
select count(*) total, count(*) filter (where error_fp='' ) gap_rows, count(*) filter (where evidence_quality in ('missing','legacy-evidence-incomplete')) legacy, count(distinct package_name) pkgs from failure_clusters;
\echo ===FC_CURRENT_VS_PRESERVED
select count(*) filter (where coalesce(evidence_quality,'') not in ('missing','legacy-evidence-incomplete') or coalesce(error_fp,'')='') current_rows, count(*) total from failure_clusters;
\echo ===SNAPSHOTS
select count(*) snapshots, count(distinct purl) purls, pg_size_pretty(avg(length(snapshot::text))::bigint) avg_len, pg_size_pretty(max(length(snapshot::text))::bigint) max_len, count(*) filter (where symbol='') pkg_level from compatibility_snapshots;
\echo ===SHARDS
select count(*), pg_size_pretty(avg(length(json))::bigint) avg_len, pg_size_pretty(max(length(json))::bigint) max_len from shards;
\echo ===MODERN_EVIDENCE
select evidence_quality, count(*), sum(observation_count) from evidence_agg where result='FAIL' group by 1;
