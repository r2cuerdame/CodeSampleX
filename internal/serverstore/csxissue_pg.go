package serverstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var _ CSXIssueStore = (*PG)(nil)

const csxIssueColumns = `id, fingerprint, report::text, surface, issue_kind, component,
	status, verdict, replay_reason, canonical_ref, occurrences, reporter_bucket,
	first_seen, last_seen, COALESCE(verdict_at, 'epoch'::timestamptz)`

func scanCSXIssueRow(scan func(dest ...any) error) (CSXIssueReportRow, error) {
	var row CSXIssueReportRow
	var verdictAt time.Time
	err := scan(&row.ID, &row.Fingerprint, &row.ReportJSON, &row.Surface, &row.IssueKind,
		&row.Component, &row.Status, &row.Verdict, &row.ReplayReason, &row.CanonicalRef,
		&row.Occurrences, &row.ReporterBucket, &row.FirstSeen, &row.LastSeen, &verdictAt)
	if err != nil {
		return row, err
	}
	// 'epoch' stands in for NULL so one scan shape serves every read; a
	// report with no verdict reports a zero time, never 1970.
	if verdictAt.Unix() != 0 {
		row.VerdictAt = verdictAt
	}
	return row, nil
}

// RecordCSXIssueReport is one statement, for the same reason the anomaly
// ingest is: many consumers meeting one defect at once is the normal case,
// and a read-then-insert would let both pass the read and file two tickets
// for one bug.
func (p *PG) RecordCSXIssueReport(ctx context.Context, row CSXIssueReportRow, now time.Time) (CSXIssueReportRow, bool, error) {
	var stored CSXIssueReportRow
	var inserted bool
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var verdictAt time.Time
		err := c.QueryRow(ctx, `
			INSERT INTO csx_issue_reports(
				fingerprint, report, surface, issue_kind, component,
				status, reporter_bucket, first_seen, last_seen)
			VALUES($1,$2::jsonb,$3,$4,$5,$6,$7,$8,$8)
			ON CONFLICT (fingerprint) DO UPDATE SET
				occurrences = csx_issue_reports.occurrences + 1,
				last_seen = EXCLUDED.last_seen
			RETURNING `+csxIssueColumns+`, (xmax = 0) AS inserted`,
			row.Fingerprint, row.ReportJSON, row.Surface, row.IssueKind, row.Component,
			csxIssueStatusOrTriage(row.Status), row.ReporterBucket, now,
		).Scan(&stored.ID, &stored.Fingerprint, &stored.ReportJSON, &stored.Surface,
			&stored.IssueKind, &stored.Component, &stored.Status, &stored.Verdict,
			&stored.ReplayReason, &stored.CanonicalRef, &stored.Occurrences,
			&stored.ReporterBucket, &stored.FirstSeen, &stored.LastSeen, &verdictAt, &inserted)
		if err != nil {
			return err
		}
		if verdictAt.Unix() != 0 {
			stored.VerdictAt = verdictAt
		}
		return nil
	})
	if err != nil {
		return CSXIssueReportRow{}, false, err
	}
	return stored, !inserted, nil
}

func csxIssueStatusOrTriage(status string) string {
	if status == "" {
		return domain.CSXIssueStatusTriage
	}
	return status
}

func (p *PG) CSXIssueReportByID(ctx context.Context, id int64) (CSXIssueReportRow, bool, error) {
	var row CSXIssueReportRow
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		r, err := scanCSXIssueRow(c.QueryRow(ctx,
			`SELECT `+csxIssueColumns+` FROM csx_issue_reports WHERE id=$1`, id).Scan)
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

func (p *PG) SetCSXIssueTriage(ctx context.Context, id int64, status, replayReason string) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx,
			`UPDATE csx_issue_reports SET status=$2, replay_reason=$3 WHERE id=$1`,
			id, status, replayReason)
		return err
	})
}

func (p *PG) SetCSXIssueVerdict(ctx context.Context, id int64, verdict string, at time.Time) (bool, error) {
	updated := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, `
			UPDATE csx_issue_reports SET verdict=$2, status=$3, verdict_at=$4
			 WHERE id=$1 AND verdict=''`, id, verdict, domain.CSXIssueStatusResolved, at)
		if err != nil {
			return err
		}
		updated = tag.RowsAffected() > 0
		return nil
	})
	return updated, err
}

// LinkCSXIssueCanonical refuses to link anything that is not a confirmed
// defect. The check is in the statement rather than in a handler so a second
// caller cannot forget it: linking an unconfirmed report to a bug is how a
// candidate quietly becomes a claim.
func (p *PG) LinkCSXIssueCanonical(ctx context.Context, id int64, ref string) (bool, error) {
	linked := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx,
			`UPDATE csx_issue_reports SET canonical_ref=$2 WHERE id=$1 AND verdict=$3`,
			id, ref, domain.CSXIssueVerdictDefect)
		if err != nil {
			return err
		}
		linked = tag.RowsAffected() > 0
		return nil
	})
	return linked, err
}

func (p *PG) ListCSXIssueReports(ctx context.Context, limit int) ([]CSXIssueReportRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []CSXIssueReportRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx,
			`SELECT `+csxIssueColumns+` FROM csx_issue_reports ORDER BY id DESC LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanCSXIssueRow(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) CSXIssueInsights(ctx context.Context, now time.Time, days int) (CSXIssueInsights, error) {
	if days <= 0 {
		days = AdminInsightDays
	}
	out := CSXIssueInsights{WindowStart: now.AddDate(0, 0, -days), WindowEnd: now}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `
			SELECT COALESCE(SUM(occurrences),0),
			       COUNT(*),
			       COUNT(*) FILTER (WHERE status=$3),
			       COUNT(*) FILTER (WHERE status=$4),
			       COUNT(*) FILTER (WHERE status=$5),
			       COUNT(*) FILTER (WHERE status=$6),
			       COUNT(*) FILTER (WHERE verdict=$7),
			       COUNT(*) FILTER (WHERE canonical_ref <> '')
			  FROM csx_issue_reports
			 WHERE last_seen >= $1 AND last_seen <= $2`,
			out.WindowStart, now,
			domain.CSXIssueStatusTriage, domain.CSXIssueStatusReplayQueued,
			domain.CSXIssueStatusNoReplayLane, domain.CSXIssueStatusResolved,
			domain.CSXIssueVerdictDefect,
		).Scan(&out.Occurrences, &out.Unique, &out.Triage, &out.ReplayQueued,
			&out.NoReplayLane, &out.Resolved, &out.Confirmed, &out.Linked)
	})
	out.Duplicates = out.Occurrences - out.Unique
	return out, err
}
