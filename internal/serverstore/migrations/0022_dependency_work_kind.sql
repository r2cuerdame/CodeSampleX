-- The coverage scheduler now generates a fourth kind of work: a coordinate
-- that exists only because somebody's lockfile resolved onto it.
--
-- 0013 wrote the kind vocabulary as an inline CHECK, so a DEPENDENCY
-- assignment does not fail a filter -- it fails the INSERT, and the worker
-- reads "claiming authoring work failed" with no way to tell that the queue
-- offered it something the schema forbids. Nothing in the in-memory store can
-- surface that: the Fake has no constraint to violate, so every test would
-- pass while production refused every dependency job it produced.
ALTER TABLE authoring_assignments
  DROP CONSTRAINT IF EXISTS authoring_assignments_kind_check;

ALTER TABLE authoring_assignments
  ADD CONSTRAINT authoring_assignments_kind_check
    CHECK (kind IN ('WANTED','FINDING','EXPANSION','DEPENDENCY'));
