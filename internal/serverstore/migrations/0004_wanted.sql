-- Questions the network could not answer.
--
-- Every NO_SAFE_MATCH is a demand signal, and until now it was thrown away:
-- the miss was counted locally, on the machine that asked, where nobody
-- could act on it. Aggregated it is the most useful thing this network can
-- tell a contributor — here is what people keep asking that nobody has
-- answered — and it is the only ranking of demand that is not a guess.
--
-- What is stored is deliberately narrow. The QUESTION ITSELF IS NEVER
-- SENT: a typed sentence carries project names, file paths and worse, and
-- goal.md §8.5 keeps those on the machine. What arrives is the part that
-- was already public in the request — the package, the symbol, the
-- ecosystem — so the row can say WHAT was wanted without saying who wanted
-- it or what they were building.
CREATE TABLE IF NOT EXISTS wanted (
    ecosystem   text NOT NULL,
    name        text NOT NULL,
    symbol      text NOT NULL DEFAULT '',
    -- asks is a count of distinct anonymous reporters per epoch, not of
    -- requests: one machine asking the same thing all afternoon is one
    -- data point, and counting keystrokes would let a single caller
    -- manufacture a ranking.
    asks        bigint NOT NULL DEFAULT 0,
    first_seen  timestamptz NOT NULL DEFAULT now(),
    last_seen   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ecosystem, name, symbol)
);

CREATE INDEX IF NOT EXISTS wanted_rank_idx ON wanted (asks DESC, last_seen DESC);

-- The dedup ledger, mirroring evidence_dedup: one reporter counts once per
-- epoch per row, so the same machine asking every day adds one per day
-- rather than one per search.
CREATE TABLE IF NOT EXISTS wanted_dedup (
    ecosystem text NOT NULL,
    name      text NOT NULL,
    symbol    text NOT NULL DEFAULT '',
    epoch     text NOT NULL,
    anon_id   text NOT NULL,
    PRIMARY KEY (ecosystem, name, symbol, epoch, anon_id)
);
