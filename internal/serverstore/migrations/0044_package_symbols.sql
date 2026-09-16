-- CSX-452: the Builder-materialized read model behind GetPackageSymbols.
-- One row per purl, holding the exact (already globally-attributed) symbol
-- list the Builder pass computed for it via snapshotTargetsFromClaims.
-- Public request handlers read this by primary key instead of recomputing
-- the corpus-wide attribution that produced it.
CREATE TABLE package_symbols (
  purl TEXT PRIMARY KEY,
  symbols JSONB NOT NULL,
  generated_at TIMESTAMPTZ NOT NULL
);
