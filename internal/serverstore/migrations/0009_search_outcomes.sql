-- Privacy-safe search-quality counters. A row stores only a UTC date and two
-- aggregate successful-response outcomes. There is deliberately no query,
-- package, symbol, path, user, network bucket, client, or request identifier.
CREATE TABLE IF NOT EXISTS search_outcomes_daily (
    day         date PRIMARY KEY,
    sample_hits bigint NOT NULL DEFAULT 0 CHECK (sample_hits >= 0),
    no_matches  bigint NOT NULL DEFAULT 0 CHECK (no_matches >= 0),
    updated_at  timestamptz NOT NULL
);
