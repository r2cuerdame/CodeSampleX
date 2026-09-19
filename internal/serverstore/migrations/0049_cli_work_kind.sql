-- The coverage scheduler now generates a fifth kind of work: a CLI
-- coordinate -- tool, version, command pattern, OS -- the farm fills by
-- running the command on a host of that OS (#81).
--
-- 0013 wrote the kind vocabulary as an inline CHECK and 0022 widened it once
-- already, for the same reason this does: a CLI assignment does not fail a
-- filter, it fails the INSERT, and the worker reads "claiming authoring work
-- failed" with no way to tell that the queue offered it something the schema
-- forbids. The Fake has no constraint to violate, so only a test against a
-- real database can see it (TestIntegrationCLIWorkCanActuallyBeClaimed).
ALTER TABLE authoring_assignments
  DROP CONSTRAINT IF EXISTS authoring_assignments_kind_check;

ALTER TABLE authoring_assignments
  ADD CONSTRAINT authoring_assignments_kind_check
    CHECK (kind IN ('WANTED','FINDING','EXPANSION','DEPENDENCY','CLI'));
