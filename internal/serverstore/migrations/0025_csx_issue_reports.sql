-- Product-defect candidates, kept deliberately apart from anomaly_reports.
--
-- An anomaly report can end up as compatibility evidence. A product defect
-- never can, and one table for both would be the way that boundary
-- eventually leaks. They share ingest, redaction and dedupe; they share
-- nothing after it.
--
-- The policy this table exists to enforce is one line: a defect many agents
-- meet is ONE row whose occurrence count goes up, never a growing pile of
-- tickets. Once an operator links that row to a canonical bug, every later
-- occurrence answers with the link instead of creating anything.
CREATE TABLE csx_issue_reports(
  id BIGSERIAL PRIMARY KEY,
  fingerprint TEXT NOT NULL UNIQUE,
  report JSONB NOT NULL,
  surface TEXT NOT NULL,
  issue_kind TEXT NOT NULL,
  component TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'triage',
  verdict TEXT NOT NULL DEFAULT '',
  replay_reason TEXT NOT NULL DEFAULT '',
  -- Set by an operator, never by a reporter. A reporter that could name the
  -- ticket could also aim reports at it.
  canonical_ref TEXT NOT NULL DEFAULT '',
  occurrences BIGINT NOT NULL DEFAULT 1,
  reporter_bucket TEXT NOT NULL DEFAULT '',
  first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
  verdict_at TIMESTAMPTZ);

CREATE INDEX csx_issue_reports_window_idx ON csx_issue_reports(last_seen);
CREATE INDEX csx_issue_reports_status_idx ON csx_issue_reports(status, verdict);
