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
  generated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY(os, ecosystem));
