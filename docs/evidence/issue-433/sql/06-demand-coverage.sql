\echo ===PUBLICNESS
select publicness, count(*) from packages group by 1;
\echo ===FAIL_LATEST_ENV_VS_PASS
with lr as (select distinct on (sample_id) sample_id, contract_result, created_at, receipt from receipts order by sample_id, created_at desc),
lp as (select distinct on (sample_id) sample_id, created_at, receipt from receipts where contract_result='PASS' order by sample_id, created_at desc)
select lr.contract_result, lp.receipt->'environment'->>'runtimeVersion' pass_rt, lr.receipt->'environment'->>'runtimeVersion' fail_rt, lp.receipt->'verifierImage'->>'reference' pass_img, lr.receipt->'verifierImage'->>'reference' fail_img, count(*) from samples s join lr on lr.sample_id=s.sample_id join lp on lp.sample_id=s.sample_id where not s.quarantined and lr.contract_result<>'PASS' group by 1,2,3,4,5 order by 6 desc;
\echo ===FAIL_LATEST_JOB_REASON
with lr as (select distinct on (sample_id) sample_id, contract_result, created_at from receipts order by sample_id, created_at desc)
select lr.contract_result, vj.reason, vj.status, count(*) from samples s join lr on lr.sample_id=s.sample_id left join lateral (select reason, status from verification_jobs v where v.sample_id=s.sample_id order by created_at desc limit 1) vj on true where not s.quarantined and lr.contract_result<>'PASS' group by 1,2,3 order by 4 desc;
\echo ===FAIL_LATEST_WANT_ENV
with lr as (select distinct on (sample_id) sample_id, contract_result, created_at from receipts order by sample_id, created_at desc)
select lr.contract_result, left(vj.want_env::text,120), count(*) from samples s join lr on lr.sample_id=s.sample_id left join lateral (select want_env from verification_jobs v where v.sample_id=s.sample_id order by created_at desc limit 1) vj on true where not s.quarantined and lr.contract_result<>'PASS' group by 1,2 order by 3 desc limit 10;
\echo ===RECEIPTS_PER_SAMPLE_RESULT_SEQ
select seq, count(*) from (select sample_id, string_agg(contract_result, '>' order by created_at) seq from receipts r join samples s using (sample_id) where not s.quarantined group by 1) d group by 1 order by 2 desc limit 15;
\echo ===DEMAND_VS_COVERAGE_TOP_WANTED_NO_SAMPLE
with cov as (select split_part(sp.purl,'@',1) pkg, count(distinct sp.sample_id) n from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1)
select w.ecosystem, w.name, sum(w.asks) asks, count(*) tuples, max(w.last_seen)::date last_seen, coalesce(cov.n,0) samples from wanted w left join cov on cov.pkg = 'pkg:'||w.ecosystem||'/'||w.name group by 1,2,cov.n order by asks desc limit 40;
\echo ===WANTED_TOTALS
select count(*) tuples, sum(asks) asks, count(distinct (ecosystem,name)) packages, count(*) filter (where last_seen > now() - interval '30 days') tuples_30d, sum(asks) filter (where last_seen > now() - interval '30 days') asks_30d from wanted;
\echo ===WANTED_COVERED_SHARE
with cov as (select split_part(sp.purl,'@',1) pkg, count(distinct sp.sample_id) n from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1),
w as (select ecosystem, name, sum(asks) asks from wanted group by 1,2)
select count(*) wanted_pkgs, count(*) filter (where cov.n is null) wanted_pkgs_no_sample, sum(asks) asks_total, sum(asks) filter (where cov.n is null) asks_no_sample from w left join cov on cov.pkg='pkg:'||w.ecosystem||'/'||w.name;
\echo ===WANTED_VERSION_COVERED
with covv as (select distinct sp.purl from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined)
select count(*) tuples_with_version, count(*) filter (where exists (select 1 from covv where covv.purl='pkg:'||w.ecosystem||'/'||w.name||'@'||w.version)) exact_version_covered, count(*) filter (where exists (select 1 from covv where split_part(covv.purl,'@',1)='pkg:'||w.ecosystem||'/'||w.name)) package_covered from wanted w where w.version<>'';
\echo ===EVIDENCE_PER_PKG_VS_SAMPLES
with ev as (select split_part(purl,'@',1) pkg, sum(observation_count) obs, count(distinct purl) versions, count(distinct symbol) filter (where symbol<>'') symbols from evidence_agg group by 1),
cov as (select split_part(sp.purl,'@',1) pkg, count(distinct sp.sample_id) n from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1)
select count(*) filter (where ev.pkg is not null) pkgs_with_evidence, count(*) filter (where ev.pkg is not null and cov.n is null) evidence_pkgs_no_sample, count(*) filter (where cov.n is not null and ev.pkg is null) sample_pkgs_no_evidence, count(*) filter (where cov.n is not null) sample_pkgs, sum(cov.n) filter (where ev.pkg is null) samples_in_pkgs_no_evidence, sum(ev.obs) filter (where cov.n is null) obs_in_pkgs_no_sample, sum(ev.obs) obs_total from ev full outer join cov on cov.pkg=ev.pkg;
\echo ===TOP_EVIDENCE_PKGS_NO_SAMPLE
with ev as (select split_part(purl,'@',1) pkg, sum(observation_count) obs, count(distinct purl) versions from evidence_agg group by 1),
cov as (select split_part(sp.purl,'@',1) pkg, count(distinct sp.sample_id) n from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1)
select ev.pkg, ev.obs, ev.versions from ev left join cov on cov.pkg=ev.pkg where cov.n is null order by ev.obs desc limit 40;
\echo ===TOP_SAMPLE_PKGS_NO_EVIDENCE_NO_WANTED
with ev as (select split_part(purl,'@',1) pkg, sum(observation_count) obs from evidence_agg group by 1),
cov as (select split_part(sp.purl,'@',1) pkg, count(distinct sp.sample_id) n from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1),
w as (select 'pkg:'||ecosystem||'/'||name pkg, sum(asks) asks from wanted group by 1),
hits as (select split_part(sp.purl,'@',1) pkg, count(*) hits from search_hits h join sample_packages sp on sp.sample_id=h.sample_id group by 1)
select cov.pkg, cov.n samples, coalesce(ev.obs,0) obs, coalesce(w.asks,0) asks, coalesce(hits.hits,0) hits from cov left join ev on ev.pkg=cov.pkg left join w on w.pkg=cov.pkg left join hits on hits.pkg=cov.pkg where coalesce(ev.obs,0)=0 and coalesce(w.asks,0)=0 order by cov.n desc limit 40;
\echo ===DENSITY_VS_SIGNAL_SUMMARY
with ev as (select split_part(purl,'@',1) pkg, sum(observation_count) obs from evidence_agg group by 1),
cov as (select split_part(sp.purl,'@',1) pkg, count(distinct sp.sample_id) n from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1),
w as (select 'pkg:'||ecosystem||'/'||name pkg, sum(asks) asks from wanted group by 1),
hits as (select split_part(sp.purl,'@',1) pkg, count(*) hits from search_hits h join sample_packages sp on sp.sample_id=h.sample_id group by 1)
select count(*) pkgs, sum(n) samples, count(*) filter (where coalesce(ev.obs,0)=0 and coalesce(w.asks,0)=0 and coalesce(hits.hits,0)=0) pkgs_zero_signal, sum(n) filter (where coalesce(ev.obs,0)=0 and coalesce(w.asks,0)=0 and coalesce(hits.hits,0)=0) samples_zero_signal, count(*) filter (where n>=10 and coalesce(ev.obs,0)=0 and coalesce(w.asks,0)=0 and coalesce(hits.hits,0)=0) dense_zero_signal_pkgs, sum(n) filter (where n>=10 and coalesce(ev.obs,0)=0 and coalesce(w.asks,0)=0 and coalesce(hits.hits,0)=0) dense_zero_signal_samples from cov left join ev on ev.pkg=cov.pkg left join w on w.pkg=cov.pkg left join hits on hits.pkg=cov.pkg;
\echo ===SEARCH_HITS_SUMMARY
select count(*) hits, count(distinct sample_id) distinct_samples, count(distinct anon_id) anon, min(created_at)::date, max(created_at)::date, count(*) filter (where created_at>now()-interval '30 days') hits_30d from search_hits;
select grade, count(*) from search_hits group by 1 order by 2 desc;
\echo ===SAMPLES_EVER_HIT
select count(distinct h.sample_id) from search_hits h join samples s on s.sample_id=h.sample_id where not s.quarantined;
\echo ===SEARCH_MISSES_SUMMARY
select count(*) misses, count(distinct anon_id) anon, count(*) filter (where created_at>now()-interval '30 days') misses_30d from search_misses;
\echo ===ADOPTIONS
select applied, build_pass, count(*), count(distinct sample_id) from adoptions group by 1,2;
\echo ===STALENESS_VS_OBSERVED
with sv as (select distinct sp.purl, split_part(sp.purl,'@',1) pkg, split_part(sp.purl,'@',2) ver from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined),
obs as (select split_part(purl,'@',1) pkg, split_part(purl,'@',2) ver, sum(observation_count) obs from evidence_agg where split_part(purl,'@',2) ~ '^v?[0-9]+\.[0-9]+' group by 1,2),
newest as (select pkg, (array_agg(ver order by string_to_array(regexp_replace(split_part(ver,'-',1),'^v',''),'.')::int[] desc))[1] newest_ver from obs group by 1)
select count(*) sample_purls, count(*) filter (where newest.pkg is not null) with_observed_versions, count(*) filter (where newest.pkg is not null and regexp_replace(sv.ver,'^v','') <> regexp_replace(newest.newest_ver,'^v','')) not_newest_observed, count(*) filter (where newest.pkg is not null and split_part(regexp_replace(sv.ver,'^v',''),'.',1) <> split_part(regexp_replace(newest.newest_ver,'^v',''),'.',1)) older_major_than_newest_observed from sv left join newest on newest.pkg=sv.pkg;
\echo ===STALE_MAJOR_EXAMPLES
with sv as (select distinct sp.purl, split_part(sp.purl,'@',1) pkg, split_part(sp.purl,'@',2) ver, count(*) over (partition by split_part(sp.purl,'@',1), split_part(sp.purl,'@',2)) from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined),
obs as (select split_part(purl,'@',1) pkg, split_part(purl,'@',2) ver, sum(observation_count) obs from evidence_agg where split_part(purl,'@',2) ~ '^v?[0-9]+\.[0-9]+' group by 1,2),
newest as (select pkg, (array_agg(ver order by string_to_array(regexp_replace(split_part(ver,'-',1),'^v',''),'.')::int[] desc))[1] newest_ver from obs group by 1)
select sv.purl, newest.newest_ver, (select count(*) from sample_packages sp2 join samples s2 on s2.sample_id=sp2.sample_id where not s2.quarantined and sp2.purl=sv.purl) samples from sv join newest on newest.pkg=sv.pkg where split_part(regexp_replace(sv.ver,'^v',''),'.',1) <> split_part(regexp_replace(newest.newest_ver,'^v',''),'.',1) order by 3 desc limit 25;
\echo ===SAMPLE_VERSION_OBSERVED_AT_ALL
with sv as (select distinct sp.purl from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined)
select count(*) sample_purls, count(*) filter (where exists (select 1 from evidence_agg e where e.purl=sv.purl)) observed_exact_version, count(*) filter (where exists (select 1 from dependency_edge d where 'pkg:'||d.ecosystem||'/'||(case when left(d.child_name,1)='@' then '%40'||substr(d.child_name,2) else d.child_name end)||'@'||d.child_version = sv.purl)) resolved_by_some_parent from sv;
\echo ===SNAPSHOT_SAMPLE_LINK
select count(*) snapshots, count(*) filter (where snapshot::text like '%sample%') mention_sample from compatibility_snapshots;
\echo ===TABLE_BLOAT
select relname, n_live_tup, n_dead_tup, pg_size_pretty(pg_relation_size(relid)) heap, pg_size_pretty(pg_total_relation_size(relid)-pg_relation_size(relid)) idx_toast, round(pg_relation_size(relid)::numeric/nullif(n_live_tup,0)) bytes_per_row from pg_stat_user_tables where relname in ('failure_clusters','compatibility_snapshots','evidence_agg','samples','receipts','shards','wanted','stats_daily','dependency_edge','evidence_dedup') order by pg_relation_size(relid) desc;
\echo ===FC_ROWS
select count(*) total, count(*) filter (where error_fp='' ) gap_rows, count(*) filter (where evidence_quality in ('missing','legacy-evidence-incomplete')) legacy, count(distinct package_name) pkgs from failure_clusters;
