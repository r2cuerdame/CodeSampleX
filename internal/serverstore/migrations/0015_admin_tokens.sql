-- Long-lived operator API credentials. Like every other bearer this server
-- accepts, only the SHA-256 digest is stored: a row is useful to nobody who
-- does not already hold the token.
--
-- expires_at NULL means the token never expires on its own. That is a
-- deliberate option rather than an oversight -- an operator running a farm
-- needs a credential that outlives any session -- and it is exactly why
-- last_used_at and last_used_ip exist. A credential that cannot expire is
-- only observable through its use, so its use is what gets recorded.
CREATE TABLE admin_tokens(
  token_hash TEXT PRIMARY KEY,
  token_id TEXT NOT NULL UNIQUE,
  label TEXT NOT NULL,
  issued_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  last_used_ip TEXT,
  revoked_at TIMESTAMPTZ);

CREATE INDEX admin_tokens_live_idx
ON admin_tokens(issued_at DESC)
WHERE revoked_at IS NULL;
