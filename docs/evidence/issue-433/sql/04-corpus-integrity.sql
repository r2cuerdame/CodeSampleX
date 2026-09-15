\echo ===LIVE_BASE
select count(*) live, count(*) filter (where status='CROSS_PASS') cross_pass, count(*) filter (where status='STABLE') stable, count(*) filter (where status='PUBLISHED') published from samples where not quarantined;
\echo ===LIVE_WITHOUT_PASS_RECEIPT
select s.status, count(*) from samples s where not s.quarantined and not exists (select 1 from receipts r where r.sample_id=s.sample_id and r.contract_result='PASS') group by 1;
\echo ===LIVE_WITH_ANY_FAIL
select count(distinct s.sample_id) from samples s join receipts r on r.sample_id=s.sample_id where not s.quarantined and r.contract_result='FAIL';
\echo ===LIVE_LATEST_RECEIPT_FAIL
with lr as (select distinct on (sample_id) sample_id, contract_result, created_at from receipts order by sample_id, created_at desc) select lr.contract_result, count(*) from samples s join lr on lr.sample_id=s.sample_id where not s.quarantined group by 1;
\echo ===RECEIPT_RESULT_DIST
select contract_result, receipt->'stages'->>'contract' stage_contract, receipt->'stages'->>'resolve' stage_resolve, receipt->'stages'->>'compile' stage_compile, count(*) from receipts group by 1,2,3,4 order by 5 desc;
\echo ===RECEIPT_COL_VS_JSON_MISMATCH
select count(*) from receipts where contract_result is distinct from (receipt->'stages'->>'contract');
\echo ===RECEIPT_SAMPLEID_MISMATCH
select count(*) from receipts where receipt->>'sampleId' <> sample_id;
\echo ===RECEIPT_CASEID_MISMATCH
select count(*) from receipts r join samples s on s.sample_id=r.sample_id where r.receipt->>'caseId' is distinct from s.manifest->'case'->>'caseId';
\echo ===SAMPLE_CASEID_MISMATCH
select count(*) filter (where s.case_id is null) case_null, count(*) filter (where s.case_id is distinct from s.manifest->'case'->>'caseId') case_col_mismatch, count(*) filter (where s.case_id is not null and not exists (select 1 from cases c where c.case_id=s.case_id)) case_missing from samples s where not s.quarantined;
\echo ===RECEIPT_ENVHASH_MISMATCH
select count(*) from receipts where env_hash <> receipt->>'environmentHash';
\echo ===RECEIPT_SCHEMA_IMAGE
select receipt->>'schemaVersion' sv, (receipt ? 'verifierImage') has_image, (receipt ? 'resolvedPackages') has_resolved, count(*) from receipts group by 1,2,3 order by 4 desc;
\echo ===LIVE_PASS_WITHOUT_IMAGE
select count(distinct s.sample_id) from samples s where not s.quarantined and not exists (select 1 from receipts r where r.sample_id=s.sample_id and r.contract_result='PASS' and r.receipt ? 'verifierImage');
\echo ===PEERS
select count(distinct peer_id) peers_total, count(distinct peer_id) filter (where contract_result='PASS') peers_pass from receipts;
select peer_id, count(*) receipts, count(*) filter (where contract_result='PASS') pass, min(created_at)::date first, max(created_at)::date last from receipts group by 1 order by 2 desc;
\echo ===PEERS_PER_LIVE_SAMPLE
with p as (select s.sample_id, s.status, count(distinct r.peer_id) filter (where r.contract_result='PASS') pass_peers, count(r.*) filter (where r.contract_result='PASS') pass_receipts from samples s left join receipts r on r.sample_id=s.sample_id where not s.quarantined group by 1,2) select status, pass_peers, count(*) from p group by 1,2 order by 1,2;
\echo ===ORIGIN_SEEDER
select origin_seeder, status, count(*) from samples where not quarantined group by 1,2 order by 3 desc;
\echo ===RESOLVED_VS_DECLARED
with x as (select s.sample_id, (select array_agg(p order by p) from jsonb_array_elements_text(s.manifest->'packages') p) declared, (select array_agg(p order by p) from jsonb_array_elements_text(r.receipt->'resolvedPackages') p) resolved from samples s join receipts r on r.sample_id=s.sample_id and r.contract_result='PASS' and r.receipt ? 'resolvedPackages' where not s.quarantined) select count(distinct sample_id) samples_with_resolved, count(distinct sample_id) filter (where not (declared <@ resolved)) declared_not_in_resolved from x;
\echo ===DECLARED_NOT_IN_RESOLVED_BY_ECO
with x as (select s.sample_id, split_part(split_part(s.manifest->'packages'->>0,':',2),'/',1) eco, (select array_agg(p order by p) from jsonb_array_elements_text(s.manifest->'packages') p) declared, (select array_agg(p order by p) from jsonb_array_elements_text(r.receipt->'resolvedPackages') p) resolved from samples s join receipts r on r.sample_id=s.sample_id and r.contract_result='PASS' and r.receipt ? 'resolvedPackages' where not s.quarantined) select eco, count(distinct sample_id) filter (where not (declared <@ resolved)) mism, count(distinct sample_id) total from x group by 1 order by 2 desc;
\echo ===DECLARED_NOT_IN_RESOLVED_EXAMPLES
with x as (select s.sample_id, (select array_agg(p order by p) from jsonb_array_elements_text(s.manifest->'packages') p) declared, (select array_agg(p order by p) from jsonb_array_elements_text(r.receipt->'resolvedPackages') p) resolved from samples s join receipts r on r.sample_id=s.sample_id and r.contract_result='PASS' and r.receipt ? 'resolvedPackages' where not s.quarantined) select sample_id, declared, resolved from x where not (declared <@ resolved) limit 8;
\echo ===SAMPLE_PACKAGES_LINK
select count(*) filter (where not exists (select 1 from sample_packages sp where sp.sample_id=s.sample_id)) live_without_sample_packages, count(*) filter (where (select count(*) from sample_packages sp where sp.sample_id=s.sample_id) <> jsonb_array_length(s.manifest->'packages')) count_mismatch from samples s where not s.quarantined;
\echo ===SUBJECT
select count(*) filter (where manifest->>'subject' is null) no_subject, count(*) filter (where manifest->>'subject' is not null and not (manifest->'packages' ? (manifest->>'subject'))) subject_not_in_packages, count(*) filter (where jsonb_array_length(manifest->'packages')=0) zero_packages, count(*) filter (where jsonb_array_length(manifest->'packages')>1) multi_packages from samples where not quarantined;
\echo ===SYMBOLS
select count(*) filter (where manifest->'symbols' is null or jsonb_array_length(manifest->'symbols')=0) no_symbols, count(*) filter (where manifest->'symbols' is distinct from manifest->'case'->'symbols') symbols_vs_case_mismatch, count(*) filter (where manifest->'packages' is distinct from manifest->'case'->'packages') packages_vs_case_mismatch from samples where not quarantined;
\echo ===CONTRACT_SHAPE
select jsonb_array_length(manifest->'case'->'contract') n, count(*) from samples where not quarantined group by 1 order by 1;
\echo ===GOAL_TEMPLATE
select count(*) total, count(*) filter (where manifest->'case'->>'goal' ~ '^verify .* in pkg:') template_goal, count(*) filter (where manifest->'case'->>'goal' ~ '^verify pkg:') template_goal2, count(*) filter (where length(manifest->'case'->>'goal') < 25) short_goal from samples where not quarantined;
\echo ===GOAL_PREFIXES
select left(manifest->'case'->>'goal', 12) pre, count(*) from samples where not quarantined group by 1 order by 2 desc limit 12;
\echo ===KIND
select manifest->'case'->>'kind', count(*) from samples where not quarantined group by 1 order by 2 desc;
\echo ===BELIEVED
select count(*) filter (where manifest->'case' ? 'believed') believed, count(*) filter (where manifest->'case' ? 'constraints') constraints from samples where not quarantined;
\echo ===EXACT_DUP_PURL_SYMBOLS
with pub as (select sample_id, manifest->'packages'->>0 purl, coalesce((select string_agg(y,',' order by y) from jsonb_array_elements_text(case when jsonb_typeof(manifest->'symbols')='array' then manifest->'symbols' else '[]'::jsonb end) y),'') syms from samples where not quarantined) select count(*) groups, sum(c)-count(*) redundant from (select purl, syms, count(*) c from pub group by 1,2 having count(*)>1) d;
\echo ===EXACT_DUP_CONTRACT
select count(*) groups, sum(c)-count(*) redundant from (select manifest->'case'->'contract' ct, count(*) c from samples where not quarantined group by 1 having count(*)>1) d;
\echo ===DUP_CONTRACT_EXAMPLES
select c, left(ct::text,160) from (select manifest->'case'->'contract' ct, count(*) c from samples where not quarantined group by 1 having count(*)>1 order by 2 desc limit 6) d;
\echo ===DUP_GOAL
select count(*) groups, sum(c)-count(*) redundant from (select manifest->'case'->>'goal' g, count(*) c from samples where not quarantined group by 1 having count(*)>1) d;
\echo ===DUP_CASEID
select count(*) groups, sum(c)-count(*) redundant from (select case_id, count(*) c from samples where not quarantined and case_id is not null group by 1 having count(*)>1) d;
\echo ===DUP_PKG_SYMBOLS_CROSSVERSION
with pub as (select sample_id, split_part(manifest->'packages'->>0,'@',1) pkg, coalesce((select string_agg(y,',' order by y) from jsonb_array_elements_text(case when jsonb_typeof(manifest->'symbols')='array' then manifest->'symbols' else '[]'::jsonb end) y),'') syms from samples where not quarantined) select count(*) groups, sum(c)-count(*) redundant from (select pkg, syms, count(*) c from pub group by 1,2 having count(*)>1) d;
\echo ===PER_PACKAGE_DENSITY
with pub as (select sample_id, split_part(manifest->'packages'->>0,'@',1) pkg from samples where not quarantined) select pkg, count(*) from pub group by 1 order by 2 desc limit 40;
\echo ===PACKAGE_DIST
with pub as (select split_part(manifest->'packages'->>0,'@',1) pkg, count(*) n from samples where not quarantined group by 1) select count(*) packages, sum(n) samples, percentile_cont(0.5) within group (order by n) p50, percentile_cont(0.9) within group (order by n) p90, max(n) mx, count(*) filter (where n=1) single, sum(n) filter (where n>=20) samples_in_pkgs_ge20, count(*) filter (where n>=20) pkgs_ge20 from pub;
\echo ===ECO_DIST
select split_part(split_part(manifest->'packages'->>0,':',2),'/',1) eco, count(*) from samples where not quarantined group by 1 order by 2 desc;
\echo ===VERSIONS_PER_PKG
with pub as (select split_part(manifest->'packages'->>0,'@',1) pkg, split_part(manifest->'packages'->>0,'@',2) ver from samples where not quarantined) select count(distinct pkg) pkgs, count(distinct (pkg,ver)) pkg_versions, count(distinct pkg) filter (where true) x from pub;
