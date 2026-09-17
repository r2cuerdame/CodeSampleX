-- Execution footprints (#318): the zero-install half of the adoption loop.
--
-- An agent that reached the network over plain HTTPS -- no csx binary, no
-- MCP host, no local offer to correlate against -- may still have run the
-- sample it was handed. Throwing that away because it did not arrive through
-- the CLI loses the one thing a web caller can honestly tell us: it ran, and
-- this is what happened. It is kept, but it is kept APART.
--
-- This table is deliberately not adoptions and not evidence_agg. A footprint
-- is self-reported and unsigned: nobody sanitized it on the caller's machine,
-- no receipt vouches for the environment, and no local correlation proves
-- the sample was the thing that ran. Nothing here reaches
-- compatibility_snapshots, the confidence ladder, the stats rollup's
-- postHitBuildsReported, or any grade. The evidence class it is filed under
-- (EXECUTION_FOOTPRINT) weighs zero in compatibility.ClassWeight so that a
-- future join cannot promote it by accident.
--
-- Bounded on purpose. Every column is a fixed enum, a short lowercase token,
-- a content address or a hash; there is no free text, no path, no log and no
-- raw address. source_bucket is SHA-256(epoch | client address) so the same
-- source on the same day is one row per sample and stage however often it
-- reports, and the address itself is never stored.
CREATE TABLE execution_footprints (
  id bigserial PRIMARY KEY,
  dedup_key text NOT NULL UNIQUE,
  sample_id text NOT NULL,
  outcome text NOT NULL,
  stage text NOT NULL DEFAULT '',
  env_os text NOT NULL DEFAULT '',
  env_arch text NOT NULL DEFAULT '',
  env_runtime text NOT NULL DEFAULT '',
  env_runtime_version text NOT NULL DEFAULT '',
  failure_fingerprint text NOT NULL DEFAULT '',
  epoch text NOT NULL,
  source_bucket text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX execution_footprints_sample_idx ON execution_footprints (sample_id);
CREATE INDEX execution_footprints_created_idx ON execution_footprints (created_at);
