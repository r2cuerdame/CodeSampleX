-- #517: what the compatibility Builder last did, durably.
--
-- The stats rollup's generatedAt is only ever written by a pass that
-- finished, so from 2026-09-17T12:34Z production kept publishing the same
-- stamp for nine days and nothing public or admin could say whether passes
-- were starting, timing out, losing the lease or failing. This row is written
-- at the end of every pass, whatever it ended with: the last success, the last
-- failure and its reason, and -- while an exhaustive repair is being walked in
-- chunks -- how far it got, so a pass that is stopped part-way (ceiling,
-- lease loss, restart) is continued rather than started over.
--
-- One row per named Builder, keyed like builder_lease. Additive: nothing reads
-- it until this build does, and an absent row reads as "no status yet".
CREATE TABLE builder_status(
  name TEXT PRIMARY KEY,
  status JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now())
