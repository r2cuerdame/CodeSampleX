package localdb

import (
	"context"
	"database/sql"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// HitRow is one local search-hit record used for the dashboard and
// adoption follow-up. Queries stay local; this table is never uploaded.
type HitRow struct {
	ID            int64 // set by ListHits
	TS            time.Time
	Query         string
	Grade         domain.MatchGrade
	SampleID      string
	Adopted       bool
	PostBuildPass sql.NullBool // unknown until the post-adoption build reports
}

// RecordHit appends one hit row.
func (d *DB) RecordHit(ctx context.Context, h HitRow) error {
	ts := h.TS
	if ts.IsZero() {
		ts = time.Now()
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO hits(ts, query, grade, sample_id, adopted, post_build_pass)
		VALUES(?, ?, ?, ?, ?, ?)`,
		timeArg(ts), h.Query, string(h.Grade), h.SampleID, boolInt(h.Adopted), h.PostBuildPass)
	return err
}

// ListHits returns the most recent hits, newest first.
func (d *DB) ListHits(ctx context.Context, limit int) ([]HitRow, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, ts, query, grade, sample_id, adopted, post_build_pass
		FROM hits ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HitRow
	for rows.Next() {
		var h HitRow
		var ts, query, grade, sampleID sql.NullString
		var adopted int
		if err := rows.Scan(&h.ID, &ts, &query, &grade, &sampleID, &adopted, &h.PostBuildPass); err != nil {
			return nil, err
		}
		h.TS = parseTimeText(ts)
		h.Query = query.String
		h.Grade = domain.MatchGrade(grade.String)
		h.SampleID = sampleID.String
		h.Adopted = adopted != 0
		out = append(out, h)
	}
	return out, rows.Err()
}

// CountHits returns the total number of recorded hits.
func (d *DB) CountHits(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM hits`).Scan(&n)
	return n, err
}
