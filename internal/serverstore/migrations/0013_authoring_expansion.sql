-- Sample writers exhaust unresolved Wanted coordinates quickly while their
-- drafts wait for independent verification. Keep them productive by recording
-- whether the leased coordinate came from explicit demand or the server's
-- evidence-driven expansion queue.
ALTER TABLE authoring_assignments
  ADD COLUMN kind TEXT NOT NULL DEFAULT 'WANTED'
    CHECK(kind IN ('WANTED','FINDING','EXPANSION')),
  ADD COLUMN score BIGINT NOT NULL DEFAULT 0 CHECK(score >= 0);
