package serverstore

// authoringCoverageCTE is what "already proven" and "still an open dependency"
// mean, written once.
//
// Two queries need it: the scheduler's candidate list, which turns the open
// set into work, and the operations panel's backlog, which counts it. A
// backlog that counts a different set from the queue it describes is worse
// than no number at all -- an operator would read a figure that never moves
// however hard the fleet runs, or one that hits zero while work is still being
// handed out. The two halves of the farm panel have already drifted apart once
// for exactly this reason, so there is one definition and both read it.
//
// It is the body of a WITH list: a caller writes "WITH " + authoringCoverageCTE
// and may append further CTEs after it.
const authoringCoverageCTE = `verified_samples AS MATERIALIZED (
				SELECT DISTINCT s.sample_id,s.manifest
				FROM samples s
				JOIN receipts r ON r.sample_id=s.sample_id AND r.contract_result='PASS'
				WHERE NOT s.quarantined
			), verified_packages AS MATERIALIZED (
				SELECT DISTINCT package.value AS purl
				FROM verified_samples s
				CROSS JOIN LATERAL jsonb_array_elements_text(
				  CASE WHEN jsonb_typeof(s.manifest->'packages')='array' THEN s.manifest->'packages' ELSE '[]'::jsonb END
				) AS package(value)
			), verified_package_targets AS MATERIALIZED (
				SELECT DISTINCT package.value AS purl,
				       LOWER(COALESCE(r.receipt->'environment'->>'os','')) AS target_os
				FROM samples s
				JOIN receipts r ON r.sample_id=s.sample_id AND r.contract_result='PASS'
				CROSS JOIN LATERAL jsonb_array_elements_text(
				  CASE WHEN jsonb_typeof(s.manifest->'packages')='array' THEN s.manifest->'packages' ELSE '[]'::jsonb END
				) AS package(value)
				WHERE NOT s.quarantined
				  AND LOWER(COALESCE(r.receipt->'environment'->>'os',''))<>''
			), proven_names AS MATERIALIZED (
				-- Package names the network already proves at SOME version.
				-- Their unproven releases are the sibling branch's business;
				-- see dependencyclosure.go for why they are not the
				-- dependency branch's.
				SELECT DISTINCT pk.ecosystem,pk.name
				FROM verified_package_targets t
				JOIN packages pk ON pk.purl=t.purl
			), dependency_open AS MATERIALIZED (
				-- Every edge whose child exists only because somebody's
				-- lockfile resolved onto it: unobserved, unproven, and not a
				-- release of a package we already prove elsewhere. This is the
				-- whole backlog, before any bound is applied to it.
				SELECT e.ecosystem,e.child_name,e.child_version,e.bucket,e.epoch
				FROM dependency_edge e
				CROSS JOIN LATERAL (SELECT
				    'pkg:'||e.ecosystem||'/'||
				      CASE WHEN left(e.parent_name,1)='@'
				           THEN '%40'||substring(e.parent_name from 2)
				           ELSE e.parent_name END||'@'||e.parent_version AS parent_purl,
				    'pkg:'||e.ecosystem||'/'||
				      CASE WHEN left(e.child_name,1)='@'
				           THEN '%40'||substring(e.child_name from 2)
				           ELSE e.child_name END||'@'||e.child_version AS child_purl) k
				-- The anchor: a package somebody LISTED, or one already
				-- proven. Without it the closure walks out of every shadow
				-- the network has ever seen.
				WHERE (EXISTS (SELECT 1 FROM evidence_agg a
				               WHERE a.purl=k.parent_purl AND a.direct)
				    OR EXISTS (SELECT 1 FROM verified_packages v WHERE v.purl=k.parent_purl))
				  -- Already observed means every other branch can already
				  -- reach it; already proven means there is nothing to ask.
				  AND NOT EXISTS (SELECT 1 FROM evidence_agg a WHERE a.purl=k.child_purl)
				  AND NOT EXISTS (SELECT 1 FROM verified_packages v WHERE v.purl=k.child_purl)
				  AND NOT EXISTS (SELECT 1 FROM proven_names n
				                  WHERE n.ecosystem=e.ecosystem AND n.name=e.child_name)
			)`
