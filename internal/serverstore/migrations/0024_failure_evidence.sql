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

-- failure_clusters is derived data, but clearing existing production rows is a
-- destructive operation and is intentionally not part of this unattended
-- additive migration. Any re-key cleanup must run through a separately
-- authorized manual lifecycle with its own rollback and evidence.
