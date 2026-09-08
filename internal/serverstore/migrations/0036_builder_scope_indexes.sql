-- Builder candidate indexes derive from the stored documents themselves.
-- Index creation covers historical/CLI writes and PostgreSQL keeps subsequent
-- direct SQL writes in sync. These are candidate keys, never receipt authority.
-- Go still validates the complete signed package list and global symbol claims.
--
-- A noncanonical percent escape could alias a different Go-decoded name.
-- Mark it with ! so incremental reads can fail closed through the same index.
-- ASCII case folding is shared by PostgreSQL and Go. Non-ASCII identities use
-- the same sentinel instead of depending on the database locale.
--
-- Rollback: first restore a binary without scoped builder reads, then DROP the
-- six builder_* indexes below and csx_builder_coords(jsonb),
-- csx_builder_coord(text). No source or materialized data is rewritten.

CREATE FUNCTION csx_builder_coord(raw text) RETURNS text
LANGUAGE SQL IMMUTABLE PARALLEL SAFE
AS $coord$
WITH input AS (
  SELECT substr(raw, 5) AS rest
), pieces AS (
  SELECT strpos(rest, '/') AS slash_at,
         split_part(rest, '/', 1) AS ecosystem,
         substr(rest, strpos(rest, '/') + 1) AS body
  FROM input
), bounds AS (
  SELECT *, strpos(reverse(body), '@') AS version_at FROM pieces
), parts AS (
  SELECT *, left(body, length(body) - version_at) AS package_name,
         substr(body, length(body) - version_at + 2) AS version
  FROM bounds
)
SELECT CASE
  WHEN raw IS NULL OR left(raw, 4) <> 'pkg:' OR slash_at = 0
    OR ecosystem = '' OR version_at = 0 OR package_name = ''
    OR version = '' OR right(package_name, 1) = '/' THEN ''
  WHEN octet_length(ecosystem) <> length(ecosystem)
    OR octet_length(package_name) <> length(package_name) THEN '!'
  WHEN strpos(CASE WHEN left(package_name, 3) = '%40'
                   THEN substr(package_name, 4) ELSE package_name END, '%') > 0
    THEN '!'
  ELSE lower('pkg:' || ecosystem || '/' ||
    CASE WHEN left(package_name, 1) = '@'
         THEN '%40' || substr(package_name, 2) ELSE package_name END || '@')
END
FROM parts
$coord$;

CREATE FUNCTION csx_builder_coords(packages jsonb) RETURNS text[]
LANGUAGE SQL IMMUTABLE PARALLEL SAFE
AS $coords$
SELECT COALESCE(array_agg(DISTINCT coord ORDER BY coord)
                  FILTER (WHERE coord <> ''), ARRAY[]::text[])
FROM (
  SELECT csx_builder_coord(value #>> '{}') AS coord
  FROM jsonb_array_elements(
    CASE WHEN jsonb_typeof(packages) = 'array'
         THEN packages ELSE '[]'::jsonb END
  ) AS package(value)
  WHERE jsonb_typeof(value) = 'string'
) AS coordinates
$coords$;

CREATE INDEX builder_samples_packages_idx
  ON samples USING gin (csx_builder_coords(manifest->'packages'));
CREATE INDEX builder_receipts_packages_idx
  ON receipts USING gin (csx_builder_coords(receipt->'resolvedPackages'));
CREATE INDEX builder_samples_subject_idx
  ON samples (csx_builder_coord(manifest->>'subject'));
CREATE INDEX builder_samples_symbols_idx
  ON samples USING gin ((manifest->'symbols'));
CREATE INDEX builder_evidence_coord_idx
  ON evidence_agg (csx_builder_coord(purl), purl, symbol);
CREATE INDEX builder_snapshots_coord_idx
  ON compatibility_snapshots (csx_builder_coord(purl), purl, symbol);
