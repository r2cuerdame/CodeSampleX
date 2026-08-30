-- A resolution that found no dependencies at all.
--
-- The dependency axis answers a coordinate when it appears as a PARENT in
-- dependency_edge, and a package that declares nothing can never be a parent.
-- Every leaf therefore sat in the open column forever, and no amount of farm
-- work could close it: measured on production 2026-08-30, 490 coordinates
-- appear as a child of some resolved tree and never as a parent, a quarter of
-- everything open on that axis.
--
-- Kept apart from dependency_edge rather than stored as an edge with no child.
-- An edge is a relationship between two releases; a row here is the absence of
-- any, and writing that as a half-edge would make every reader of that table
-- carry the special case.
--
-- The key matches dependency_edge's grain (project-day) so the two tables
-- count the same way, even though nothing needs a project count from this one
-- yet: presence is the whole fact.
CREATE TABLE IF NOT EXISTS dependency_resolution (
  ecosystem text        NOT NULL,
  name      text        NOT NULL,
  version   text        NOT NULL,
  bucket    text        NOT NULL,
  epoch     text        NOT NULL,
  last_seen timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (ecosystem, name, version, bucket, epoch)
);

-- The axis reads "is this release answered", so the lookup is by coordinate.
CREATE INDEX IF NOT EXISTS dependency_resolution_coordinate_idx
  ON dependency_resolution (ecosystem, name, version);
