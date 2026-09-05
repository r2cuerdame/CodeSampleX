-- Preserve literal substring search while avoiding two full manifest scans.
-- The live corpus had 7,012 samples when R2C-190 measured /samples?q=pgx at
-- 4.92s p50. The predicate is identical to SearchSamplesPage.
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE INDEX IF NOT EXISTS samples_manifest_lower_trgm_idx
ON samples USING gin ((lower(manifest::text)) public.gin_trgm_ops)
WHERE NOT quarantined;
