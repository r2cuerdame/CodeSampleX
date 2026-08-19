package serverstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func scanAdminToken(row pgx.Row) (AdminTokenRow, error) {
	var out AdminTokenRow
	var expires, lastUsed, revoked *time.Time
	var lastIP *string
	err := row.Scan(&out.TokenHash, &out.TokenID, &out.Label, &out.IssuedAt,
		&expires, &lastUsed, &lastIP, &revoked)
	if expires != nil {
		out.ExpiresAt = *expires
	}
	if lastUsed != nil {
		out.LastUsedAt = *lastUsed
	}
	if lastIP != nil {
		out.LastUsedIP = *lastIP
	}
	if revoked != nil {
		out.RevokedAt = *revoked
	}
	return out, err
}

const adminTokenCols = `token_hash,token_id,label,issued_at,expires_at,last_used_at,last_used_ip,revoked_at`

func (p *PG) IssueAdminTokens(ctx context.Context, rows []AdminTokenRow) error {
	if len(rows) == 0 {
		return nil
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
		var live int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM admin_tokens WHERE revoked_at IS NULL`).Scan(&live); err != nil {
			return err
		}
		if live+len(rows) > MaxAdminTokens {
			return errors.New("serverstore: too many live admin tokens")
		}
		for _, row := range rows {
			if row.TokenHash == "" || row.TokenID == "" {
				return errors.New("serverstore: admin token needs a digest and an id")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO admin_tokens(
				token_hash,token_id,label,issued_at,expires_at)
				VALUES($1,$2,$3,$4,$5)`,
				row.TokenHash, row.TokenID, row.Label, row.IssuedAt, nullableTime(row.ExpiresAt)); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
}

// ResolveAdminToken validates and stamps in one statement. The stamp is not
// bookkeeping: an unlimited token has no expiry to watch, so its use is the
// only signal its owner has that somebody else holds it.
func (p *PG) ResolveAdminToken(ctx context.Context, tokenHash, ip string, now time.Time) (AdminTokenRow, bool, error) {
	var out AdminTokenRow
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		row, err := scanAdminToken(c.QueryRow(ctx, `
			UPDATE admin_tokens SET last_used_at=$3,last_used_ip=$4
			WHERE token_hash=$1 AND revoked_at IS NULL
			  AND (expires_at IS NULL OR expires_at > $2)
			RETURNING `+adminTokenCols, tokenHash, now, now, ip))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = row, true
		return nil
	})
	return out, found, err
}

func (p *PG) ListAdminTokens(ctx context.Context, limit int) ([]AdminTokenRow, error) {
	if limit < 1 || limit > MaxAdminTokens {
		limit = MaxAdminTokens
	}
	var out []AdminTokenRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT `+adminTokenCols+` FROM admin_tokens
			ORDER BY issued_at DESC,token_id ASC LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanAdminToken(rows)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) RevokeAdminToken(ctx context.Context, tokenID string, now time.Time) (bool, error) {
	revoked := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx,
			`UPDATE admin_tokens SET revoked_at=$2 WHERE token_id=$1 AND revoked_at IS NULL`,
			tokenID, now)
		if err == nil {
			revoked = tag.RowsAffected() == 1
		}
		return err
	})
	return revoked, err
}

var _ AdminTokenStore = (*PG)(nil)
