-- Fold Go module versions onto their canonical v-prefixed spelling.
--
-- A Go module version is canonically v-prefixed. A bare `5.10.0` for golang
-- names no release — but it parses, stores and renders exactly like one, so
-- the corpus held the same release twice: `github.com/jackc/pgx/v5@v5.10.0`
-- at rank 4 on the wanted board with `@5.10.0` as a separate row beneath it,
-- their asks not adding up, one question shown to a reader as two entries.
--
-- ParsePURL now repairs the spelling on the way in (domain.CanonicalVersion),
-- which stops new splits and makes this fold necessary rather than optional:
-- a stored bare row is a coordinate no parsed purl can reach any more, so
-- leaving it would silently strand its evidence instead of merging it.
--
-- Measured on production 2026-08-31 before this ran: 8 package rows across 6
-- names, of which 6 had a canonical twin to merge into and 2 did not, and 16
-- wanted rows with no twin at all. Both directions are handled here anyway,
-- because a migration runs on databases nobody measured.
--
-- The `major` column needs no repair: PURL.Major() has prepended the missing
-- "v" at shard-key time for as long as both shapes existed, so every stored
-- major is already canonical. That workaround stays where it is; it still
-- covers PURLs built directly rather than parsed.

-- packages. Merge into the canonical row where one exists: the earliest
-- sighting and the latest are both facts and must survive the merge, and a
-- decided publicness must never lose to UNKNOWN.
UPDATE packages c SET
  first_seen  = LEAST(c.first_seen, b.first_seen),
  last_seen   = GREATEST(c.last_seen, b.last_seen),
  publicness  = CASE WHEN c.publicness = 'UNKNOWN' AND b.publicness <> 'UNKNOWN'
                     THEN b.publicness ELSE c.publicness END,
  checked_at  = CASE WHEN c.publicness = 'UNKNOWN' AND b.publicness <> 'UNKNOWN'
                     THEN b.checked_at ELSE c.checked_at END
FROM packages b
WHERE b.ecosystem = 'golang' AND b.version <> '' AND b.version !~ '^v'
  AND c.ecosystem = 'golang' AND c.name = b.name AND c.version = 'v' || b.version;

DELETE FROM packages b
WHERE b.ecosystem = 'golang' AND b.version <> '' AND b.version !~ '^v'
  AND EXISTS (SELECT 1 FROM packages c
              WHERE c.ecosystem = 'golang' AND c.name = b.name
                AND c.version = 'v' || b.version);

UPDATE packages SET version = 'v' || version,
                    purl = 'pkg:golang/' || name || '@v' || version
WHERE ecosystem = 'golang' AND version <> '' AND version !~ '^v';

-- wanted. Asks are a count of people who asked, so a merge adds them: that
-- undercount is what pushed a real demand down the board.
UPDATE wanted c SET
  asks       = c.asks + b.asks,
  first_seen = LEAST(c.first_seen, b.first_seen),
  last_seen  = GREATEST(c.last_seen, b.last_seen)
FROM wanted b
WHERE b.ecosystem = 'golang' AND b.version <> '' AND b.version !~ '^v'
  AND c.ecosystem = 'golang' AND c.name = b.name AND c.symbol = b.symbol
  AND c.target_os = b.target_os AND c.version = 'v' || b.version;

DELETE FROM wanted b
WHERE b.ecosystem = 'golang' AND b.version <> '' AND b.version !~ '^v'
  AND EXISTS (SELECT 1 FROM wanted c
              WHERE c.ecosystem = 'golang' AND c.name = b.name AND c.symbol = b.symbol
                AND c.target_os = b.target_os AND c.version = 'v' || b.version);

UPDATE wanted SET version = 'v' || version
WHERE ecosystem = 'golang' AND version <> '' AND version !~ '^v';

-- authoring_assignments. A live lease on the canonical coordinate wins; the
-- bare duplicate is dropped rather than merged, because two rows cannot both
-- be one writer's single live claim and the canonical one is the one a client
-- will ask about again.
DELETE FROM authoring_assignments b
WHERE b.ecosystem = 'golang' AND b.version <> '' AND b.version !~ '^v'
  AND EXISTS (SELECT 1 FROM authoring_assignments c
              WHERE c.ecosystem = 'golang' AND c.name = b.name AND c.symbol = b.symbol
                AND c.version = 'v' || b.version);

UPDATE authoring_assignments SET version = 'v' || version
WHERE ecosystem = 'golang' AND version <> '' AND version !~ '^v';

-- sample_packages. The pair is (sample, purl) and carries no counts, so a
-- duplicate is simply redundant.
DELETE FROM sample_packages b
WHERE b.purl LIKE 'pkg:golang/%@%'
  AND substring(b.purl from '@([^@]*)$') !~ '^v'
  AND substring(b.purl from '@([^@]*)$') ~ '^[0-9]'
  AND EXISTS (SELECT 1 FROM sample_packages c
              WHERE c.sample_id = b.sample_id
                AND c.purl = regexp_replace(b.purl, '@([^@]*)$', '@v\1'));

UPDATE sample_packages SET purl = regexp_replace(purl, '@([^@]*)$', '@v\1')
WHERE purl LIKE 'pkg:golang/%@%'
  AND substring(purl from '@([^@]*)$') !~ '^v'
  AND substring(purl from '@([^@]*)$') ~ '^[0-9]';

-- dependency_edge. Both ends carry a version, so both are folded, and the key
-- includes bucket and epoch: a collision means the same edge was recorded for
-- the same project-day under two spellings, and the later sighting wins.
UPDATE dependency_edge c SET last_seen = GREATEST(c.last_seen, b.last_seen)
FROM dependency_edge b
WHERE b.ecosystem = 'golang' AND c.ecosystem = 'golang'
  AND c.parent_name = b.parent_name AND c.child_name = b.child_name
  AND c.bucket = b.bucket AND c.epoch = b.epoch
  AND c.parent_version = CASE WHEN b.parent_version ~ '^[0-9]'
                              THEN 'v' || b.parent_version ELSE b.parent_version END
  AND c.child_version = CASE WHEN b.child_version ~ '^[0-9]'
                             THEN 'v' || b.child_version ELSE b.child_version END
  AND (b.parent_version ~ '^[0-9]' OR b.child_version ~ '^[0-9]');

DELETE FROM dependency_edge b
WHERE b.ecosystem = 'golang'
  AND (b.parent_version ~ '^[0-9]' OR b.child_version ~ '^[0-9]')
  AND EXISTS (SELECT 1 FROM dependency_edge c
              WHERE c.ecosystem = 'golang'
                AND c.parent_name = b.parent_name AND c.child_name = b.child_name
                AND c.bucket = b.bucket AND c.epoch = b.epoch
                AND c.parent_version = CASE WHEN b.parent_version ~ '^[0-9]'
                                            THEN 'v' || b.parent_version ELSE b.parent_version END
                AND c.child_version = CASE WHEN b.child_version ~ '^[0-9]'
                                           THEN 'v' || b.child_version ELSE b.child_version END);

UPDATE dependency_edge
SET parent_version = CASE WHEN parent_version ~ '^[0-9]' THEN 'v' || parent_version ELSE parent_version END,
    child_version  = CASE WHEN child_version  ~ '^[0-9]' THEN 'v' || child_version  ELSE child_version  END
WHERE ecosystem = 'golang'
  AND (parent_version ~ '^[0-9]' OR child_version ~ '^[0-9]');

-- dependency_resolution. Same grain, same rule.
UPDATE dependency_resolution c SET last_seen = GREATEST(c.last_seen, b.last_seen)
FROM dependency_resolution b
WHERE b.ecosystem = 'golang' AND b.version ~ '^[0-9]'
  AND c.ecosystem = 'golang' AND c.name = b.name
  AND c.bucket = b.bucket AND c.epoch = b.epoch
  AND c.version = 'v' || b.version;

DELETE FROM dependency_resolution b
WHERE b.ecosystem = 'golang' AND b.version ~ '^[0-9]'
  AND EXISTS (SELECT 1 FROM dependency_resolution c
              WHERE c.ecosystem = 'golang' AND c.name = b.name
                AND c.bucket = b.bucket AND c.epoch = b.epoch
                AND c.version = 'v' || b.version);

UPDATE dependency_resolution SET version = 'v' || version
WHERE ecosystem = 'golang' AND version ~ '^[0-9]';
