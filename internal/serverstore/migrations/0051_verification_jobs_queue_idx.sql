-- Keep the public claimable queue in its requested order without sorting the
-- completed and unsupported history on every poll. Expired claims remain
-- visible through the existing query's second branch.
CREATE INDEX IF NOT EXISTS verification_jobs_open_queue_idx
  ON verification_jobs (created_at, id)
  WHERE status = 'open';
