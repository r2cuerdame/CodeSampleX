-- A miss is about the requested release and symbol, not merely the package
-- name.  The first wanted table discarded the version from the PURL, which
-- meant that publishing any sample for a package hid every still-unanswered
-- request for every other release and API.
ALTER TABLE wanted ADD COLUMN version text NOT NULL DEFAULT '';
ALTER TABLE wanted DROP CONSTRAINT wanted_pkey;
ALTER TABLE wanted ADD PRIMARY KEY (ecosystem, name, version, symbol);

ALTER TABLE wanted_dedup ADD COLUMN version text NOT NULL DEFAULT '';
ALTER TABLE wanted_dedup DROP CONSTRAINT wanted_dedup_pkey;
ALTER TABLE wanted_dedup ADD PRIMARY KEY (ecosystem, name, version, symbol, epoch, anon_id);
