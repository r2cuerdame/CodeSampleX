\pset format unaligned
\pset tuples_only on
\echo ===DUP_PURL_SYMBOLS_GROUPS
with pub as (select sample_id, status, created_at, manifest->'packages'->>0 purl, coalesce((select string_agg(y,',' order by y) from jsonb_array_elements_text(case when jsonb_typeof(manifest->'symbols')='array' then manifest->'symbols' else '[]'::jsonb end) y),'') syms from samples where not quarantined)
select json_agg(json_build_object('purl',purl,'symbols',syms,'count',c,'sampleIds',ids) order by c desc) from (select purl, syms, count(*) c, array_agg(sample_id order by created_at) ids from pub group by 1,2 having count(*)>1) d;
\echo ===DUP_CONTRACT_GROUPS
select json_agg(json_build_object('count',c,'packages',pk,'sampleIds',ids) order by c desc) from (select count(*) c, array_agg(distinct manifest->'packages'->>0) pk, array_agg(sample_id order by created_at) ids from samples where not quarantined group by manifest->'case'->'contract' having count(*)>1) d;
\echo ===DUP_CASEID_GROUPS
select json_agg(json_build_object('caseId',case_id,'sampleIds',ids)) from (select case_id, array_agg(sample_id order by created_at) ids from samples where not quarantined and case_id is not null group by 1 having count(*)>1) d;
\echo ===DECLARED_NOT_RESOLVED
with x as (select s.sample_id, (select array_agg(p order by p) from jsonb_array_elements_text(s.manifest->'packages') p) declared, (select array_agg(p order by p) from jsonb_array_elements_text(r.receipt->'resolvedPackages') p) resolved from samples s join receipts r on r.sample_id=s.sample_id and r.contract_result='PASS' and r.receipt ? 'resolvedPackages' where not s.quarantined)
select json_agg(json_build_object('sampleId',sample_id,'declared',declared,'resolved',resolved)) from (select distinct on (sample_id) sample_id, declared, resolved from x where not (declared <@ resolved)) d;
\echo ===SYMBOLS_VS_CASE
select json_agg(json_build_object('sampleId',sample_id,'symbols',manifest->'symbols','caseSymbols',manifest->'case'->'symbols')) from samples where not quarantined and manifest->'symbols' is distinct from manifest->'case'->'symbols';
\echo ===LATEST_NONPASS
with lr as (select distinct on (sample_id) sample_id, contract_result, peer_id, created_at, (receipt ? 'stageFailures') has_sf, receipt->'verifierImage'->>'reference' img from receipts order by sample_id, created_at desc)
select json_agg(json_build_object('sampleId',s.sample_id,'status',s.status,'purl',s.manifest->'packages'->>0,'latestResult',lr.contract_result,'latestAt',lr.created_at,'peer',lr.peer_id,'hasStageFailures',lr.has_sf,'verifierImage',lr.img) order by lr.contract_result, s.manifest->'packages'->>0) from samples s join lr on lr.sample_id=s.sample_id where not s.quarantined and lr.contract_result<>'PASS';
\echo ===RAW_AT_SCOPED
select json_agg(json_build_object('sampleId',sample_id,'purl',manifest->'packages'->>0)) from samples where not quarantined and manifest->'packages'->>0 like 'pkg:npm/@%';
\echo ===NO_SUBJECT_COUNT_BY_ECO
select json_agg(json_build_object('eco',eco,'noSubject',n)) from (select split_part(split_part(manifest->'packages'->>0,':',2),'/',1) eco, count(*) n from samples where not quarantined and manifest->>'subject' is null group by 1) d;
\echo ===DENSITY_TOP
with pub as (select sample_id, split_part(manifest->'packages'->>0,'@',1) pkg, split_part(manifest->'packages'->>0,'@',2) ver from samples where not quarantined),
w as (select 'pkg:'||ecosystem||'/'||replace(name,'@','%40') pkg, sum(asks) asks from wanted group by 1),
hits as (select split_part(sp.purl,'@',1) pkg, count(*) hits from search_hits h join sample_packages sp on sp.sample_id=h.sample_id group by 1)
select json_agg(json_build_object('package',pkg,'samples',n,'versions',v,'asks',asks,'searchHits',hits) order by n desc) from (select pub.pkg, count(*) n, count(distinct ver) v, coalesce(max(w.asks),0) asks, coalesce(max(hits.hits),0) hits from pub left join w on w.pkg=pub.pkg left join hits on hits.pkg=pub.pkg group by 1 order by 2 desc limit 60) d;
\echo ===STALE_MAJOR
with sv as (select sp.purl, split_part(sp.purl,'@',1) pkg, regexp_replace(split_part(sp.purl,'@',2),'^v','') ver, count(distinct sp.sample_id) samples from sample_packages sp join samples s on s.sample_id=sp.sample_id where not s.quarantined group by 1,2,3),
obs as (select split_part(purl,'@',1) pkg, regexp_replace(split_part(purl,'@',2),'^v','') ver, sum(observation_count) obs from evidence_agg where regexp_replace(split_part(purl,'@',2),'^v','') ~ '^[0-9]+\.[0-9]+(\.[0-9]+)?$' group by 1,2),
newest as (select pkg, (array_agg(ver order by string_to_array(ver,'.')::int[] desc))[1] newest_ver from obs group by 1)
select json_agg(json_build_object('purl',sv.purl,'newestObservedVersion',newest.newest_ver,'samples',sv.samples) order by sv.samples desc) from sv join newest on newest.pkg=sv.pkg where sv.ver ~ '^[0-9]+\.[0-9]+(\.[0-9]+)?$' and split_part(sv.ver,'.',1)::int < split_part(newest.newest_ver,'.',1)::int;
