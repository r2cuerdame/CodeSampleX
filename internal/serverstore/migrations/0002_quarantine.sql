-- Sample publishing is anonymous by design, and until now there was no way
-- to take anything back: no delete, no TTL, no admin path. A single bad or
-- abusive sample was permanent. Quarantine hides a sample from search,
-- shards and the explorer without destroying the evidence trail behind it —
-- receipts and cases stay intact, so a mistaken quarantine is reversible
-- and a real one is auditable.
ALTER TABLE samples ADD COLUMN IF NOT EXISTS quarantined BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE samples ADD COLUMN IF NOT EXISTS quarantine_reason TEXT;
ALTER TABLE samples ADD COLUMN IF NOT EXISTS quarantined_at TIMESTAMPTZ;

-- Serving reads always filter on this, so it earns an index.
CREATE INDEX IF NOT EXISTS samples_live_idx ON samples(created_at DESC) WHERE NOT quarantined;
