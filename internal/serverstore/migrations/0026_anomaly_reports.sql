-- The consumption side of the network gets a way to answer back.
--
-- An agent that used a CSX answer and then watched its own machine
-- contradict it is holding the one observation the container farm cannot
-- produce: what happened somewhere the farm will never run. This table is
-- where that observation waits for a verifier to agree or disagree with it.
--
-- It is deliberately not evidence. Nothing public reads this table; a row
-- becomes visible to anybody only through the signed receipt a verifier
-- writes afterwards, which travels the existing receipt path like every
-- other verification.
CREATE TABLE anomaly_reports(
  id BIGSERIAL PRIMARY KEY,
  -- The dedupe key: exact public coordinate plus the shape of the
  -- mismatch. UNIQUE is the whole spam defence -- one bad answer cannot
  -- queue a thousand containers, however many agents hit it.
  fingerprint TEXT NOT NULL UNIQUE,
  report JSONB NOT NULL,
  anomaly_type TEXT NOT NULL,
  purl TEXT NOT NULL,
  symbol TEXT NOT NULL DEFAULT '',
  -- No foreign key to samples on purpose. A report that names a sample id
  -- this server does not have is not a broken row: it is one of the
  -- anomaly types, and refusing to store it would erase the report that
  -- proves the reference is broken.
  sample_id TEXT,
  status TEXT NOT NULL DEFAULT 'queued',
  verdict TEXT NOT NULL DEFAULT '',
  unsupported_reason TEXT NOT NULL DEFAULT '',
  -- Likewise unconstrained: the report outlives the queue row, and a
  -- report must never be lost because a job it pointed at was retired.
  job_id BIGINT,
  reports BIGINT NOT NULL DEFAULT 1,
  reporter_bucket TEXT NOT NULL DEFAULT '',
  first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
  verdict_at TIMESTAMPTZ);

CREATE INDEX anomaly_reports_open_idx ON anomaly_reports(sample_id, verdict);
CREATE INDEX anomaly_reports_window_idx ON anomaly_reports(last_seen);
