package serverstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (p *PG) RecordAnonymousClient(ctx context.Context, hash string, now time.Time, credentialPresent bool) error {
	if !validAnonymousHash(hash) {
		return errors.New("invalid anonymous client hash")
	}
	return p.withConn(ctx, func(c *pgx.Conn) error {
		// A single statement commits summary and daily activity atomically.
		_, err := c.Exec(ctx, `WITH client AS (
		 INSERT INTO anonymous_clients(client_hash,first_seen,last_seen,request_count) VALUES($1,$2,$2,1)
		 ON CONFLICT(client_hash) DO UPDATE SET first_seen=LEAST(anonymous_clients.first_seen,EXCLUDED.first_seen),
		 last_seen=GREATEST(anonymous_clients.last_seen,EXCLUDED.last_seen),request_count=anonymous_clients.request_count+1
		 RETURNING client_hash)
		 INSERT INTO anonymous_client_days(day,client_hash,request_count,credential_present_count,credential_issued_count)
		 SELECT ($2::timestamptz AT TIME ZONE 'UTC')::date,client_hash,1,
		 CASE WHEN $3::boolean THEN 1 ELSE 0 END,CASE WHEN $3::boolean THEN 0 ELSE 1 END FROM client
		 ON CONFLICT(day,client_hash) DO UPDATE SET request_count=anonymous_client_days.request_count+1,
		 credential_present_count=anonymous_client_days.credential_present_count+EXCLUDED.credential_present_count,
		 credential_issued_count=anonymous_client_days.credential_issued_count+EXCLUDED.credential_issued_count`, hash, now.UTC(), credentialPresent)
		return err
	})
}

func (p *PG) AnonymousAnalytics(ctx context.Context, now time.Time) (AnonymousAnalytics, error) {
	var out AnonymousAnalytics
	today := anonymousDay(now)
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		if err := c.QueryRow(ctx, `SELECT started_at,credential_adoption_started_at,(SELECT count(*) FROM anonymous_clients) FROM anonymous_analytics_collection WHERE singleton`).Scan(&out.CollectedSince, &out.CredentialSince, &out.TotalClients); err != nil {
			return err
		}
		start := today.AddDate(0, 0, -89)
		if d := anonymousDay(out.CollectedSince); d.After(start) {
			start = d
		}
		rows, err := c.Query(ctx, `SELECT d::date,
		 (SELECT count(*) FROM anonymous_clients WHERE first_seen>=d AT TIME ZONE 'UTC' AND first_seen<(d+interval '1 day') AT TIME ZONE 'UTC'),
		 (SELECT count(*) FROM anonymous_client_days WHERE day=d::date),
		 (SELECT count(DISTINCT client_hash) FROM anonymous_client_days WHERE day BETWEEN d::date-29 AND d::date),
			 (SELECT COALESCE(sum(request_count),0) FROM anonymous_client_days WHERE day=d::date),
			 (SELECT COALESCE(sum(credential_present_count),0) FROM anonymous_client_days WHERE day=d::date),
			 (SELECT COALESCE(sum(credential_issued_count),0) FROM anonymous_client_days WHERE day=d::date)
		 FROM generate_series($1::date::timestamp,$2::date::timestamp,interval '1 day') d ORDER BY d`, start.Format("2006-01-02"), today.Format("2006-01-02"))
		if err != nil {
			return err
		}
		for rows.Next() {
			var m AnonymousDailyMetric
			if err := rows.Scan(&m.Day, &m.NRU, &m.DAU, &m.MAU, &m.Requests, &m.CredentialPresent, &m.CredentialIssued); err != nil {
				rows.Close()
				return err
			}
			out.Daily = append(out.Daily, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = c.Query(ctx, `WITH cohorts AS (
		 SELECT client_hash,(first_seen AT TIME ZONE 'UTC')::date AS day FROM anonymous_clients
		 WHERE first_seen >= $1::date::timestamp AT TIME ZONE 'UTC' AND first_seen < ($2::date+1)::timestamp AT TIME ZONE 'UTC')
		 SELECT c.day,count(*),
		 count(*) FILTER(WHERE EXISTS(SELECT 1 FROM anonymous_client_days a WHERE a.client_hash=c.client_hash AND a.day=c.day+1)),
		 count(*) FILTER(WHERE EXISTS(SELECT 1 FROM anonymous_client_days a WHERE a.client_hash=c.client_hash AND a.day=c.day+7)),
		 count(*) FILTER(WHERE EXISTS(SELECT 1 FROM anonymous_client_days a WHERE a.client_hash=c.client_hash AND a.day=c.day+30))
		 FROM cohorts c GROUP BY c.day ORDER BY c.day`, start.Format("2006-01-02"), today.Format("2006-01-02"))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var co AnonymousCohort
			var d1, d7, d30 int64
			if err := rows.Scan(&co.Day, &co.Size, &d1, &d7, &d30); err != nil {
				return err
			}
			for i, offset := range []int{1, 7, 30} {
				cell := AnonymousRetention{Day: offset, Eligible: co.Day.AddDate(0, 0, offset).Before(today)}
				if cell.Eligible {
					cell.Active = []int64{d1, d7, d30}[i]
				}
				co.Retention = append(co.Retention, cell)
			}
			out.Cohorts = append(out.Cohorts, co)
		}
		return rows.Err()
	})
	if n := len(out.Daily); n > 0 {
		m := out.Daily[n-1]
		out.NRU = m.NRU
		out.DAU = m.DAU
		out.MAU = m.MAU
		out.CredentialPresent = m.CredentialPresent
		out.CredentialIssued = m.CredentialIssued
	}
	return out, err
}

func (p *PG) PruneAnonymousAnalytics(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var removed int64
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, `DELETE FROM anonymous_client_days WHERE (day,client_hash) IN (SELECT day,client_hash FROM anonymous_client_days WHERE day<$1::date ORDER BY day LIMIT $2)`, anonymousDay(now).AddDate(0, 0, -119).Format("2006-01-02"), limit)
		if err != nil {
			return err
		}
		removed = tag.RowsAffected()
		tag, err = c.Exec(ctx, `DELETE FROM anonymous_clients WHERE client_hash IN(SELECT client_hash FROM anonymous_clients WHERE last_seen<$1 ORDER BY last_seen LIMIT $2)`, now.AddDate(0, 0, -365), limit-int(removed))
		removed += tag.RowsAffected()
		return err
	})
	return removed, err
}
