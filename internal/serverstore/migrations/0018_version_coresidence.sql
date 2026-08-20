-- One library installed at two versions in a single resolution is the
-- commonest reason a build does not work, and the server cannot work it out
-- from evidence_agg: an observation batch carries a single package, so a
-- lockfile arrives already shredded into independent records and the finest
-- grouping left is a project and a day. A project that builds twice in an
-- afternoon against different lockfiles produces exactly the input that would
-- be read as a collision.
--
-- The scanner holds the lockfile at once and reports the pair as a fact. This
-- table stores those facts one row per (pair, project, day) so the counts are
-- of distinct projects rather than of how often anyone rebuilt.
CREATE TABLE IF NOT EXISTS version_coresidence (
  ecosystem      text        NOT NULL,
  name           text        NOT NULL,
  lower_version  text        NOT NULL,
  higher_version text        NOT NULL,
  bucket         text        NOT NULL,
  epoch          text        NOT NULL,
  -- A failure someone could name a cause for. An unattributed one says a
  -- build containing this package broke and nothing about which package
  -- broke it, so it is not evidence that these versions collided.
  failing        boolean     NOT NULL DEFAULT false,
  last_seen      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (ecosystem, name, lower_version, higher_version, bucket, epoch)
);

CREATE INDEX IF NOT EXISTS version_coresidence_lib_idx
  ON version_coresidence (ecosystem, name);
