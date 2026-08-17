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
		// -1 means the offer was explicitly not applied. It is a completed
		// report (so it must not be selected again) but not an adoption.
		h.Adopted = adopted > 0
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

// MarkAdopted records that a sample the caller was already shown got
// applied, and reports whether an existing search row was updated.
//
// An adoption used to INSERT its own row, so one search that was then
// adopted counted as two hits: the search row, plus a second row with an
// empty query and an empty grade. `csx stats` read that as two searches
// answered, and the post-hit success rate divided by the inflated number.
// An adoption is not a search; it is what happened to one.
//
// The most recent un-adopted row for this sample is the one being reported
// on. A false return means no such row exists — an agent can report an
// adoption for a sample it obtained some other way — and the caller
// decides whether that deserves a row of its own.
func (d *DB) MarkAdopted(ctx context.Context, sampleID string, applied bool, buildPass sql.NullBool) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `
		UPDATE hits SET adopted = ?, post_build_pass = ?
		WHERE id = (
			SELECT id FROM hits
			 WHERE sample_id = ? AND adopted = 0
			 ORDER BY id DESC LIMIT 1)`,
		adoptionState(applied), buildPass, sampleID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CountAdoptions returns how many recorded hits were adopted.
func (d *DB) CountAdoptions(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM hits WHERE adopted > 0`).Scan(&n)
	return n, err
}

// adoptionState distinguishes "not reported yet" (0) from an explicit
// applied=false report (-1) without changing the existing SQLite schema.
// Public/local surfaces still expose both as adopted=false.
func adoptionState(applied bool) int {
	if applied {
		return 1
	}
	return -1
}
