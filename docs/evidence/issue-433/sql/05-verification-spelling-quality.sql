\echo ===LATEST_FAIL_DETAIL
with lr as (select distinct on (sample_id) sample_id, contract_result, peer_id, created_at, receipt from receipts order by sample_id, created_at desc)
select lr.contract_result, s.status, lr.peer_id, count(*), min(lr.created_at)::date first, max(lr.created_at)::date last from samples s join lr on lr.sample_id=s.sample_id where not s.quarantined and lr.contract_result<>'PASS' group by 1,2,3 order by 4 desc;
\echo ===LATEST_FAIL_STAGEFAILURE_CODES
with lr as (select distinct on (sample_id) sample_id, contract_result, created_at, receipt from receipts order by sample_id, created_at desc)
select lr.contract_result, coalesce(sf->>'stage','?') stage, coalesce(sf->>'errorCode', sf->>'code','?') code, left(coalesce(sf->>'errorSummary',''),90) summary, count(*) from samples s join lr on lr.sample_id=s.sample_id left join lateral jsonb_array_elements(case when jsonb_typeof(lr.receipt->'stageFailures')='array' then lr.receipt->'stageFailures' else '[]'::jsonb end) sf on true where not s.quarantined and lr.contract_result<>'PASS' group by 1,2,3,4 order by 5 desc limit 30;
\echo ===LATEST_FAIL_HAD_EARLIER_PASS_DAYS
with lr as (select distinct on (sample_id) sample_id, contract_result, created_at from receipts order by sample_id, created_at desc), fp as (select sample_id, max(created_at) last_pass from receipts where contract_result='PASS' group by 1)
select lr.contract_result, count(*), round(avg(extract(epoch from (lr.created_at-fp.last_pass))/86400)::numeric,1) avg_days_since_pass, min(lr.created_at)::date, max(lr.created_at)::date from samples s join lr on lr.sample_id=s.sample_id join fp on fp.sample_id=s.sample_id where not s.quarantined and lr.contract_result<>'PASS' group by 1;
\echo ===LATEST_FAIL_ECO
with lr as (select distinct on (sample_id) sample_id, contract_result, created_at from receipts order by sample_id, created_at desc)
select lr.contract_result, split_part(split_part(s.manifest->'packages'->>0,':',2),'/',1) eco, count(*) from samples s join lr on lr.sample_id=s.sample_id where not s.quarantined and lr.contract_result<>'PASS' group by 1,2 order by 3 desc;
\echo ===LATEST_FAIL_IDS
with lr as (select distinct on (sample_id) sample_id, contract_result, created_at from receipts order by sample_id, created_at desc)
select lr.contract_result, s.sample_id, s.status, s.manifest->'packages'->>0 purl, lr.created_at::date from samples s join lr on lr.sample_id=s.sample_id where not s.quarantined and lr.contract_result<>'PASS' order by 1, 4;
\echo ===PURL_SPELLING
select count(*) filter (where manifest::text like '%pkg:npm/@%') raw_at_scoped, count(*) filter (where manifest::text like '%pkg:npm/%40%') encoded_scoped, count(*) filter (where manifest->'packages'->>0 ~ '^pkg:golang/[^@]+@[0-9]') golang_no_v, count(*) filter (where manifest->'packages'->>0 ~ '^pkg:golang/[^@]+@v[0-9]') golang_v from samples where not quarantined;
\echo ===PURL_SPELLING_SAMPLE_PACKAGES
select count(*) filter (where purl like 'pkg:npm/@%') raw_at, count(*) filter (where purl like 'pkg:npm/%40%') encoded, count(*) filter (where purl ~ '^pkg:golang/[^@]+@[0-9]') golang_no_v, count(*) filter (where purl ~ '^pkg:golang/[^@]+@v') golang_v from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined;
\echo ===PURL_SPELLING_PACKAGES_TABLE
select ecosystem, count(*) filter (where purl like 'pkg:npm/@%') raw_at, count(*) filter (where purl like 'pkg:npm/%40%') encoded, count(*) filter (where purl ~ '^pkg:golang/[^@]+@[0-9]') golang_no_v, count(*) filter (where purl ~ '^pkg:golang/[^@]+@v') golang_v, count(*) from packages group by 1 order by 6 desc;
\echo ===SAME_PACKAGE_TWO_SPELLINGS
with n as (select distinct case when purl like 'pkg:npm/@%' then replace(purl,'pkg:npm/@','pkg:npm/%40') else purl end canon, purl from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined and (purl like 'pkg:npm/@%' or purl like 'pkg:npm/%40%')) select count(*) purl_pairs_both_spellings from (select canon from n group by canon having count(distinct purl)>1) d;
\echo ===GOLANG_V_BOTH
with n as (select distinct regexp_replace(purl,'@v','@') canon, purl from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined and purl like 'pkg:golang/%') select count(*) from (select canon from n group by canon having count(distinct purl)>1) d;
\echo ===BUILDER_PURLS_VS_SAMPLE_PACKAGES
select count(*) from samples s where not quarantined and not (s.builder_purls @> (select array_agg(purl) from sample_packages sp where sp.sample_id=s.sample_id));
\echo ===STALE_VS_NEWEST_KNOWN
with sv as (select s.sample_id, split_part(sp.purl,'@',1) pkg, split_part(sp.purl,'@',2) ver from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined),
newest as (select split_part(purl,'@',1) pkg, max(string_to_array(regexp_replace(version,'^v',''),'.')::text[]) maxver_arr from packages where publicness='public' and version ~ '^v?[0-9]+(\.[0-9]+)*$' group by 1)
select count(distinct sv.sample_id) samples_checked, count(distinct sv.sample_id) filter (where regexp_replace(sv.ver,'^v','') <> array_to_string(newest.maxver_arr,'.')) samples_not_on_newest_known from sv join newest on newest.pkg=sv.pkg;
\echo ===RUNTIME_VERSIONS
select receipt->'environment'->>'runtime' rt, receipt->'environment'->>'runtimeVersion' rv, count(*) from receipts where contract_result='PASS' group by 1,2 order by 1, 3 desc;
\echo ===WEAK_CONTRACT_LINES
with l as (select s.sample_id, c.line from samples s, jsonb_array_elements_text(s.manifest->'case'->'contract') c(line) where not s.quarantined)
select count(*) lines, count(*) filter (where line ~* '^\S+ (is|are) (a |an )?(function|class|object|constant|string|number|boolean|defined|exported|available|callable)\b' or line ~* '\b(exports?|exposes?) (a |an )?(callable )?(function|class|object|constant)\b' or line ~* '\bis (defined|exported|available|callable)\b' or line ~* '\bcan be (imported|required)\b' or line ~* '\b(imports?|requires?) (successfully|without error)\b') existence_only, count(distinct sample_id) samples from l;
\echo ===SAMPLES_ALL_WEAK
with l as (select s.sample_id, c.line, (c.line ~* '^\S+ (is|are) (a |an )?(function|class|object|constant|string|number|boolean|defined|exported|available|callable)\b' or c.line ~* '\b(exports?|exposes?) (a |an )?(callable )?(function|class|object|constant)\b' or c.line ~* '\bis (defined|exported|available|callable)\b' or c.line ~* '\bcan be (imported|required)\b') weak from samples s, jsonb_array_elements_text(s.manifest->'case'->'contract') c(line) where not s.quarantined)
select count(*) filter (where nweak=n) all_weak, count(*) filter (where nweak>=n/2.0) half_weak from (select sample_id, count(*) n, count(*) filter (where weak) nweak from l group by 1) d;
\echo ===DUP_LINES_WITHIN_SAMPLE
select count(*) from (select s.sample_id from samples s, jsonb_array_elements_text(s.manifest->'case'->'contract') c(line) where not s.quarantined group by 1 having count(*)<>count(distinct c.line)) d;
\echo ===SYMBOL_NOT_IN_CONTRACT
with x as (select s.sample_id, y.sym, s.manifest->'case'->'contract' ct from samples s, jsonb_array_elements_text(s.manifest->'symbols') y(sym) where not s.quarantined)
select count(distinct sample_id) samples_with_symbols, count(*) symbol_rows, count(*) filter (where position(lower(regexp_replace(sym,'^.*[./]','')) in lower(ct::text))=0) symbol_never_named_in_contract, count(distinct sample_id) filter (where position(lower(regexp_replace(sym,'^.*[./]','')) in lower(ct::text))=0) samples_with_unnamed_symbol from x;
\echo ===SYMBOL_PKG_MISMATCH_GOLANG
select count(*) from samples s, jsonb_array_elements_text(s.manifest->'symbols') y(sym) where not s.quarantined and s.manifest->'packages'->>0 like 'pkg:golang/%' and position(split_part(substr(s.manifest->'packages'->>0, 12),'@',1) in y.sym)=0;
\echo ===SYMBOL_PKG_MISMATCH_GOLANG_EX
select s.sample_id, s.manifest->'packages'->>0, y.sym from samples s, jsonb_array_elements_text(s.manifest->'symbols') y(sym) where not s.quarantined and s.manifest->'packages'->>0 like 'pkg:golang/%' and position(split_part(substr(s.manifest->'packages'->>0, 12),'@',1) in y.sym)=0 limit 10;
\echo ===VJOBS
select status, reason, count(*) from verification_jobs group by 1,2 order by 3 desc;
\echo ===DRAFTS
select local_status, count(*), min(updated_at)::date, max(updated_at)::date from authoring_drafts group by 1;
\echo ===DRAFT_ORPHANS
select count(*) drafts_without_sample_row, count(*) filter (where s.sample_id is not null and not s.quarantined) drafts_of_live from authoring_drafts d left join samples s on s.sample_id=d.sample_id where s.sample_id is null or not s.quarantined;
\echo ===QUARANTINED_STILL_REFERENCED
select count(*) from samples s where s.quarantined and exists (select 1 from search_hits h where h.sample_id=s.sample_id);
\echo ===FINDINGS_BELIEVED_SHAPE
select jsonb_typeof(manifest->'case'->'believed') t, count(*) from samples where not quarantined and manifest->'case' ? 'believed' group by 1;
\echo ===BELIEVED_EQUALS_CONTRACT
select count(*) from samples where not quarantined and manifest->'case' ? 'believed' and exists (select 1 from jsonb_array_elements_text(manifest->'case'->'contract') c(l) where l = manifest->'case'->>'believed');
\echo ===SIZE
select percentile_cont(0.5) within group (order by size_bytes) p50, percentile_cont(0.9) within group (order by size_bytes) p90, max(size_bytes) mx, count(*) filter (where size_bytes<512) tiny from samples where not quarantined;
\echo ===HOT_SCORE
select count(*) filter (where hot_score>0) hot_gt0, count(*) from samples where not quarantined;
\echo ===LICENSE
select license, count(*) from samples where not quarantined group by 1;
