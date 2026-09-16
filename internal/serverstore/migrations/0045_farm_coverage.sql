-- CSX-452: the Builder-materialized read model behind GetFarmCoverage.
-- One row per (os, ecosystem) axis, holding the compatibility-map coverage
-- cell the Builder pass computed via the exact aggregation farm_pg.go's
-- FarmCoverage query already ran. The admin farm panel reads this by a
-- small whole-table scan instead of recomputing the corpus-wide join
-- (evidence_agg/receipts/samples JOIN packages) on every admin cache-miss.
--
-- All four FarmAxisCoverage counts are persisted, not just observed/proven:
-- the admin panel also renders measured and observedProven for buildable
-- cells (farmCoverageView in internal/admin/farm_http.go), and a read model
-- that dropped them would silently zero those columns on every render.
CREATE TABLE farm_coverage(
  os TEXT NOT NULL,
  ecosystem TEXT NOT NULL,
  observed INT NOT NULL DEFAULT 0,
  measured INT NOT NULL DEFAULT 0,
  proven INT NOT NULL DEFAULT 0,
  observed_proven INT NOT NULL DEFAULT 0,
  PRIMARY KEY(os, ecosystem));

-- farm_coverage_meta tracks publication state independently of row count.
-- A Builder pass that legitimately computes zero coverage cells (bootstrap
-- state, or a moment where every axis is unobserved) still calls
-- PutFarmCoverage with an empty slice, and farm_coverage then holds zero
-- rows -- indistinguishable, by row count alone, from "no pass has ever
-- published". This singleton row (the same pattern
-- anonymous_analytics_collection uses) is written in the same transaction
-- as every farm_coverage replace, so its mere existence is "found", and its
-- generated_at is the one place the pass timestamp lives now (removed from
-- farm_coverage itself, where it was redundant across every row of one
-- publish anyway).
CREATE TABLE farm_coverage_meta(
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  generated_at TIMESTAMPTZ NOT NULL);
