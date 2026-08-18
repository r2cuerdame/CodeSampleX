-- Private sample-worker inbox. Draft artifacts live in the content-addressed
-- blob store. LOCAL_PASS enters samples only as a quarantined DRAFT; a signed
-- PASS for its separately claimed cross job is the sole automatic publish gate.
CREATE TABLE authoring_drafts(
  sample_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  worker_label TEXT NOT NULL,
  manifest JSONB NOT NULL,
  local_status TEXT NOT NULL CHECK(local_status IN ('LOCAL', 'LOCAL_PASS')),
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL CHECK(updated_at >= created_at));

CREATE INDEX authoring_drafts_updated_idx
ON authoring_drafts(updated_at DESC);

-- One live authoring lease per worker and one reserved writer per exact
-- Wanted coordinate. Completed rows remain until their sample cross-passes;
-- a failed cross receipt releases the coordinate for another attempt.
CREATE TABLE authoring_assignments(
  ecosystem TEXT NOT NULL,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  symbol TEXT NOT NULL,
  asks BIGINT NOT NULL,
  session_id TEXT NOT NULL,
  claimed_at TIMESTAMPTZ NOT NULL,
  lease_expires_at TIMESTAMPTZ NOT NULL,
  sample_id TEXT,
  completed_at TIMESTAMPTZ,
  PRIMARY KEY(ecosystem, name, version, symbol));

CREATE UNIQUE INDEX authoring_assignments_live_session_idx
ON authoring_assignments(session_id)
WHERE sample_id IS NULL;
