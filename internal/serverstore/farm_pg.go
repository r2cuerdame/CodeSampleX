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
	health := FarmHealth{ReceiptsByOS: map[string]int{}, QuarantinedByReason: map[string]int{},
		WithheldByReason: map[string]int{}}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		// One pass over the public manifests. The corpus is thousands of rows,
		// not millions, and this number is the reason the panel exists: it sat
		// at 37% for a day with nowhere to show itself.
		if err := c.QueryRow(ctx, `
			WITH pub AS (
			  -- jsonb_array_elements_text raises "cannot extract elements
			  -- from a scalar" on jsonb null, and a manifest with no packages
			  -- serializes to null rather than SQL NULL -- so one such sample
			  -- would fail this whole panel instead of skipping itself.
			  SELECT s.sample_id,
			         (SELECT p FROM jsonb_array_elements_text(
			              CASE WHEN jsonb_typeof(s.manifest->'packages') = 'array'
			                   THEN s.manifest->'packages' ELSE '[]'::jsonb END) p LIMIT 1) AS purl,
			         COALESCE((SELECT string_agg(y,',' ORDER BY y)
			                     FROM jsonb_array_elements_text(
			              CASE WHEN jsonb_typeof(s.manifest->'symbols') = 'array'
			                   THEN s.manifest->'symbols' ELSE '[]'::jsonb END) y),'') AS syms
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
		// Why things were withdrawn, not just how many. The reason was always
		// recorded and never read.
		reasons, err := c.Query(ctx, `
			SELECT COALESCE(quarantine_reason,''), count(*)
			  FROM samples WHERE quarantined GROUP BY 1 ORDER BY 2 DESC LIMIT 32`)
		if err != nil {
			return err
		}
		for reasons.Next() {
			var reason string
			var n int
			if err := reasons.Scan(&reason, &n); err != nil {
				reasons.Close()
				return err
			}
			health.QuarantinedByReason[reason] = n
		}
		if err := reasons.Err(); err != nil {
			reasons.Close()
			return err
		}
		reasons.Close()

		// What the authoring queue is refusing to offer, read from the same
		// predicate the picker uses. An operator reading "0 withheld" while
		// the fleet is being refused work is the failure the attempt ledger
		// exists to make visible.
		//
		// The total is counted on its own rather than summed from the reason
		// rows below: those are capped, and a capped sum reported as a total
		// is the quiet kind of wrong this panel keeps being fixed for.
		if err := c.QueryRow(ctx, `SELECT count(*) FROM authoring_attempts
			 WHERE quarantined_at IS NOT NULL AND (reopens_at IS NULL OR reopens_at > $1)`, now).
			Scan(&health.WithheldCoordinates); err != nil {
			return err
		}
		withheld, err := c.Query(ctx, `
			SELECT COALESCE(ledger->>'quarantineReason',''), count(*)
			  FROM authoring_attempts
			 WHERE quarantined_at IS NOT NULL AND (reopens_at IS NULL OR reopens_at > $1)
			 GROUP BY 1 ORDER BY 2 DESC LIMIT 32`, now)
		if err != nil {
			return err
		}
		for withheld.Next() {
			var reason string
			var n int
			if err := withheld.Scan(&reason, &n); err != nil {
				withheld.Close()
				return err
			}
			health.WithheldByReason[reason] = n
		}
		if err := withheld.Err(); err != nil {
			withheld.Close()
			return err
		}
		withheld.Close()

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

// FarmCoverage measures the compatibility map against itself: for every
// (os, ecosystem) the network has seen used, how much of it has actually been
// proven there.
//
// Observations arrive from developer machines and proofs from containers, so
// the two axes can disagree entirely -- and did: every observation recorded
// was windows while every receipt was linux. Counting them together hid that
// for months behind a single healthy-looking coverage number.
func (p *PG) FarmCoverage(ctx context.Context) ([]FarmAxisCoverage, error) {
	var out []FarmAxisCoverage
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			WITH observed AS (
			  SELECT DISTINCT LOWER(BTRIM(e.env_json->>'os')) AS os, p.ecosystem, p.purl
			    FROM evidence_agg e
			    JOIN packages p ON p.purl=e.purl AND p.publicness='PUBLIC'
			   WHERE COALESCE(BTRIM(e.env_json->>'os'),'') <> ''
			), ran AS (
			  -- jsonb_array_elements_text raises on a scalar, and a manifest
			  -- with no packages serializes to jsonb null rather than SQL
			  -- NULL, so one such row would abort the whole query instead of
			  -- skipping itself. Guard it the way admin_insights.go already
			  -- guards the identical expansion.
			  --
			  -- resolvedPackages is credited only from a v2 receipt whose
			  -- resolve stage PASSED — the rule resolvedPackageStrings applies
			  -- on the Fake, and the same reason: a list a failed resolve
			  -- claims to have installed installed nothing. Anything else
			  -- falls back to the manifest, exactly as the Fake does.
			  SELECT DISTINCT LOWER(BTRIM(r.receipt->'environment'->>'os')) AS os,
			         p.ecosystem, p.purl, r.contract_result
			    FROM receipts r
			    JOIN samples s ON s.sample_id=r.sample_id AND NOT s.quarantined
			   CROSS JOIN LATERAL jsonb_array_elements_text(
			         CASE WHEN r.receipt->>'schemaVersion' = '2'
			                   AND r.receipt->'stages'->>'resolve' = 'PASS'
			                   AND jsonb_typeof(r.receipt->'resolvedPackages') = 'array'
			                   AND jsonb_array_length(r.receipt->'resolvedPackages') > 0
			              THEN r.receipt->'resolvedPackages'
			              WHEN jsonb_typeof(s.manifest->'packages') = 'array'
			              THEN s.manifest->'packages'
			              ELSE '[]'::jsonb END) AS m(purl)
			    JOIN packages p ON p.purl=m.purl AND p.publicness='PUBLIC'
			   WHERE r.contract_result IN ('PASS','FAIL')
			     AND COALESCE(BTRIM(r.receipt->'environment'->>'os'),'') <> ''
			), measured AS (
			  SELECT DISTINCT os, ecosystem, purl FROM ran
			), proven AS (
			  SELECT DISTINCT os, ecosystem, purl FROM ran WHERE contract_result = 'PASS'
			), keys AS (
			  SELECT os, ecosystem FROM observed
			  UNION
			  SELECT os, ecosystem FROM measured
			)
			SELECT k.os, k.ecosystem,
			       (SELECT count(*) FROM observed o
			         WHERE o.os = k.os AND o.ecosystem = k.ecosystem),
			       (SELECT count(*) FROM measured m
			         WHERE m.os = k.os AND m.ecosystem = k.ecosystem),
			       (SELECT count(*) FROM proven v
			         WHERE v.os = k.os AND v.ecosystem = k.ecosystem),
			       (SELECT count(*) FROM observed o
			          JOIN proven v ON v.os = o.os AND v.ecosystem = o.ecosystem
			                       AND v.purl = o.purl
			         WHERE o.os = k.os AND o.ecosystem = k.ecosystem)
			  FROM keys k
			 ORDER BY 1 ASC, 3 DESC, 2 ASC
			 LIMIT 500`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row FarmAxisCoverage
			if err := rows.Scan(&row.OS, &row.Ecosystem, &row.Observed,
				&row.Measured, &row.Proven, &row.ObservedProven); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

var _ FarmStatsStore = (*PG)(nil)
