-- Stable pseudonymous client analytics, distinct from rotating presence.
-- No IP, raw credential, URL, user agent, account or project data.
CREATE TABLE anonymous_clients (
  client_hash TEXT PRIMARY KEY CHECK (client_hash ~ '^[0-9a-f]{64}$'),
  first_seen TIMESTAMPTZ NOT NULL,
  last_seen TIMESTAMPTZ NOT NULL,
  request_count BIGINT NOT NULL CHECK (request_count > 0)
);
CREATE INDEX anonymous_clients_first_seen_idx ON anonymous_clients (first_seen);
CREATE INDEX anonymous_clients_last_seen_idx ON anonymous_clients (last_seen);
CREATE TABLE anonymous_client_days (
  day DATE NOT NULL,
  client_hash TEXT NOT NULL REFERENCES anonymous_clients(client_hash) ON DELETE CASCADE,
  request_count BIGINT NOT NULL CHECK (request_count > 0),
  PRIMARY KEY (day, client_hash)
);
CREATE INDEX anonymous_client_days_client_idx ON anonymous_client_days (client_hash, day);
CREATE TABLE anonymous_analytics_collection (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  started_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO anonymous_analytics_collection(singleton) VALUES (TRUE);
