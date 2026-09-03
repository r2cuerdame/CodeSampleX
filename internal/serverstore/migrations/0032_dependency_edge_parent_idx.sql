-- Fast lookup for Dependencies(ecosystem, parent_name).
-- 0019 indexed (ecosystem, child_name, child_version), so looking up a parent's
-- first-level dependencies required a sequential scan across all edges.
CREATE INDEX IF NOT EXISTS dependency_edge_parent_idx
  ON dependency_edge (ecosystem, parent_name);
