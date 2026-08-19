package serverstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

func (p *PG) FarmWorkers(ctx context.Context, since, now time.Time) ([]FarmWorker, error) {
	var out []FarmWorker
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT s.label, COALESCE(s.computer_name,''), s.issued_at,
			       s.last_refreshed_at, s.idle_expires_at,
			       (SELECT count(*) FROM authoring_drafts d
			         WHERE d.session_id=s.session_id AND d.created_at >= $1) AS drafts,
			       (SELECT count(*) FROM authoring_drafts d
			         JOIN samples sm ON sm.sample_id=d.sample_id AND NOT sm.quarantined
			         WHERE d.session_id=s.session_id AND d.created_at >= $1) AS published,
			       COALESCE((SELECT a.name||'@'||a.version||' '||
			                        CASE WHEN a.symbol='' THEN '(package)' ELSE a.symbol END
			                   FROM authoring_assignments a
			                  WHERE a.session_id=s.session_id AND a.sample_id IS NULL
			                  ORDER BY a.claimed_at DESC LIMIT 1),'') AS holding
			  FROM authoring_sessions s
			 WHERE s.revoked_at IS NULL AND s.idle_expires_at > $2
			 ORDER BY s.label ASC`, since, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w FarmWorker
			var lastRefresh *time.Time
			if err := rows.Scan(&w.Label, &w.ComputerName, &w.IssuedAt, &lastRefresh,
				&w.IdleExpiresAt, &w.Drafts, &w.Published, &w.Holding); err != nil {
				return err
			}
			if lastRefresh != nil {
				w.LastRefreshAt = *lastRefresh
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) FarmHealthNow(ctx context.Context, now time.Time) (FarmHealth, error) {
	health := FarmHealth{ReceiptsByOS: map[string]int{}}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		// One pass over the public manifests. The corpus is thousands of rows,
		// not millions, and this number is the reason the panel exists: it sat
		// at 37% for a day with nowhere to show itself.
		if err := c.QueryRow(ctx, `
			WITH pub AS (
			  SELECT s.sample_id,
			         (SELECT p FROM jsonb_array_elements_text(s.manifest->'packages') p LIMIT 1) AS purl,
			         COALESCE((SELECT string_agg(y,',' ORDER BY y)
			                     FROM jsonb_array_elements_text(s.manifest->'symbols') y),'') AS syms
			    FROM samples s WHERE NOT s.quarantined
			)
			SELECT (SELECT count(*) FROM pub),
			       (SELECT count(*) FROM (
			          SELECT 1 FROM pub GROUP BY purl, syms HAVING count(*) > 1) d)`).
			Scan(&health.PublicSamples, &health.DuplicateCoords); err != nil {
			return err
		}
		if err := c.QueryRow(ctx, `
			SELECT count(*) FROM authoring_assignments a
			  JOIN authoring_sessions s ON s.session_id=a.session_id
			 WHERE a.sample_id IS NULL
			   AND (s.revoked_at IS NOT NULL OR s.idle_expires_at <= $1)`, now).
			Scan(&health.StaleClaims); err != nil {
			return err
		}
		rows, err := c.Query(ctx, `
			SELECT LOWER(COALESCE(r.receipt->'environment'->>'os','')) AS os, count(*)
			  FROM receipts r JOIN samples s ON s.sample_id=r.sample_id AND NOT s.quarantined
			 WHERE r.contract_result='PASS'
			   AND COALESCE(r.receipt->'environment'->>'os','') <> ''
			 GROUP BY 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var os string
			var n int
			if err := rows.Scan(&os, &n); err != nil {
				return err
			}
			health.ReceiptsByOS[os] = n
		}
		return rows.Err()
	})
	return health, err
}

var _ FarmStatsStore = (*PG)(nil)
