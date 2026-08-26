-- The miss side of the No-match rate, in the same unit as the hit side.
--
-- 0020 gave hits a timestamped, deduplicated row so a rate could have a
-- denominator. The other half stayed unmeasurable in time: a miss becomes
-- wanted rows, which are a demand RANKING -- one row per coordinate, no
-- timestamp on the dedup ledger at all. So the two halves could not be
-- compared, and a report that named three packages weighed three times a
-- report that named one.
--
-- This table counts the question. One row per Wanted report, keyed by the
-- reporter, the UTC day and a digest of the whole coordinate set, which makes
-- a retried search land on the primary key and be dropped -- exactly how a
-- retried hit is dropped by its offer id.
--
-- Nothing new travels or is retained. The digest is derived from package
-- coordinates that wanted_dedup already stores in the clear beside the same
-- anon id, and the question itself has never left the caller's machine.
CREATE TABLE IF NOT EXISTS search_misses (
    epoch      text NOT NULL,
    anon_id    text NOT NULL,
    -- sha256 of the report's sorted coordinate set. Order-independent,
    -- because neither the batch nor the daemon's drain promises one.
    dedup_key  text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (epoch, anon_id, dedup_key)
);

-- Every read of this table is a bounded recent window, newest first.
CREATE INDEX IF NOT EXISTS search_misses_created_idx ON search_misses (created_at DESC);

-- 0020 indexed the day string and the ids, which answer "how many today" but
-- not "how many in the last hour" -- and an hour is the window that separates
-- a lull from a stall.
CREATE INDEX IF NOT EXISTS search_hits_created_idx ON search_hits (created_at DESC);

-- samples had a partial index over the live rows only, so counting what was
-- accepted AND what is still held in one window fell back to a sequential
-- scan on a page with a five-second budget.
CREATE INDEX IF NOT EXISTS samples_created_at_idx ON samples (created_at DESC);
