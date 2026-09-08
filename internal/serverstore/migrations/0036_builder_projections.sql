-- Source-bound, Go-validated inputs for incremental compatibility passes.
-- NULL/mismatched hashes are indexed and block scoped reads until repaired.
-- Rollback: drop these additive columns (CASCADE removes their indexes).
ALTER TABLE samples ADD COLUMN IF NOT EXISTS builder_coords text[] NOT NULL DEFAULT '{}';
ALTER TABLE samples ADD COLUMN IF NOT EXISTS builder_purls text[] NOT NULL DEFAULT '{}';
ALTER TABLE samples ADD COLUMN IF NOT EXISTS builder_symbols text[] NOT NULL DEFAULT '{}';
ALTER TABLE samples ADD COLUMN IF NOT EXISTS builder_subject text NOT NULL DEFAULT '';
ALTER TABLE samples ADD COLUMN IF NOT EXISTS builder_source_hash text;
ALTER TABLE samples ADD COLUMN IF NOT EXISTS builder_previous_purls text[] NOT NULL DEFAULT '{}';
ALTER TABLE samples ADD COLUMN IF NOT EXISTS builder_previous_symbols text[] NOT NULL DEFAULT '{}';
ALTER TABLE receipts ADD COLUMN IF NOT EXISTS builder_packages text[] NOT NULL DEFAULT '{}';
ALTER TABLE receipts ADD COLUMN IF NOT EXISTS builder_coords text[] NOT NULL DEFAULT '{}';
ALTER TABLE receipts ADD COLUMN IF NOT EXISTS builder_claim boolean NOT NULL DEFAULT false;
ALTER TABLE receipts ADD COLUMN IF NOT EXISTS builder_source_hash text;
-- Every GIN read also scans its pending list. Insert unrelated source keys
-- directly into the index so bounded reads do not depend on a later vacuum.
CREATE INDEX IF NOT EXISTS samples_builder_coords_idx ON samples USING gin(builder_coords) WITH (fastupdate=off);
CREATE INDEX IF NOT EXISTS samples_builder_symbols_idx ON samples USING gin(builder_symbols) WITH (fastupdate=off);
CREATE INDEX IF NOT EXISTS receipts_builder_coords_idx ON receipts USING gin(builder_coords) WITH (fastupdate=off);
CREATE INDEX IF NOT EXISTS samples_builder_stale_idx ON samples(sample_id)
    WHERE builder_source_hash IS DISTINCT FROM md5(manifest::text);
CREATE INDEX IF NOT EXISTS receipts_builder_stale_idx ON receipts(receipt_id)
    WHERE builder_source_hash IS DISTINCT FROM md5(receipt::text);

-- Internal package identity for indexed target/retirement reads. Decode once,
-- keep the raw name (escaping a leading @ would collide with literal %40),
-- and use PG17's builtin Unicode simple mapping to match Go strings.ToLower.
-- The default libc locale on Alpine lowercases only ASCII.
-- Invalid spellings yield NULL and remain subject to the full repair parser.
-- SQL-language single statement: compatible with the small migration runner.
CREATE FUNCTION builder_purl_coord(raw TEXT) RETURNS TEXT
LANGUAGE SQL IMMUTABLE STRICT PARALLEL SAFE AS $$
  WITH parsed AS (
    SELECT regexp_match(raw, '^pkg:([^/]+)/(.+)@([^@]+)$') AS m
  ), decoded AS (
    SELECT m, convert_from(decode((
      SELECT string_agg(CASE WHEN left(token[1],1)='%' AND length(token[1])=3
        THEN substring(token[1] from 2)
        ELSE encode(convert_to(token[1], 'UTF8'), 'hex') END, '' ORDER BY n)
      FROM regexp_matches(m[2], '%[0-9A-Fa-f]{2}|[^%]', 'g') WITH ORDINALITY AS t(token,n)
    ), 'hex'), 'UTF8') AS name
    FROM parsed
    WHERE m IS NOT NULL AND right(m[2],1) <> '/'
      AND m[2] !~ '%([^0-9A-Fa-f]|[0-9A-Fa-f]([^0-9A-Fa-f]|$)|$)'
  )
  SELECT 'pkg:' || lower(m[1] COLLATE pg_catalog."pg_c_utf8") || '/' ||
    lower(name COLLATE pg_catalog."pg_c_utf8") || '@' FROM decoded
$$;

CREATE INDEX evidence_agg_builder_coord_idx
  ON evidence_agg(builder_purl_coord(purl), purl, symbol);
CREATE INDEX snapshots_builder_coord_idx
  ON compatibility_snapshots(builder_purl_coord(purl), purl, symbol);
CREATE INDEX evidence_agg_builder_changed_idx ON evidence_agg(last_seen, purl, symbol);
CREATE INDEX samples_builder_created_idx ON samples(created_at, sample_id);
