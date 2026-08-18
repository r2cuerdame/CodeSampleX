package serverstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const authoringAdvisoryLock int64 = 0x43535841555448 // "CSXAUTH"

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
		tag, err := c.Exec(ctx, `UPDATE authoring_sessions SET revoked_at=$2
			WHERE session_id=$1 AND revoked_at IS NULL`, sessionID, now)
		if err == nil {
			revoked = tag.RowsAffected() == 1
		}
		return err
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
