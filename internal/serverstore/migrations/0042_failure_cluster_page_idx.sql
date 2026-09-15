-- Package pages need an exact cluster count and only the highest-count rows.
-- Keep the current-cluster predicate in the index so COUNT(*) can use this
-- narrow structure without revisiting the wide JSONB-heavy heap ledger.
-- A normal CREATE INDEX is intentional: migrations are transactional, so
-- CONCURRENTLY is unavailable, and production's reviewed offline runner first
-- stops the server/builder and proves no database clients remain. It then runs
-- this transaction with a 5-second lock timeout and verifies the exact index is
-- valid and ready before it starts the candidate server.
CREATE INDEX IF NOT EXISTS failure_clusters_current_page_idx
  ON failure_clusters (ecosystem, package_name, observation_count DESC, id)
  WHERE (
    COALESCE(evidence_quality, 'legacy-evidence-incomplete') NOT IN
      ('missing', 'legacy-evidence-incomplete')
    OR COALESCE(error_fp, '') = ''
  );
