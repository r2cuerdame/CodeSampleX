-- Incremental aggregation decides what to rebuild from evidence timestamps,
-- sample creation and receipt arrival. That covers every status change made
-- by the request path, and misses every one made outside it: a quarantine,
-- or a status corrected by recompute-status, changes no timestamp the
-- builder looks at, so the materialized shard keeps advertising the old
-- state indefinitely.
--
-- Observed: 25 samples correctly downgraded from MATRIX_PASS in the
-- database while their shards still served MATRIX_PASS. A quarantined
-- sample would have stayed visible the same way, which is worse.
ALTER TABLE samples ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS samples_updated_at_idx ON samples(updated_at);
