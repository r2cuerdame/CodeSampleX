-- Private internal sample-authoring capabilities. Raw bearer tokens are never
-- stored; a row is useful only to a caller that already holds the token.
CREATE TABLE authoring_sessions(
  token_hash TEXT PRIMARY KEY,
  session_id TEXT NOT NULL UNIQUE,
  label TEXT NOT NULL,
  model TEXT NOT NULL,
  reasoning TEXT NOT NULL,
  issued_at TIMESTAMPTZ NOT NULL,
  last_refreshed_at TIMESTAMPTZ,
  idle_expires_at TIMESTAMPTZ NOT NULL,
  last_refresh_ip TEXT,
  computer_name TEXT,
  revoked_at TIMESTAMPTZ);

CREATE INDEX authoring_sessions_active_idx
ON authoring_sessions(idle_expires_at DESC)
WHERE revoked_at IS NULL;
