ALTER TABLE evidence_agg ADD COLUMN termination_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN exit_code INTEGER;
ALTER TABLE evidence_agg ADD COLUMN signal TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN timeout_millis BIGINT NOT NULL DEFAULT 0;
ALTER TABLE evidence_agg ADD COLUMN error_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN evidence_quality TEXT NOT NULL DEFAULT 'legacy-evidence-incomplete';

UPDATE evidence_agg SET evidence_quality = '' WHERE result = 'PASS';

ALTER TABLE failure_clusters ADD COLUMN termination_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_clusters ADD COLUMN exit_code INTEGER;
ALTER TABLE failure_clusters ADD COLUMN signal TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_clusters ADD COLUMN timeout_millis BIGINT NOT NULL DEFAULT 0;
ALTER TABLE failure_clusters ADD COLUMN error_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_clusters ADD COLUMN evidence_quality TEXT NOT NULL DEFAULT 'legacy-evidence-incomplete';
ALTER TABLE failure_clusters ADD COLUMN env_variants JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE failure_clusters ADD COLUMN evidence_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE failure_clusters ADD COLUMN diagnostic_candidate BOOLEAN NOT NULL DEFAULT false;

-- failure_clusters is a derived/materialized table. Legacy evidence is
-- deliberately re-keyed by the new builder: old opaque fingerprints collapse
-- into an explicit Evidence-gap key. Keeping pre-migration rows would expose
-- both the stale historical key and the rebuilt gap cluster. Clear the derived
-- rows here; the compatibility builder repopulates them from evidence_agg and
-- verification receipts after deployment. Source evidence is not deleted.
TRUNCATE TABLE failure_clusters;