package serverstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var _ AnomalyStore = (*PG)(nil)

// RecordAnomalyReport is one statement on purpose.
//
// Two agents hitting the same wrong answer at the same moment is the normal
// case, not the race to worry about later: the whole point of the channel is
// that many consumers meet the same defect. A read-then-insert would let both
// pass the read and both queue a container. The unique fingerprint plus
// ON CONFLICT makes "already known" the database's answer rather than the
// application's guess, and the RETURNING tells the caller which of the two
// happened without a second round trip.
func (p *PG) RecordAnomalyReport(ctx context.Context, row AnomalyReportRow, now time.Time) (AnomalyReportRow, bool, error) {
	var stored AnomalyReportRow
	var inserted bool
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var verdictAt time.Time
		err := c.QueryRow(ctx, `
			INSERT INTO anomaly_reports(
				fingerprint, report, anomaly_type, purl, symbol, sample_id,
				status, reporter_bucket, first_seen, last_seen)
			VALUES($1,$2::jsonb,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$9)
			ON CONFLICT (fingerprint) DO UPDATE SET
				reports = anomaly_reports.reports + 1,
				last_seen = EXCLUDED.last_seen
			RETURNING id, fingerprint, report::text, anomaly_type, purl, symbol,
			          COALESCE(sample_id,''), status, verdict, unsupported_reason,
			          COALESCE(job_id,0), reports, reporter_bucket,
			          first_seen, last_seen,
			          COALESCE(verdict_at, 'epoch'::timestamptz),
			          (xmax = 0) AS inserted`,
			row.Fingerprint, row.ReportJSON, row.AnomalyType, row.PURL, row.Symbol,
			row.SampleID, statusOrQueued(row.Status), row.ReporterBucket, now,
		).Scan(&stored.ID, &stored.Fingerprint, &stored.ReportJSON, &stored.AnomalyType,
			&stored.PURL, &stored.Symbol, &stored.SampleID, &stored.Status, &stored.Verdict,
			&stored.UnsupportedReason, &stored.JobID, &stored.Reports, &stored.ReporterBucket,
			&stored.FirstSeen, &stored.LastSeen, &verdictAt, &inserted)
		if err != nil {
			return err
		}
		if verdictAt.Unix() != 0 {
			stored.VerdictAt = verdictAt
		}
		return nil
	})
	if err != nil {
		return AnomalyReportRow{}, false, err
	}
	return stored, !inserted, nil
}

func statusOrQueued(status string) string {
	if status == "" {
		return domain.AnomalyStatusQueued
	}
	return status
}

const anomalySelect = `
	SELECT id, fingerprint, report::text, anomaly_type, purl, symbol,
	       COALESCE(sample_id,''), status, verdict, unsupported_reason,
	       COALESCE(job_id,0), reports, reporter_bucket,
	       first_seen, last_seen, COALESCE(verdict_at, 'epoch'::timestamptz)
	  FROM anomaly_reports`

func scanAnomalyRow(scan func(dest ...any) error) (AnomalyReportRow, error) {
	var row AnomalyReportRow
	var verdictAt time.Time
	err := scan(&row.ID, &row.Fingerprint, &row.ReportJSON, &row.AnomalyType, &row.PURL,
		&row.Symbol, &row.SampleID, &row.Status, &row.Verdict, &row.UnsupportedReason,
		&row.JobID, &row.Reports, &row.ReporterBucket, &row.FirstSeen, &row.LastSeen, &verdictAt)
	if err != nil {
		return row, err
	}
	// 'epoch' stands in for NULL so one scan shape serves every read; a
	// report with no verdict must report a zero time, never 1970.
	if verdictAt.Unix() != 0 {
		row.VerdictAt = verdictAt
	}
	return row, nil
}

func (p *PG) AnomalyReportByID(ctx context.Context, id int64) (AnomalyReportRow, bool, error) {
	var row AnomalyReportRow
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		r, err := scanAnomalyRow(c.QueryRow(ctx, anomalySelect+` WHERE id=$1`, id).Scan)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		row, found = r, true
		return nil
	})
	return row, found, err
}

func (p *PG) AttachAnomalyVerificationJob(ctx context.Context, id, jobID int64, status, unsupportedReason string) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			UPDATE anomaly_reports
			   SET job_id=NULLIF($2,0::bigint), status=$3, unsupported_reason=$4
			 WHERE id=$1`, id, jobID, status, unsupportedReason)
		return err
	})
}

// SetAnomalyVerdict refuses to overwrite an answer already given.
//
// A sample accumulates receipts for its own reasons, and each new one would
// otherwise re-decide every report ever filed against it — including reports
// a different receipt already closed. The verdict a report carries is the
// answer to the question it asked, at the time it was asked.
func (p *PG) SetAnomalyVerdict(ctx context.Context, id int64, verdict string, at time.Time) (bool, error) {
	updated := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, `
			UPDATE anomaly_reports
			   SET verdict=$2, status=$3, verdict_at=$4
			 WHERE id=$1 AND verdict=''`, id, verdict, domain.AnomalyStatusVerified, at)
		if err != nil {
			return err
		}
		updated = tag.RowsAffected() > 0
		return nil
	})
	return updated, err
}

func (p *PG) OpenAnomalyReportsForSample(ctx context.Context, sampleID string) ([]AnomalyReportRow, error) {
	var out []AnomalyReportRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, anomalySelect+`
			WHERE sample_id=$1 AND verdict=''
			ORDER BY id ASC
			LIMIT 200`, sampleID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanAnomalyRow(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) ListAnomalyReports(ctx context.Context, limit int) ([]AnomalyReportRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []AnomalyReportRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, anomalySelect+` ORDER BY id DESC LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanAnomalyRow(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

// AnomalyInsights is bounded by the last-seen index and returns fixed
// buckets, so the operator page costs one indexed scan rather than a table
// walk that grows with the channel.
func (p *PG) AnomalyInsights(ctx context.Context, now time.Time, days int) (AnomalyInsights, error) {
	if days <= 0 {
		days = AdminInsightDays
	}
	out := AnomalyInsights{WindowStart: now.AddDate(0, 0, -days), WindowEnd: now}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var latencySeconds, maxLatencySeconds float64
		err := c.QueryRow(ctx, `
			SELECT COALESCE(SUM(reports),0),
			       COUNT(*),
			       COUNT(*) FILTER (WHERE status=$3),
			       COUNT(*) FILTER (WHERE status=$4),
			       COUNT(*) FILTER (WHERE status=$5),
			       COUNT(*) FILTER (WHERE status=$6),
			       COUNT(*) FILTER (WHERE verdict IN ($7,$8,$9)),
			       COUNT(*) FILTER (WHERE verdict=$7),
			       COUNT(*) FILTER (WHERE verdict=$10),
			       COALESCE(SUM(EXTRACT(EPOCH FROM (verdict_at - first_seen)))
			                FILTER (WHERE verdict_at IS NOT NULL),0),
			       COUNT(*) FILTER (WHERE verdict_at IS NOT NULL),
			       COALESCE(MAX(EXTRACT(EPOCH FROM (verdict_at - first_seen)))
			                FILTER (WHERE verdict_at IS NOT NULL),0)
			  FROM anomaly_reports
			 WHERE last_seen >= $1 AND last_seen <= $2`,
			out.WindowStart, now,
			domain.AnomalyStatusQueued, domain.AnomalyStatusVerifying,
			domain.AnomalyStatusVerified, domain.AnomalyStatusUnsupported,
			domain.AnomalyVerdictCSXDefect, domain.AnomalyVerdictCompatibilityBoundary,
			domain.AnomalyVerdictNewEvidence, domain.AnomalyVerdictInsufficientEvidence,
		).Scan(&out.Reports, &out.Unique, &out.Queued, &out.Verifying, &out.Verified,
			&out.Unsupported, &out.Confirmed, &out.CSXDefects, &out.Insufficient,
			&latencySeconds, &out.VerdictLatencyCount, &maxLatencySeconds)
		if err != nil {
			return err
		}
		out.Duplicates = out.Reports - out.Unique
		out.VerdictLatencyTotal = time.Duration(latencySeconds * float64(time.Second))
		out.VerdictLatencyMax = time.Duration(maxLatencySeconds * float64(time.Second))

		rows, err := c.Query(ctx, `
			SELECT verdict, COUNT(*)
			  FROM anomaly_reports
			 WHERE last_seen >= $1 AND last_seen <= $2 AND verdict <> ''
			 GROUP BY verdict
			 ORDER BY COUNT(*) DESC, verdict ASC
			 LIMIT 16`, out.WindowStart, now)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v AnomalyVerdictCounts
			if err := rows.Scan(&v.Verdict, &v.Count); err != nil {
				rows.Close()
				return err
			}
			out.Verdicts = append(out.Verdicts, v)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		// One bucket submitting far more than the rest is what a runaway
		// client looks like from here. The bucket rotates daily and names
		// nobody; only the count is read.
		return c.QueryRow(ctx, `
			SELECT COALESCE(MAX(total),0) FROM (
				SELECT SUM(reports) AS total
				  FROM anomaly_reports
				 WHERE last_seen >= $1 AND last_seen <= $2 AND reporter_bucket <> ''
				 GROUP BY reporter_bucket) AS per_reporter`,
			out.WindowStart, now).Scan(&out.BusiestReporter)
	})
	return out, err
}
