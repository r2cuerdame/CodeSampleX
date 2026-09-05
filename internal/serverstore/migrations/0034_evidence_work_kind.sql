-- The completeness scheduler generates a fifth kind of work: a PUBLIC release
-- nothing has ever been recorded running, whose missing axis is Evidence.
--
-- The vocabulary is an inline CHECK, so an EVIDENCE assignment does not fail a
-- filter -- it fails the INSERT, and the worker reads "claiming authoring work
-- failed" with no way to tell that the queue offered it something the schema
-- forbids. Nothing in the in-memory store can surface that: the Fake has no
-- constraint to violate, so every unit test would pass while production
-- refused every evidence job it produced. 0022 learned this for DEPENDENCY;
-- TestIntegrationEveryOfferedWorkKindCanBeClaimed is what stops it being
-- learned a third time.
ALTER TABLE authoring_assignments
  DROP CONSTRAINT IF EXISTS authoring_assignments_kind_check;

ALTER TABLE authoring_assignments
  ADD CONSTRAINT authoring_assignments_kind_check
    CHECK (kind IN ('WANTED','FINDING','EXPANSION','DEPENDENCY','EVIDENCE'));
