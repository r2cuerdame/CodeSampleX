-- CSX-451: the leader lock a standalone Builder process holds so a second
-- one (a deploy overlap, a stuck restart) cannot run the aggregation
-- pipeline at the same time. One row per named lease; the compatibility
-- Builder uses the single name "compatibility-builder".
CREATE TABLE builder_lease (
  name TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  fence BIGINT NOT NULL,
  acquired_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX builder_lease_expires_at_idx ON builder_lease (expires_at);
