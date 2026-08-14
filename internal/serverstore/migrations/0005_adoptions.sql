-- Adoption evidence: an agent applied a sample, and the build then passed
-- or failed.
--
-- This is the far end of the loop the whole product describes — ask, get a
-- verified answer, report whether it worked — and it was never connected.
-- The client enqueued every report into its local upload_queue, nothing
-- ever drained that table, and the server had no route to receive one, so
-- postHitSuccessRate was a hardcoded 0 with a comment explaining that
-- adoption reporting had not reached the server yet.
--
-- It is the only feedback the network gets about whether its answers are
-- any good. Without it every other number here describes how much the
-- network KNOWS, and nothing describes whether any of it HELPED.
CREATE TABLE IF NOT EXISTS adoptions (
    sample_id  text NOT NULL,
    -- applied=false is a real and useful report: the agent looked at the
    -- sample and did not use it. Counting only successes would make the
    -- rate a measure of enthusiasm rather than of usefulness.
    applied    boolean NOT NULL,
    -- build_pass is NULL when the reporter did not run a build afterwards.
    -- Unknown stays unknown; it is never folded into either bucket.
    build_pass boolean,
    epoch      text NOT NULL,
    anon_id    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- One reporter counts once per sample per epoch, mirroring
    -- evidence_dedup: an agent that retries all afternoon is one report.
    PRIMARY KEY (sample_id, epoch, anon_id)
);

CREATE INDEX IF NOT EXISTS adoptions_sample_idx ON adoptions (sample_id);
CREATE INDEX IF NOT EXISTS adoptions_created_idx ON adoptions (created_at DESC);
