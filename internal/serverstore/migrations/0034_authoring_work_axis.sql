-- A scheduler assignment now records which of the three completeness assets
-- it is meant to produce. The existing kind remains the source/ranking lane
-- (Wanted, Finding, Expansion, Dependency); axis is the deliverable.
--
-- No constraint rewrite is needed on the hot assignments table. Old rows and
-- rolled-back binaries read as SAMPLE, and current code validates the closed
-- vocabulary before inserting a new row.
ALTER TABLE authoring_assignments
  ADD COLUMN axis TEXT NOT NULL DEFAULT 'SAMPLE';
