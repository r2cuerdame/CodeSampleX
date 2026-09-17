package serverstore

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/apidemand"
)

var _ apidemand.Store = (*PG)(nil)

// apiDemandLatencyColumns are the histogram columns in bucket order. The
// order must match apidemand.LatencyBoundsMS followed by the overflow.
var apiDemandLatencyColumns = []string{
	"latency_le_10", "latency_le_25", "latency_le_50", "latency_le_100", "latency_le_250",
	"latency_le_500", "latency_le_1000", "latency_le_2500", "latency_le_5000", "latency_gt_5000",
}

func apiDemandHourlyKey(key apidemand.HourlyKey) string {
	return strings.Join([]string{key.Hour.UTC().Format(time.RFC3339), key.Route, key.Outcome, key.Auth}, "|")
}

// UpsertDemand implements apidemand.Store. One transaction per flush: the
// batch is already aggregated, so each distinct key is one upsert, and a
// batch either lands whole or leaves nothing behind for a retry to double.
func (p *PG) UpsertDemand(ctx context.Context, batch apidemand.Batch) error {
	if batch.Empty() {
		return nil
	}
	ctx = WithQueryClass(ctx, ClassBackground)
	hourlySet := make([]string, len(apiDemandLatencyColumns))
	for i, column := range apiDemandLatencyColumns {
		hourlySet[i] = column + "=api_demand_hourly." + column + "+EXCLUDED." + column
	}
	hourlySQL := `INSERT INTO api_demand_hourly(dedup_key,hour,route,outcome,auth,requests,latency_sum_ms,` +
		strings.Join(apiDemandLatencyColumns, ",") + `,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,now())
		ON CONFLICT(dedup_key) DO UPDATE SET
		  requests=api_demand_hourly.requests+EXCLUDED.requests,
		  latency_sum_ms=api_demand_hourly.latency_sum_ms+EXCLUDED.latency_sum_ms,` +
		strings.Join(hourlySet, ",") + `,updated_at=now()`
	return p.withConn(ctx, func(c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
			var queued pgx.Batch
			for key, counts := range batch.Hourly {
				if !apidemand.ValidRoute(key.Route) {
					continue
				}
				args := []any{apiDemandHourlyKey(key), key.Hour.UTC().Truncate(time.Hour), key.Route, key.Outcome, key.Auth, counts.Requests, counts.LatencySumMS}
				for _, n := range counts.Buckets {
					args = append(args, n)
				}
				queued.Queue(hourlySQL, args...)
			}
			for key, n := range batch.Clients {
				queued.Queue(`INSERT INTO api_demand_client_daily(dedup_key,day,client_kind,client_version,protocol,requests)
					VALUES($1,$2,$3,$4,$5,$6)
					ON CONFLICT(dedup_key) DO UPDATE SET requests=api_demand_client_daily.requests+EXCLUDED.requests`,
					strings.Join([]string{key.Day, key.Kind, key.Version, key.Protocol}, "|"), key.Day, key.Kind, key.Version, key.Protocol, n)
			}
			for key, n := range batch.Countries {
				queued.Queue(`INSERT INTO api_demand_country_daily(dedup_key,day,country,requests)
					VALUES($1,$2,$3,$4)
					ON CONFLICT(dedup_key) DO UPDATE SET requests=api_demand_country_daily.requests+EXCLUDED.requests`,
					key.Day+"|"+key.Country, key.Day, key.Country, n)
			}
			for key, n := range batch.Callers {
				if !apidemand.ValidRoute(key.Route) {
					continue
				}
				queued.Queue(`INSERT INTO api_demand_callers_daily(dedup_key,day,route,caller_hash,requests)
					VALUES($1,$2,$3,$4,$5)
					ON CONFLICT(dedup_key) DO UPDATE SET requests=api_demand_callers_daily.requests+EXCLUDED.requests`,
					strings.Join([]string{key.Day, key.Route, key.CallerHash}, "|"), key.Day, key.Route, key.CallerHash, n)
			}
			if queued.Len() == 0 {
				return nil
			}
			results := tx.SendBatch(ctx, &queued)
			defer results.Close()
			for i := 0; i < queued.Len(); i++ {
				if _, err := results.Exec(); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// PruneDemand implements apidemand.Store: rows older than the cutoff go,
// by hour for the hourly table and by UTC day for the daily ones.
func (p *PG) PruneDemand(ctx context.Context, before time.Time) error {
	ctx = WithQueryClass(ctx, ClassBackground)
	cutoffDay := before.UTC().Format("2006-01-02")
	return p.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `DELETE FROM api_demand_hourly WHERE hour < $1`, before.UTC()); err != nil {
			return err
		}
		for _, table := range []string{"api_demand_client_daily", "api_demand_country_daily", "api_demand_callers_daily"} {
			if _, err := c.Exec(ctx, `DELETE FROM `+table+` WHERE day < $1`, cutoffDay); err != nil {
				return err
			}
		}
		return nil
	})
}

// DemandReport implements apidemand.Store with the same windows the memory
// store uses. Every read is bounded by the hour or day index.
func (p *PG) DemandReport(ctx context.Context, now time.Time) (apidemand.Report, error) {
	now = now.UTC()
	weekStart := now.Add(-7 * 24 * time.Hour)
	priorStart := now.Add(-14 * 24 * time.Hour)
	firstDay := apidemand.ReportFirstDay(now)
	weekDay := weekStart.Format("2006-01-02")
	priorDay := priorStart.Format("2006-01-02")
	var report apidemand.Report
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var oldest *time.Time
		if err := c.QueryRow(ctx, `SELECT MIN(hour) FROM api_demand_hourly`).Scan(&oldest); err != nil {
			return err
		}
		if oldest != nil {
			report.OldestHour = oldest.UTC()
		}

		rows, err := c.Query(ctx, `SELECT hour, outcome, auth, SUM(requests)::bigint
			FROM api_demand_hourly WHERE hour >= $1
			GROUP BY hour, outcome, auth ORDER BY hour, outcome, auth`, priorStart)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row apidemand.HourRow
			if err := rows.Scan(&row.Hour, &row.Outcome, &row.Auth, &row.Requests); err != nil {
				rows.Close()
				return err
			}
			row.Hour = row.Hour.UTC()
			report.Hours = append(report.Hours, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		sums := make([]string, len(apiDemandLatencyColumns))
		for i, column := range apiDemandLatencyColumns {
			sums[i] = "SUM(" + column + ")::bigint"
		}
		rows, err = c.Query(ctx, `SELECT route, outcome, SUM(requests)::bigint, SUM(latency_sum_ms)::bigint, `+strings.Join(sums, ", ")+`
			FROM api_demand_hourly WHERE hour >= $1
			GROUP BY route, outcome ORDER BY route, outcome`, weekStart)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row apidemand.RouteRow
			dest := []any{&row.Route, &row.Outcome, &row.Requests, &row.LatencySumMS}
			for i := range row.Buckets {
				dest = append(dest, &row.Buckets[i])
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return err
			}
			report.Routes = append(report.Routes, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		rows, err = c.Query(ctx, `SELECT route, COUNT(DISTINCT caller_hash)::bigint
			FROM api_demand_callers_daily WHERE day >= $1
			GROUP BY route ORDER BY route`, weekDay)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row apidemand.RouteCallers
			if err := rows.Scan(&row.Route, &row.Callers); err != nil {
				rows.Close()
				return err
			}
			report.RouteCallers = append(report.RouteCallers, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		if err := c.QueryRow(ctx, `SELECT
			(SELECT COUNT(DISTINCT caller_hash) FROM api_demand_callers_daily WHERE day >= $1)::bigint,
			(SELECT COUNT(DISTINCT caller_hash) FROM api_demand_callers_daily WHERE day >= $2 AND day < $1)::bigint`,
			weekDay, priorDay).Scan(&report.UniqueCallers, &report.PriorUniqueCallers); err != nil {
			return err
		}

		rows, err = c.Query(ctx, `SELECT client_kind, client_version, protocol, SUM(requests)::bigint
			FROM api_demand_client_daily WHERE day >= $1
			GROUP BY client_kind, client_version, protocol ORDER BY client_kind, client_version, protocol`, firstDay)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row apidemand.ClientRow
			if err := rows.Scan(&row.Kind, &row.Version, &row.Protocol, &row.Requests); err != nil {
				rows.Close()
				return err
			}
			report.Clients = append(report.Clients, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		rows, err = c.Query(ctx, `SELECT country, SUM(requests)::bigint
			FROM api_demand_country_daily WHERE day >= $1
			GROUP BY country ORDER BY country`, firstDay)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row apidemand.CountryRow
			if err := rows.Scan(&row.Country, &row.Requests); err != nil {
				return err
			}
			report.Countries = append(report.Countries, row)
		}
		return rows.Err()
	})
	if err != nil {
		return apidemand.Report{}, err
	}
	// The memory store sorts routes the same way; keep the two comparable.
	sort.Slice(report.Routes, func(i, j int) bool {
		a, b := report.Routes[i], report.Routes[j]
		if a.Route != b.Route {
			return a.Route < b.Route
		}
		return a.Outcome < b.Outcome
	})
	return report, nil
}
