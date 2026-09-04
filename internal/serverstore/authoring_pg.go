package serverstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const authoringAdvisoryLock int64 = 0x43535841555448 // "CSXAUTH"

// Fleet clients stop waiting after 15 seconds. PostgreSQL gets the first say
// at 10 seconds so it cancels the statement without discarding the pooled
// connection; the HTTP layer's 12-second context is the outer backstop.
const authoringExpansionStatementTimeout = 10 * time.Second

// authoringExpansionUnhurriedStatementTimeout bounds the background refresh
// of the candidate snapshot. Eight minutes is above the 249s measured on
// production under farm load and far below anything a poll would wait for;
// it exists so a wedged scan still releases its connection.
const authoringExpansionUnhurriedStatementTimeout = 8 * time.Minute

const authoringStatementDeadlineMargin = 250 * time.Millisecond

// pgStatementTimeout renders a duration the way statement_timeout accepts
// it: an integer number of milliseconds. time.Duration.String() is not that
// -- ten seconds is "10s" and passes, eight minutes is "8m0s" and PostgreSQL
// refuses it (SQLSTATE 22023). The unhurried refresh was the first caller to
// cross a minute, and the first to find out.
func pgStatementTimeout(d time.Duration) string {
	ms := d.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return strconv.FormatInt(ms, 10)
}

func authoringStatementTimeout(ctx context.Context, ceiling time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline) - authoringStatementDeadlineMargin
		if remaining < time.Millisecond {
			return time.Millisecond
		}
		if remaining < ceiling {
			return remaining
		}
	}
	return ceiling
}

func authoringPollStatementTimeout(ctx context.Context) time.Duration {
	if !isAuthoringPoll(ctx) {
		return 0
	}
	return authoringStatementTimeout(ctx, authoringExpansionStatementTimeout)
}

func (p *PG) IssueAuthoringSessions(ctx context.Context, rows []AuthoringSessionRow, now time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, authoringAdvisoryLock); err != nil {
			return err
		}
		// Retain a short audit tail, but expired/revoked rows never consume the
		// active cap and old private IP metadata is removed automatically.
		if _, err := tx.Exec(ctx, `DELETE FROM authoring_sessions
			WHERE idle_expires_at < $1 OR (revoked_at IS NOT NULL AND revoked_at < $1)`, now.Add(-30*24*time.Hour)); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM authoring_sessions
			WHERE revoked_at IS NULL AND idle_expires_at > $1`, now).Scan(&active); err != nil {
			return err
		}
		if active+len(rows) > MaxAuthoringSessions {
			return ErrAuthoringSessionLimit
		}
		for _, row := range rows {
			if _, err := tx.Exec(ctx, `INSERT INTO authoring_sessions(
				token_hash, session_id, label, model, reasoning, issued_at, idle_expires_at)
				VALUES($1,$2,$3,$4,$5,$6,$7)`, row.TokenHash, row.SessionID, row.Label,
				row.Model, row.Reasoning, row.IssuedAt, row.IdleExpiresAt); err != nil {
				return fmt.Errorf("insert authoring session: %w", err)
			}
		}
		return tx.Commit(ctx)
	})
}

func (p *PG) RotateAuthoringSession(ctx context.Context, sessionID, tokenHash string, now, idleExpiresAt time.Time) (AuthoringSessionRow, error) {
	var row AuthoringSessionRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `UPDATE authoring_sessions SET token_hash=$2,
			last_refreshed_at=NULL,idle_expires_at=$4,last_refresh_ip=NULL,computer_name=NULL
			WHERE session_id=$1 AND revoked_at IS NULL AND idle_expires_at > $3
			RETURNING token_hash,session_id,label,model,reasoning,issued_at,
				COALESCE(last_refreshed_at,'0001-01-01'::timestamptz),idle_expires_at,
				COALESCE(last_refresh_ip,''),COALESCE(computer_name,''),COALESCE(revoked_at,'0001-01-01'::timestamptz)`,
			sessionID, tokenHash, now, idleExpiresAt).Scan(
			&row.TokenHash, &row.SessionID, &row.Label, &row.Model, &row.Reasoning,
			&row.IssuedAt, &row.LastRefreshAt, &row.IdleExpiresAt, &row.LastRefreshIP, &row.ComputerName, &row.RevokedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthoringSessionRow{}, ErrAuthoringSessionMissing
	}
	return row, err
}

func (p *PG) RefreshAuthoringSession(ctx context.Context, tokenHash, ip, computerName string, now, idleExpiresAt time.Time) (AuthoringSessionRow, error) {
	var row AuthoringSessionRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		// Repeated calls inside five minutes are successful no-ops. This keeps
		// an accidentally tight worker loop from turning one valid token into
		// unbounded PostgreSQL writes while the documented 45-minute cadence
		// still extends the one-hour idle deadline normally.
		return c.QueryRow(ctx, `UPDATE authoring_sessions SET
			last_refreshed_at = CASE WHEN last_refreshed_at IS NULL OR last_refreshed_at <= $3 THEN $2 ELSE last_refreshed_at END,
			idle_expires_at = CASE WHEN last_refreshed_at IS NULL OR last_refreshed_at <= $3 THEN $4 ELSE idle_expires_at END,
			last_refresh_ip = CASE WHEN last_refreshed_at IS NULL OR last_refreshed_at <= $3 THEN NULLIF($5,'') ELSE last_refresh_ip END
			,computer_name = CASE WHEN last_refreshed_at IS NULL OR last_refreshed_at <= $3 THEN NULLIF($6,'') ELSE computer_name END
			WHERE token_hash=$1 AND revoked_at IS NULL AND idle_expires_at > $2
			RETURNING token_hash,session_id,label,model,reasoning,issued_at,
				COALESCE(last_refreshed_at,'0001-01-01'::timestamptz),idle_expires_at,
				COALESCE(last_refresh_ip,''),COALESCE(computer_name,''),COALESCE(revoked_at,'0001-01-01'::timestamptz)`,
			tokenHash, now, now.Add(-5*time.Minute), idleExpiresAt, ip, computerName).Scan(
			&row.TokenHash, &row.SessionID, &row.Label, &row.Model, &row.Reasoning,
			&row.IssuedAt, &row.LastRefreshAt, &row.IdleExpiresAt, &row.LastRefreshIP, &row.ComputerName, &row.RevokedAt)
	})
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AuthoringSessionRow{}, err
	}
	var expired bool
	err = p.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authoring_sessions
			WHERE token_hash=$1 AND revoked_at IS NULL AND idle_expires_at <= $2)`, tokenHash, now).Scan(&expired)
	})
	if err != nil {
		return AuthoringSessionRow{}, err
	}
	if expired {
		return AuthoringSessionRow{}, ErrAuthoringSessionExpired
	}
	return AuthoringSessionRow{}, ErrAuthoringSessionMissing
}

func (p *PG) RevokeAuthoringSession(ctx context.Context, sessionID string, now time.Time) (bool, error) {
	var revoked bool
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
		tag, err := tx.Exec(ctx, `UPDATE authoring_sessions SET revoked_at=$2
			WHERE session_id=$1 AND revoked_at IS NULL`, sessionID, now)
		if err != nil {
			return err
		}
		revoked = tag.RowsAffected() == 1
		if !revoked {
			return tx.Commit(ctx)
		}
		// Hand back whatever it was holding. The lease runs 24 hours and the
		// assignment key does not record who holds it, so a claim left behind
		// takes its coordinates off the board for every other worker for a day.
		// Work already submitted keeps its row: that one carries a sample_id.
		if _, err := tx.Exec(ctx, `DELETE FROM authoring_assignments
			WHERE session_id=$1 AND sample_id IS NULL`, sessionID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return revoked, err
}

func (p *PG) ListAuthoringSessions(ctx context.Context, now time.Time, limit int) ([]AuthoringSessionRow, error) {
	if limit < 1 || limit > MaxAuthoringSessions {
		limit = MaxAuthoringSessions
	}
	var out []AuthoringSessionRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT token_hash,session_id,label,model,reasoning,issued_at,
			COALESCE(last_refreshed_at,'0001-01-01'::timestamptz),idle_expires_at,
			COALESCE(last_refresh_ip,''),COALESCE(computer_name,''),COALESCE(revoked_at,'0001-01-01'::timestamptz)
			FROM authoring_sessions
			WHERE revoked_at IS NULL AND idle_expires_at > $1
			ORDER BY issued_at DESC, session_id ASC LIMIT $2`, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row AuthoringSessionRow
			if err := rows.Scan(&row.TokenHash, &row.SessionID, &row.Label, &row.Model, &row.Reasoning,
				&row.IssuedAt, &row.LastRefreshAt, &row.IdleExpiresAt, &row.LastRefreshIP, &row.ComputerName, &row.RevokedAt); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

var _ AuthoringSessionStore = (*PG)(nil)

func (p *PG) SaveAuthoringDraft(ctx context.Context, row AuthoringDraftRow) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `INSERT INTO authoring_drafts(
			sample_id,session_id,worker_label,manifest,local_status,created_at,updated_at)
			VALUES($1,$2,$3,$4::jsonb,$5,$6,$7)
			ON CONFLICT(sample_id) DO UPDATE SET
				session_id=EXCLUDED.session_id,worker_label=EXCLUDED.worker_label,
				manifest=EXCLUDED.manifest,local_status=EXCLUDED.local_status,
				updated_at=EXCLUDED.updated_at`, row.SampleID, row.SessionID, row.WorkerLabel,
			row.ManifestJSON, row.LocalStatus, row.CreatedAt, row.UpdatedAt)
		return err
	})
}

func (p *PG) ListAuthoringDrafts(ctx context.Context, limit int) ([]AuthoringDraftRow, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	var out []AuthoringDraftRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT d.sample_id,d.session_id,d.worker_label,d.manifest::text,
			d.local_status,
			CASE WHEN s.status='CROSS_PASS' AND NOT s.quarantined THEN 'CROSS_PASS'
			     WHEN EXISTS(SELECT 1 FROM receipts r WHERE r.sample_id=d.sample_id AND r.contract_result='FAIL') THEN 'CROSS_FAIL'
			     ELSE 'PENDING' END,
			d.created_at,d.updated_at FROM authoring_drafts d
			LEFT JOIN samples s ON s.sample_id=d.sample_id
			ORDER BY d.updated_at DESC,d.sample_id ASC LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row AuthoringDraftRow
			if err := rows.Scan(&row.SampleID, &row.SessionID, &row.WorkerLabel, &row.ManifestJSON,
				&row.LocalStatus, &row.VerificationStatus, &row.CreatedAt, &row.UpdatedAt); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

func scanAuthoringWork(row pgx.Row) (AuthoringWorkRow, error) {
	var work AuthoringWorkRow
	var sampleID *string
	err := row.Scan(&work.Ecosystem, &work.Name, &work.Version, &work.Symbol, &work.Asks, &work.Kind, &work.Score,
		&work.SessionID, &work.ClaimedAt, &work.LeaseExpiresAt, &sampleID)
	if sampleID != nil {
		work.SampleID = *sampleID
	}
	return work, err
}

func (p *PG) ListAuthoringExpansionCandidates(ctx context.Context, limit int) ([]WantedRow, error) {
	return p.listAuthoringExpansionCandidates(ctx, limit, authoringExpansionStatementTimeout, false)
}

// ListAuthoringExpansionCandidatesUnhurried runs the same read under the
// refresh budget AND on one core. The host has two; the parallel plan takes
// both for the ~4 minutes the read needs on production, and for that whole
// window the website shares the box with nothing. Measured 2026-09-02 with
// parallelism off the read took 300s instead of 249s -- a fifth slower, for
// a core the site keeps. A refresh answers no caller; it has no claim on
// the second core.
func (p *PG) ListAuthoringExpansionCandidatesUnhurried(ctx context.Context, limit int) ([]WantedRow, error) {
	return p.listAuthoringExpansionCandidates(ctx, limit, authoringExpansionUnhurriedStatementTimeout, true)
}

// authoringExpansionCandidatesSQL is the candidate query on its own, so a
// test can EXPLAIN the statement the store actually runs rather than a
// copy of it. $1 limit, $2 sibling versions per package, $3 dependency
// closure cap, $4 resolve weight.
var authoringExpansionCandidatesSQL = `
			WITH ` + authoringCoverageCTE + `, verified_symbols AS MATERIALIZED (
				SELECT DISTINCT package.value AS purl,symbol.value AS symbol
				FROM verified_samples s
				CROSS JOIN LATERAL jsonb_array_elements_text(
				  CASE WHEN jsonb_typeof(s.manifest->'packages')='array' THEN s.manifest->'packages' ELSE '[]'::jsonb END
				) AS package(value)
				CROSS JOIN LATERAL jsonb_array_elements_text(
				  CASE WHEN jsonb_typeof(s.manifest->'symbols')='array' THEN s.manifest->'symbols' ELSE '[]'::jsonb END
				) AS symbol(value)
			), dependency_closure AS MATERIALIZED (
				-- The ranked, bounded view of dependency_open: at most
				-- $2 releases of one library and at most $3 rows overall,
				-- demand first throughout. The bounds and the ordering are
				-- explained in dependencyclosure.go, and
				-- (*Fake).dependencyClosure is the other half of them --
				-- TestIntegrationDependencyClosureParity holds the two
				-- together.
				SELECT ecosystem,child_name AS name,child_version AS version,projects
				FROM (
					SELECT o.ecosystem,o.child_name,o.child_version,
					       COUNT(DISTINCT o.bucket||o.epoch) AS projects,
					       ROW_NUMBER() OVER (
					         PARTITION BY o.ecosystem,o.child_name
					         ORDER BY COUNT(DISTINCT o.bucket||o.epoch) DESC,
					                  o.child_version DESC) AS version_rank
					FROM dependency_open o
					GROUP BY o.ecosystem,o.child_name,o.child_version
				) ranked_dependency
				WHERE version_rank <= $2
				ORDER BY projects DESC,version DESC,ecosystem,name
				LIMIT $3
			), resolve_demand AS MATERIALIZED (
				-- R2C-90. The distinct project-days that resolved each exact
				-- release, read from the resolved graph rather than from
				-- anybody's manifest. A carried sighting count says a machine
				-- mentioned the package; this says a machine installed this
				-- release, and authoringResolveWeight is what one of them is
				-- worth beside a chosen sighting.
				--
				-- This is the whole graph, not dependency_open: the branch
				-- above asks which children nobody has REPORTED, and the
				-- ranking question here is about the ones they have.
				SELECT 'pkg:'||ecosystem||'/'||
				         CASE WHEN left(child_name,1)='@'
				              THEN '%40'||substring(child_name from 2)
				              ELSE child_name END||'@'||child_version AS purl,
				       COUNT(DISTINCT bucket||epoch) AS projects
				FROM dependency_edge
				GROUP BY 1
			), finding_versions AS MATERIALIZED (
				-- Every current cluster, expanded to one row per version it
				-- names, BEFORE the join to packages. Written inline, the
				-- LATERAL expansion made the planner answer that join once per
				-- expanded row: an index scan on packages and a four-row hash
				-- rebuilt 159,601 times, 0.5ms each, 84 of the 101 seconds the
				-- FINDING branch cost on production (#173). Materialised, the
				-- expansion is one stream and packages is hashed once. Only the
				-- columns the branch needs travel: env_summary is a wide jsonb
				-- and carrying it through 160k rows would cost what it saves.
				SELECT fc.ecosystem,fc.package_name,fc.symbol,fc.error_fp,fc.observation_count,
				       COALESCE(NULLIF(fc.env_summary->>'os',''),'') AS env_os,
				       version.value AS version
				FROM failure_clusters fc
				CROSS JOIN LATERAL jsonb_array_elements_text(
				  CASE WHEN jsonb_typeof(fc.versions)='array' THEN fc.versions ELSE '[]'::jsonb END
				) AS version(value)
				WHERE ` + CurrentFailureClusterPredicateSQL + `
			), candidates AS (
				SELECT p.purl,p.ecosystem,p.name,p.version,fv.symbol,
				       fv.observation_count AS score,'FINDING'::text AS kind,0 AS source_rank,p.last_seen,
				       COALESCE(NULLIF(fv.env_os,''),(
				         SELECT e2.env_json->>'os' FROM evidence_agg e2
				         WHERE e2.purl=p.purl AND e2.symbol=fv.symbol AND e2.error_fp=fv.error_fp
				         ORDER BY e2.observation_count DESC LIMIT 1
				       ),'') AS target_os
				FROM finding_versions fv
				JOIN packages p ON p.ecosystem=fv.ecosystem AND p.name=fv.package_name
				  AND p.version=fv.version
				WHERE p.version<>'' AND p.publicness='PUBLIC'
				UNION ALL
				SELECT p.purl,p.ecosystem,p.name,p.version,e.symbol,
				       SUM(e.observation_count * CASE WHEN e.direct THEN 1000 ELSE 1 END) AS score,
				       'EXPANSION'::text AS kind,2 AS source_rank,p.last_seen,
				       COALESCE(e.env_json->>'os','') AS target_os
				FROM packages p
				JOIN evidence_agg e ON e.purl=p.purl
				WHERE p.version<>'' AND p.publicness='PUBLIC'
				  AND NOT EXISTS (
				        SELECT 1 FROM verified_symbols v
				        WHERE v.purl=p.purl AND v.symbol=e.symbol)
				GROUP BY p.purl,p.ecosystem,p.name,p.version,e.symbol,p.last_seen,target_os
				UNION ALL
				SELECT p.purl,p.ecosystem,p.name,p.version,''::text AS symbol,
				       SUM(e.observation_count * CASE WHEN e.direct THEN 1000 ELSE 1 END)
				         + COALESCE(MAX(rd.projects),0) * $4 AS score,
				       'EXPANSION'::text AS kind,1 AS source_rank,p.last_seen,
				       LOWER(COALESCE(e.env_json->>'os','')) AS target_os
				-- Package-level work is for an environment that has evidence but no
				-- proof yet. It used to be generated FROM verified_package_targets --
				-- the pairs already proven -- instead of excluded BY them, and the
				-- symbol filter below never applies to a package-level row. So a
				-- package proven on linux was offered for linux again, forever. In
				-- production that made 201 verified samples for one coordinate, all
				-- on the same OS, and left 37% of the corpus redundant.
				FROM packages p
				JOIN evidence_agg e ON e.purl=p.purl
				LEFT JOIN resolve_demand rd ON rd.purl=p.purl
				WHERE p.version<>'' AND p.publicness='PUBLIC'
				  AND NOT EXISTS (
				        SELECT 1 FROM verified_packages v
				        WHERE v.purl=p.purl)
				  AND LOWER(COALESCE(e.env_json->>'os',''))<>''
				  AND NOT EXISTS (
				        SELECT 1 FROM verified_package_targets t
				        WHERE t.purl=p.purl
				          AND t.target_os=LOWER(COALESCE(e.env_json->>'os','')))
				GROUP BY p.purl,p.ecosystem,p.name,p.version,p.last_seen,target_os
				UNION ALL
				-- Sibling versions of a package already proven at some version, but
				-- carrying no verified sample of their own. Every branch above reaches
				-- a version only through an evidence row keyed by the exact purl, so a
				-- release nobody has measured can never become work and its column in
				-- the matrix stays blank however long the workers run. Score 0 ranks
				-- these last on merit; version_depth below is what lifts them into
				-- reach. target_os comes from where the package was already proven, so
				-- the row is claimable by a verifier that can actually execute it.
				SELECT p.purl,p.ecosystem,p.name,p.version,''::text AS symbol,
				       0::bigint AS score,'EXPANSION'::text AS kind,4 AS source_rank,p.last_seen,
				       sibling.target_os
				-- Newest few per package only. Every sibling is a first job and so
				-- lands at version_depth 1; uncapped, one long release history fills
				-- the entire window with score-0 rows and pushes every other
				-- package's real work past the LIMIT. The ordering is last_seen then
				-- version as a STRING -- not semver, which SQL cannot express and
				-- which puts 7.0.3 above 14.0.1. That is acceptable because this is
				-- a safety cap rather than a ranking, and because the Fake caps by
				-- the identical rule: a bound the two stores disagree about would be
				-- worse than one that picks an imperfect six.
				FROM (
				  SELECT purl,ecosystem,name,version,last_seen,
				         ROW_NUMBER() OVER (PARTITION BY ecosystem,name
				                            ORDER BY last_seen DESC,version DESC) AS sibling_rank
				  FROM packages
				  WHERE version<>'' AND publicness='PUBLIC'
				    AND NOT EXISTS (SELECT 1 FROM verified_packages v WHERE v.purl=packages.purl)
				) p
				JOIN (
				  SELECT DISTINCT pk.ecosystem,pk.name,t.target_os
				  FROM verified_package_targets t
				  JOIN packages pk ON pk.purl=t.purl
				) sibling ON sibling.ecosystem=p.ecosystem AND sibling.name=p.name
				WHERE p.sibling_rank <= $2
				UNION ALL
				-- The dependency closure, ranked between the exact holes
				-- evidence points at and the version breadth nobody has asked
				-- for. target_os is empty on purpose: a resolved edge records
				-- what a lockfile contained, not a platform anybody ran it on,
				-- so it pins nothing and any verifier that can build the
				-- ecosystem may answer it.
				--
				-- last_seen is the zero timestamp rather than the edge's,
				-- because Go's zero time is what the Fake carries here and an
				-- ordering term the two stores disagree about is how the two
				-- implementations of this query drifted apart before.
				SELECT 'pkg:'||d.ecosystem||'/'||
				         CASE WHEN left(d.name,1)='@'
				              THEN '%40'||substring(d.name from 2)
				              ELSE d.name END||'@'||d.version AS purl,
				       d.ecosystem,d.name,d.version,''::text AS symbol,
				       d.projects AS score,'DEPENDENCY'::text AS kind,3 AS source_rank,
				       '0001-01-01 00:00:00+00'::timestamptz AS last_seen,
				       ''::text AS target_os
				FROM dependency_closure d
			), ranked AS (
				SELECT DISTINCT ON(ecosystem,name,version,symbol,target_os)
				       purl,ecosystem,name,version,symbol,score,kind,source_rank,last_seen,target_os
				FROM candidates
				ORDER BY ecosystem,name,version,symbol,target_os,source_rank,score DESC
			), in_flight AS MATERIALIZED (
				-- Coordinates a worker has already answered and is waiting on an
				-- independent verification for. Nothing about "proven" changes until
				-- that verification lands, so without this the coordinate stayed a
				-- candidate and the next worker claimed it minutes after the first
				-- submitted. One worker rarely raced itself; two produced six
				-- duplicate coordinates in six hours. Work in flight is work done.
				-- The appended "" holds the PACKAGE-LEVEL coordinate (purl,'')
				-- in flight for every draft, not only a symbol-less one. The
				-- LEFT JOIN it replaces yielded (purl,'') only when the symbols
				-- array was empty — while the fake marked it for every draft —
				-- so a purl whose symbol-scoped draft was awaiting verification
				-- was re-offered as package-level EXPANSION work minutes later,
				-- and every test passed on the fake while production duplicated.
				SELECT DISTINCT package.value AS purl,
				       symbol.value AS symbol
				FROM authoring_drafts d
				JOIN samples s ON s.sample_id=d.sample_id AND s.status='DRAFT'
				CROSS JOIN LATERAL jsonb_array_elements_text(
				  CASE WHEN jsonb_typeof(s.manifest->'packages')='array' THEN s.manifest->'packages' ELSE '[]'::jsonb END
				) AS package(value)
				CROSS JOIN LATERAL jsonb_array_elements_text(
				  (CASE WHEN jsonb_typeof(s.manifest->'symbols')='array' THEN s.manifest->'symbols' ELSE '[]'::jsonb END)
				  || '[""]'::jsonb
				) AS symbol(value)
			), fresh AS (
				-- The already-answered symbols drop out BEFORE depth is counted, or a
				-- version would be charged for work nobody can be given.
				--
				-- "symbol='' OR ..." exempted symbol-less rows from the check
				-- entirely, and package-level observations produce exactly those.
				-- So an answered package stayed a candidate forever and the
				-- expansion branch reissued it: production wrote eight samples
				-- for three@0.185.1 in twenty-eight minutes, each a placeholder
				-- goal with no symbols, bounded by nothing but the corpus
				-- running out of packages.
				--
				-- A symbol-less EXPANSION asks "has this package been answered",
				-- so it is checked against verified_packages. FINDING and WANTED
				-- are not: a finding is about a failure, and 77% of production's
				-- clusters carry no symbol, so filtering them this way stopped
				-- the work rather than the loop.
				SELECT * FROM ranked c
				WHERE (CASE
				    WHEN c.symbol<>'' THEN NOT EXISTS (
				      SELECT 1 FROM verified_symbols v WHERE v.purl=c.purl AND v.symbol=c.symbol)
				    WHEN c.kind IN ('EXPANSION','DEPENDENCY') THEN NOT EXISTS (
				      SELECT 1 FROM verified_packages v WHERE v.purl=c.purl)
				    ELSE true END)
				  AND NOT EXISTS (
					SELECT 1 FROM in_flight f WHERE f.purl=c.purl AND f.symbol=c.symbol)
			), claimable AS (
				-- Coordinates an assignment already answered. The claim
				-- inserts ON CONFLICT DO NOTHING against a key nothing
				-- deletes once a sample is attached, so a row here can never
				-- be handed out again -- and it sorts by the observation
				-- count that made it worth answering first, which puts it at
				-- the TOP of a finite window rather than the bottom.
				--
				-- Package-level EXPANSION and DEPENDENCY hand their row back
				-- on submission and a symbol-bearing row is already filtered
				-- by verified_symbols, so what accumulates is the symbol-less
				-- FINDING. Production held 407 of them on 2026-08-23; 141
				-- were inside the 200-row window, 56 more were npm platform
				-- builds the handler drops, and THREE rows were claimable.
				-- Authoring went from 45 handouts an hour to zero for five
				-- hours with 1,810 coverage holes on the board.
				SELECT c.* FROM fresh c
				WHERE NOT EXISTS (
				  SELECT 1 FROM authoring_assignments a
				  WHERE a.ecosystem=c.ecosystem AND a.name=c.name
				    AND a.version=c.version AND a.symbol=c.symbol
				    AND a.sample_id IS NOT NULL)
			), spread AS (
				-- How many jobs this version has already been offered higher up the
				-- merit order. Ordering by it first means every version earns its
				-- first job before any version earns its second.
				SELECT *, ROW_NUMBER() OVER (
				         PARTITION BY ecosystem,name,version
				         ORDER BY source_rank,score DESC,last_seen DESC,symbol) AS version_depth
				FROM claimable
			)
			SELECT ecosystem,name,version,symbol,score,kind,target_os
			FROM spread
			-- Depth first: it is what stops one package with a long release
			-- history filling the window. Then USAGE -- authoring follows
			-- what people actually run.
			--
			-- Usage is weighted by whether the reporter CHOSE the package.
			-- Raw volume ranked the shadow of popular libraries: a transitive
			-- dependency pulled into a thousand lockfiles beat a package
			-- fifty developers listed themselves, and the queue wrote samples
			-- for the shadow. A direct sighting counts a thousand carried
			-- ones, which is the ratio between "somebody wanted this" and
			-- "somebody received this".
			--
			-- A "linux first" term used to lead this. It was arbitrary when
			-- written and became actively wrong: every observation this
			-- network holds is recorded on Windows, so preferring Linux
			-- pushed the entire measured demand behind work nobody asked for.
			ORDER BY version_depth,
			         score DESC,source_rank,last_seen DESC,ecosystem,name,version,symbol
			LIMIT $1`

func (p *PG) listAuthoringExpansionCandidates(ctx context.Context, limit int, statementTimeout time.Duration, oneCore bool) ([]WantedRow, error) {
	if limit < 1 {
		return nil, nil
	}
	if limit > 200 {
		limit = 200
	}
	var out []WantedRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		statementTimeout = authoringStatementTimeout(ctx, statementTimeout)
		// The candidate query is large enough to cross PostgreSQL's JIT cost
		// threshold on the production corpus. A timed-out fleet poll then
		// returned to its caller while LLVM compilation kept the backend busy
		// for another minute, because that compilation did not observe the
		// pending statement interrupt promptly. Keep this transaction on the
		// ordinary executor: it is both faster for this short-lived read and
		// lets statement_timeout remain an actual upper bound.
		if _, err := tx.Exec(ctx, `SELECT set_config('statement_timeout',$1,true), set_config('jit','off',true)`, pgStatementTimeout(statementTimeout)); err != nil {
			return err
		}
		if oneCore {
			// Transaction-local like the two above, so the pooled connection
			// goes back to the shipped setting on commit or rollback.
			if _, err := tx.Exec(ctx, `SELECT set_config('max_parallel_workers_per_gather','0',true)`); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, authoringExpansionCandidatesSQL, limit, authoringSiblingVersionsPerPackage, authoringDependencyClosureCap,
			authoringResolveWeight)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row WantedRow
			if err := rows.Scan(&row.Ecosystem, &row.Name, &row.Version, &row.Symbol, &row.Score, &row.Kind, &row.TargetOS); err != nil {
				return err
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return tx.Commit(ctx)
	})
	return out, err
}

func (p *PG) ClaimAuthoringWork(ctx context.Context, sessionID string, candidates []WantedRow, now, leaseExpiresAt time.Time) (AuthoringWorkRow, bool, error) {
	var claimed AuthoringWorkRow
	found := false
	eligible := make(map[[4]string]struct{}, len(candidates))
	for _, candidate := range candidates {
		eligible[authoringWorkKey(candidate.Ecosystem, candidate.Name, candidate.Version, candidate.Symbol)] = struct{}{}
	}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "authoring-work\x1f"+sessionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM authoring_assignments WHERE sample_id IS NULL AND lease_expires_at <= $1`, now); err != nil {
			return err
		}
		// Revoking is not the only way a session stops. One that simply quits
		// refreshing idles out in an hour while its claim runs for a day, and
		// the assignment key does not record who holds it — so the coordinate is
		// off the board for everybody until the lease expires.
		if _, err := tx.Exec(ctx, `DELETE FROM authoring_assignments a
			USING authoring_sessions s
			WHERE a.session_id = s.session_id AND a.sample_id IS NULL
			  AND (s.revoked_at IS NOT NULL OR s.idle_expires_at <= $1)`, now); err != nil {
			return err
		}
		// One query for every attempt ledger in the candidate window. Asking
		// per candidate would be up to four hundred round trips inside this
		// transaction on an endpoint the whole fleet polls constantly.
		ledgers, err := loadAuthoringLedgers(ctx, tx, candidates)
		if err != nil {
			return err
		}
		claimed, err = scanAuthoringWork(tx.QueryRow(ctx, `SELECT ecosystem,name,version,symbol,asks,kind,score,
			session_id,claimed_at,lease_expires_at,sample_id FROM authoring_assignments
			WHERE session_id=$1 AND sample_id IS NULL AND lease_expires_at>$2`, sessionID, now))
		if err == nil {
			key := authoringWorkKey(claimed.Ecosystem, claimed.Name, claimed.Version, claimed.Symbol)
			_, stillEligible := eligible[key]
			// A live worker refreshing a hopeless claim used to hold the slot
			// until its 24-hour lease ran out: reclaim released only claims
			// whose SESSION had died, and this one had not.
			ledger := ledgers[key]
			if stillEligible && (ledger == nil || !ledger.barred(sessionID, now)) {
				if ledger == nil || !now.Before(ledger.LastAttemptAt.Add(AuthoringAttemptDebounce)) {
					if err := noteAuthoringHandout(ctx, tx, ledger, claimed, sessionID, now); err != nil {
						return err
					}
				}
				found = true
				return tx.Commit(ctx)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM authoring_assignments
				WHERE ecosystem=$1 AND name=$2 AND version=$3 AND symbol=$4
				  AND session_id=$5 AND sample_id IS NULL`, claimed.Ecosystem, claimed.Name,
				claimed.Version, claimed.Symbol, sessionID); err != nil {
				return err
			}
			err = pgx.ErrNoRows
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		for _, candidate := range candidates {
			ledger := ledgers[authoringWorkKey(candidate.Ecosystem, candidate.Name, candidate.Version, candidate.Symbol)]
			if ledger != nil && ledger.barred(sessionID, now) {
				continue
			}
			kind := candidate.Kind
			if kind == "" {
				kind = "WANTED"
			}
			claimed, err = scanAuthoringWork(tx.QueryRow(ctx, `INSERT INTO authoring_assignments(
				ecosystem,name,version,symbol,asks,kind,score,session_id,claimed_at,lease_expires_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
				ON CONFLICT(ecosystem,name,version,symbol) DO NOTHING
				RETURNING ecosystem,name,version,symbol,asks,kind,score,session_id,claimed_at,lease_expires_at,sample_id`,
				candidate.Ecosystem, candidate.Name, candidate.Version, candidate.Symbol, candidate.Asks,
				kind, candidate.Score, sessionID, now, leaseExpiresAt))
			if err == nil {
				if err := noteAuthoringHandout(ctx, tx, ledger, claimed, sessionID, now); err != nil {
					return err
				}
				found = true
				return tx.Commit(ctx)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		return tx.Commit(ctx)
	})
	return claimed, found, err
}

func (p *PG) AuthoringWorkForSubmission(ctx context.Context, sessionID, sampleID string, now time.Time) (AuthoringWorkRow, bool, error) {
	var work AuthoringWorkRow
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var err error
		work, err = scanAuthoringWork(c.QueryRow(ctx, `SELECT ecosystem,name,version,symbol,asks,kind,score,
			session_id,claimed_at,lease_expires_at,sample_id FROM authoring_assignments
			WHERE session_id=$1 AND ((sample_id IS NULL AND lease_expires_at>$2) OR sample_id=$3)
			ORDER BY CASE WHEN sample_id=$3 THEN 0 ELSE 1 END LIMIT 1`, sessionID, now, sampleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	return work, found, err
}

func (p *PG) AttachAuthoringWorkSample(ctx context.Context, sessionID string, work AuthoringWorkRow, sampleID string, now time.Time) (bool, error) {
	attached := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		// A package-level claim is keyed (ecosystem,name,version,'') whichever
		// symbol the writer ended up choosing, so leaving the row behind with a
		// sample id would take that coordinate off the board permanently. A
		// DEPENDENCY claim has the same shape and the same reason: it asks about
		// the release, not about one symbol in it.
		if (work.Kind == "EXPANSION" || work.Kind == "DEPENDENCY") && work.Symbol == "" {
			tag, err := tx.Exec(ctx, `DELETE FROM authoring_assignments
				WHERE ecosystem=$1 AND name=$2 AND version=$3 AND symbol=''
				  AND session_id=$4 AND sample_id IS NULL AND lease_expires_at>$5`,
				work.Ecosystem, work.Name, work.Version, sessionID, now)
			if err != nil {
				return err
			}
			attached = tag.RowsAffected() == 1
		} else {
			tag, err := tx.Exec(ctx, `UPDATE authoring_assignments SET sample_id=$7,completed_at=$8
				WHERE ecosystem=$1 AND name=$2 AND version=$3 AND symbol=$4
				  AND session_id=$5 AND sample_id IS NULL AND lease_expires_at>$6`,
				work.Ecosystem, work.Name, work.Version, work.Symbol, sessionID, now, sampleID, now)
			if err != nil {
				return err
			}
			attached = tag.RowsAffected() == 1
		}
		// The coordinate answered the question. Its history stays for the
		// audit trail; the counters that withhold work do not.
		if attached {
			ledger, err := loadAuthoringLedger(ctx, tx, work.Ecosystem, work.Name, work.Version, work.Symbol)
			if err != nil {
				return err
			}
			if ledger != nil {
				ledger.authored(sessionID, now)
				if err := saveAuthoringLedger(ctx, tx, ledger, now); err != nil {
					return err
				}
			}
		}
		return tx.Commit(ctx)
	})
	return attached, err
}
