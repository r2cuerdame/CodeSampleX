-- Who pulled what, as the machine holding the lockfile saw it.
--
-- version_coresidence says two versions of a library were installed together.
-- That is the half of the answer nobody can act on; this is the other half.
-- The server cannot derive it: an observation batch carries a single package,
-- so a resolution arrives already shredded and no grouping finer than a
-- project-day survives.
--
-- One row per (edge, project, day), so the counts are of distinct projects
-- rather than of how often anyone rebuilt.
CREATE TABLE IF NOT EXISTS dependency_edge (
  ecosystem      text        NOT NULL,
  parent_name    text        NOT NULL,
  parent_version text        NOT NULL,
  child_name     text        NOT NULL,
  child_version  text        NOT NULL,
  bucket         text        NOT NULL,
  epoch          text        NOT NULL,
  last_seen      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (ecosystem, parent_name, parent_version, child_name, child_version, bucket, epoch)
);

-- The question this table exists to answer is "who wanted THIS version of the
-- child", so the child end is what gets the index.
CREATE INDEX IF NOT EXISTS dependency_edge_child_idx
  ON dependency_edge (ecosystem, child_name, child_version);
