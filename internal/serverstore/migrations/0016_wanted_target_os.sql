-- A miss is about the platform it happened on. Without one, every ask was
-- unpinned: the queue handed Windows reports to whichever verifier asked
-- first, and a Linux pass then closed the row -- so the platform the report
-- was actually about was never measured, and the ask was gone. The network
-- reached 2,729 packages observed on Windows and zero proven there.
--
-- An empty target_os keeps the previous meaning exactly: a question about the
-- package rather than about a platform, answerable by anyone who can run it.
-- Existing rows backfill to '' and behave as they always have.
ALTER TABLE wanted ADD COLUMN target_os text NOT NULL DEFAULT '';
ALTER TABLE wanted DROP CONSTRAINT wanted_pkey;
ALTER TABLE wanted ADD PRIMARY KEY (ecosystem, name, version, symbol, target_os);

-- The dedup ledger keys on the coordinate, so it must widen with it: one
-- reporter hitting the same release on two platforms is two data points, and
-- the narrower key would have silently discarded the second.
ALTER TABLE wanted_dedup ADD COLUMN target_os text NOT NULL DEFAULT '';
ALTER TABLE wanted_dedup DROP CONSTRAINT wanted_dedup_pkey;
ALTER TABLE wanted_dedup ADD PRIMARY KEY (ecosystem, name, version, symbol, target_os, epoch, anon_id);
