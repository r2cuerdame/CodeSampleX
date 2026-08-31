-- Remove "this release declares no dependencies" claims that nothing measured.
--
-- A verification wrote a dependency_resolution row for every package it
-- resolved that had no edges. Edges come only from an adapter implementing
-- EdgeScanner, and when none exists for an ecosystem, ResolvedEdges returns
-- nothing at all -- so the absence of a reader was written down as the absence
-- of dependencies. resolvedPackages does not need an EdgeScanner (golang reads
-- .csx-vendor/go-modules.json), so the two halves disagreed in silence.
--
-- The verifier no longer does this: ResolvedEdges reports whether anything
-- actually read the workspace and TreeBatches claims a leaf only when
-- something did. This removes what the old behaviour already wrote.
--
-- What goes:
--
--   golang, before the v0.1.77 farm rollout at 2026-08-31 03:50Z. golang had
--   no EdgeScanner until then (#146). Five rows on production, including
--   go.etcd.io/bbolt@v1.4.3, whose own go.mod names six direct requires --
--   cobra, pflag, testify, gofail, x/sync, x/sys -- measured against a real
--   `go mod download` while building that adapter.
--
--   maven, gem, pub, hex, composer at any time. No adapter reads their
--   lockfiles today, so no leaf claim about them was ever measured. None were
--   present on production when this was written; the clause is here because a
--   migration runs on databases nobody measured.
--
-- What stays: npm, pypi and cargo rows, and golang rows written after the
-- rollout. Those come from a scanner that ran, looked, and found no children
-- -- which is a measurement and the whole reason this table exists.
--
-- Deleting rather than flagging: a row here IS the claim. There is no state
-- for "we said this and should not have" that any reader of this table would
-- know how to render, and the coordinate returning to the open column is the
-- honest outcome -- the dependency axis genuinely has no answer for it.
--
-- dependency_edge is deliberately untouched. A missing scanner produces no
-- edges rather than wrong ones, so every edge in that table was read from a
-- lockfile by something that could read it.

DELETE FROM dependency_resolution
WHERE ecosystem = 'golang'
  AND last_seen < TIMESTAMPTZ '2026-08-31 03:50:00+00';

DELETE FROM dependency_resolution
WHERE ecosystem IN ('maven', 'gem', 'pub', 'hex', 'composer');
