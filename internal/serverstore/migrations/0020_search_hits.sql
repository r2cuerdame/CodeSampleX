-- Search hits: the denominator every rate this product is steered by was
-- missing.
--
-- The network could see the demand it could not satisfy and nothing of the
-- demand it could. A MISS uploads a Wanted ask, so misses arrive — 648 in the
-- first week. A HIT wrote a row in the caller's local hits table and stopped
-- there, so the server recorded five hits in its lifetime while 130 distinct
-- samples were adopted, and a sample can only be adopted after a hit surfaced
-- it. The one counter that looked like search volume, search_outcomes_daily,
-- only moves when something calls the HTTP endpoint directly, and the only
-- HTTP search client in the repo had no callers at all.
--
-- So "samples shown per search" and "applied per shown" could not be computed
-- network-side, and "nobody is searching" was read off a number that was
-- measuring something else.
--
-- Counts only. No query, no packages, no symbols, no environment: those stay
-- on the caller's machine, which is why hits were never uploaded in the first
-- place, and nothing here changes that.
CREATE TABLE IF NOT EXISTS search_hits (
    -- The grade bucket of the top result, as the client reported it.
    grade         text NOT NULL,
    -- How many results the caller was handed. The denominator itself.
    results_shown integer NOT NULL,
    -- The sample the top result named. A content address of something this
    -- network already published, so public by construction — and what makes
    -- "which answers actually get used" answerable when joined to adoptions.
    sample_id     text,
    -- The opaque local capability that ties this hit to the adoption that may
    -- follow it. Nothing is derivable from it; it exists so a numerator can
    -- find its own denominator.
    offer_id      text,
    epoch         text NOT NULL,
    anon_id       text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    -- One reporter counts once per offer, mirroring adoptions: an agent that
    -- retries a search all afternoon is one hit, not an afternoon of demand.
    -- The key is written by the server as the offer id, or the sample id when
    -- there is none, or empty — a primary key cannot hold an expression, and
    -- leaving the collapse implicit is how a retry becomes traffic.
    dedup_key     text NOT NULL,
    PRIMARY KEY (epoch, anon_id, dedup_key)
);

CREATE INDEX IF NOT EXISTS search_hits_day_idx ON search_hits (epoch);
CREATE INDEX IF NOT EXISTS search_hits_sample_idx ON search_hits (sample_id);
CREATE INDEX IF NOT EXISTS search_hits_offer_idx ON search_hits (offer_id);
