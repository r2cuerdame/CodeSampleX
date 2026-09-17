package serverstore

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ListCLIObservations is the PostgreSQL half of CLIObservationStore.
//
// The prefix predicate on purl walks evidence_agg_target_idx (purl, symbol),
// so this reads the CLI rows and nothing else; the OS comes out of the same
// env_json the authoring query reads it from. Ordered so a truncated read is
// a deterministic one.
func (p *PG) ListCLIObservations(ctx context.Context, limit int) ([]CLIObservationRow, error) {
	if limit <= 0 || limit > CLIObservationReadLimit {
		limit = CLIObservationReadLimit
	}
	var out []CLIObservationRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT purl, symbol, lower(COALESCE(env_json->>'os', '')), result,
			       observation_count, COALESCE(last_seen, first_seen, now())
			  FROM evidence_agg
			 WHERE purl LIKE 'pkg:generic/cli/%'
			 ORDER BY purl, symbol, env_hash
			 LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row CLIObservationRow
			var lastSeen time.Time
			if err := rows.Scan(&row.PURL, &row.Symbol, &row.OS, &row.Result, &row.Count, &lastSeen); err != nil {
				return err
			}
			row.OS = strings.TrimSpace(row.OS)
			row.LastSeen = lastSeen.UTC()
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}
