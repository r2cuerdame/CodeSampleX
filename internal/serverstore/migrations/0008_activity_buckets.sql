-- Privacy-bounded external API network estimates. The only identifier is a
-- 128-bit HMAC bucket scoped independently to its day or month epoch. The
-- application prunes to exactly 35 daily and 13 monthly epochs including the
-- current UTC epoch even when collection is disabled. These are keyed pseudonyms,
-- not anonymous values: the colocated CSX_ACTIVITY_HASH_KEY is the
-- privacy boundary, and after key compromise the 2^32 IPv4 space is enumerable.
-- There is intentionally no raw address, header, route, or cross-period
-- identifier in this activity-estimate table.
CREATE TABLE IF NOT EXISTS activity_buckets (
    kind       text NOT NULL CHECK (kind IN ('day', 'month')),
    epoch      text NOT NULL,
    bucket     bytea NOT NULL CHECK (octet_length(bucket) = 16),
    owner      boolean NOT NULL DEFAULT false,
    first_seen timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    PRIMARY KEY (kind, epoch, bucket)
);

CREATE INDEX IF NOT EXISTS activity_buckets_epoch_idx
ON activity_buckets (kind, epoch) INCLUDE (owner);

-- One bounded marker per UTC day proves collector maintenance ran even when
-- there was genuinely no meaningful API traffic. Absence is never treated as
-- a fabricated zero; it remains a visible collection gap.
CREATE TABLE IF NOT EXISTS activity_health (
    epoch      text PRIMARY KEY,
    checked_at timestamptz NOT NULL
);
