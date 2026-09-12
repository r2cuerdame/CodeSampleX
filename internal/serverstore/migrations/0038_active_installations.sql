-- Active installations presence tracking (GitHub #383).
--
-- Domain-separated rotating HMAC presence tokens derived locally from anonSeed.
-- Stable only inside their own epoch (1d, 7d, 30d aligned blocks) and unlinkable
-- across epoch boundaries.
-- Intentionally measures current aligned 1/7/30-day windows, NOT sliding DAU/WAU/MAU.
-- They are never people, users, or MAU.
--
-- Explicit clientClass distinguishes ordinary/external public installations from
-- internal/farm/ci/verifier/operator nodes.
CREATE TABLE IF NOT EXISTS active_installations (
  id BIGSERIAL PRIMARY KEY,
  interval_kind TEXT NOT NULL,
  epoch TEXT NOT NULL,
  token TEXT NOT NULL,
  client_class TEXT NOT NULL,
  client_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT active_installations_token_key UNIQUE (interval_kind, epoch, token)
);

CREATE INDEX IF NOT EXISTS active_installations_count_idx
ON active_installations (interval_kind, epoch, client_class);

CREATE INDEX IF NOT EXISTS active_installations_prune_idx
ON active_installations (updated_at);
