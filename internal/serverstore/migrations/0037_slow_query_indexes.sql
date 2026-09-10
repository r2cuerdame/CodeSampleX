-- P0 slow-query performance remediation for #174.
--
-- 1. failure_clusters: eliminate in-memory/spill sort on package detail pages
-- by indexing (package_name, observation_count DESC, id).
CREATE INDEX IF NOT EXISTS failure_clusters_pkg_count_idx
  ON failure_clusters (package_name, observation_count DESC, id);

-- 2. samples: eliminate Incremental Sort and pool timeouts on /samples pagination
-- by indexing (created_at DESC, sample_id) for live (unquarantined) samples.
CREATE INDEX IF NOT EXISTS samples_live_created_id_idx
  ON samples (created_at DESC, sample_id) WHERE NOT quarantined;
