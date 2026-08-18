package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// SampleRow is the local metadata for one cached or authored sample.
// ManifestJSON is the csx.json content; the artifact itself lives in the
// CAS keyed by SampleID.
type SampleRow struct {
	SampleID     string
	CaseID       string
	ManifestJSON string
	Status       string // LOCAL | LOCAL_PASS | PUBLISHED | CROSS_PASS | MATRIX_PASS | STABLE
	OriginSeeder string
	License      string
	CreatedAt    time.Time
	Pinned       bool
	HotScore     float64
	LastUsed     time.Time
	HasArtifact  bool
}

// SaveCase upserts a case; an empty CaseID is derived from content.
func (d *DB) SaveCase(ctx context.Context, c domain.Case) error {
	if c.CaseID == "" {
		c.CaseID = c.ComputeID()
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO cases(case_id, kind, goal, json) VALUES(?, ?, ?, ?)
		ON CONFLICT(case_id) DO UPDATE SET
		  kind = excluded.kind, goal = excluded.goal, json = excluded.json`,
		c.CaseID, c.Kind, c.Goal, string(domain.MustCanonicalJSON(c)))
	return err
}

// GetCase loads a case by id.
func (d *DB) GetCase(ctx context.Context, caseID string) (domain.Case, bool, error) {
	var raw string
	err := d.sql.QueryRowContext(ctx,
		`SELECT json FROM cases WHERE case_id = ?`, caseID).Scan(&raw)
	if err == sql.ErrNoRows {
		return domain.Case{}, false, nil
	}
	if err != nil {
		return domain.Case{}, false, err
	}
	var c domain.Case
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return domain.Case{}, false, err
	}
	return c, true, nil
}

// SaveSample upserts full sample metadata.
func (d *DB) SaveSample(ctx context.Context, s SampleRow) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO samples(sample_id, case_id, manifest_json, status, origin_seeder, license,
		  created_at, pinned, hot_score, last_used, has_artifact)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(sample_id) DO UPDATE SET
		  case_id = excluded.case_id, manifest_json = excluded.manifest_json,
		  -- A sample id is the content hash, so a conflict is the exact same
		  -- artifact arriving again. Re-indexing it must not erase verification
		  -- already represented by the local row and its immutable receipts.
		  -- SetSampleStatus remains the explicit path for derived lifecycle
		  -- changes; ingestion only keeps or raises the known status.
		  status = CASE
		    WHEN (CASE samples.status
		      WHEN 'STABLE' THEN 5 WHEN 'MATRIX_PASS' THEN 4
		      WHEN 'CROSS_PASS' THEN 3 WHEN 'PUBLISHED' THEN 2
		      WHEN 'LOCAL_PASS' THEN 1 WHEN 'LOCAL' THEN 0 ELSE -1 END)
		    > (CASE excluded.status
		      WHEN 'STABLE' THEN 5 WHEN 'MATRIX_PASS' THEN 4
		      WHEN 'CROSS_PASS' THEN 3 WHEN 'PUBLISHED' THEN 2
		      WHEN 'LOCAL_PASS' THEN 1 WHEN 'LOCAL' THEN 0 ELSE -1 END)
		    THEN samples.status ELSE excluded.status END,
		  origin_seeder = excluded.origin_seeder,
		  license = excluded.license, created_at = excluded.created_at,
		  pinned = excluded.pinned, hot_score = excluded.hot_score,
		  last_used = excluded.last_used, has_artifact = excluded.has_artifact`,
		s.SampleID, s.CaseID, s.ManifestJSON, s.Status, s.OriginSeeder, s.License,
		timeArg(s.CreatedAt), boolInt(s.Pinned), s.HotScore, timeArg(s.LastUsed),
		boolInt(s.HasArtifact))
	return err
}

const sampleCols = `sample_id, case_id, manifest_json, status, origin_seeder, license,
	created_at, pinned, hot_score, last_used, has_artifact`

func scanSample(scan func(...any) error) (SampleRow, error) {
	var s SampleRow
	var caseID, seeder, license, created, lastUsed sql.NullString
	var pinned, hasArtifact int
	err := scan(&s.SampleID, &caseID, &s.ManifestJSON, &s.Status, &seeder, &license,
		&created, &pinned, &s.HotScore, &lastUsed, &hasArtifact)
	if err != nil {
		return SampleRow{}, err
	}
	s.CaseID = caseID.String
	s.OriginSeeder = seeder.String
	s.License = license.String
	s.CreatedAt = parseTimeText(created)
	s.LastUsed = parseTimeText(lastUsed)
	s.Pinned = pinned != 0
	s.HasArtifact = hasArtifact != 0
	return s, nil
}

// GetSample loads one sample by id.
func (d *DB) GetSample(ctx context.Context, sampleID string) (SampleRow, bool, error) {
	row := d.sql.QueryRowContext(ctx,
		`SELECT `+sampleCols+` FROM samples WHERE sample_id = ?`, sampleID)
	s, err := scanSample(row.Scan)
	if err == sql.ErrNoRows {
		return SampleRow{}, false, nil
	}
	if err != nil {
		return SampleRow{}, false, err
	}
	return s, true, nil
}

// ListSamples returns all samples ordered by id.
func (d *DB) ListSamples(ctx context.Context) ([]SampleRow, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+sampleCols+` FROM samples ORDER BY sample_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SampleRow
	for rows.Next() {
		s, err := scanSample(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetSampleStatus updates the verification lifecycle status.
func (d *DB) SetSampleStatus(ctx context.Context, sampleID, status string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE samples SET status = ? WHERE sample_id = ?`, status, sampleID)
	return err
}

// TouchSample stamps last_used, feeding cache eviction ordering.
func (d *DB) TouchSample(ctx context.Context, sampleID string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE samples SET last_used = ? WHERE sample_id = ?`, nowText(), sampleID)
	return err
}

// SetSamplePinned marks a sample exempt from cache eviction.
func (d *DB) SetSamplePinned(ctx context.Context, sampleID string, pinned bool) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE samples SET pinned = ? WHERE sample_id = ?`, boolInt(pinned), sampleID)
	return err
}

// SetSampleHot records the network HOT score used by cache policy.
func (d *DB) SetSampleHot(ctx context.Context, sampleID string, score float64) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE samples SET hot_score = ? WHERE sample_id = ?`, score, sampleID)
	return err
}

// SaveReceipt stores a verification receipt under its content id.
// Receipts are immutable, so a duplicate save is a no-op.
func (d *DB) SaveReceipt(ctx context.Context, r domain.VerificationReceipt) error {
	created := r.CreatedAt
	if created == "" {
		created = nowText()
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO receipts(receipt_id, sample_id, json, created_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(receipt_id) DO NOTHING`,
		r.ReceiptID(), r.SampleID, string(domain.MustCanonicalJSON(r)), created)
	return err
}

// ReceiptsForSample loads all stored receipts for one sample.
func (d *DB) ReceiptsForSample(ctx context.Context, sampleID string) ([]domain.VerificationReceipt, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT json FROM receipts WHERE sample_id = ? ORDER BY created_at, receipt_id`, sampleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.VerificationReceipt
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r domain.VerificationReceipt
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RemoveSample atomically removes one sample's local searchable evidence.
// The case is deliberately retained: multiple sample artifacts may prove the
// same case, and withdrawing one artifact must not invalidate the others.
// Historical hits/interventions are retained as an audit trail, but receipts
// and both locally- and shard-shaped FTS document ids are removed with the row.
// It reports false without changing anything when sampleID is not present.
func (d *DB) RemoveSample(ctx context.Context, sampleID string) (bool, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM receipts WHERE sample_id = ?`, sampleID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM search_fts WHERE doc_id = ? OR doc_id = ?`, sampleID, "sample:"+sampleID); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM samples WHERE sample_id = ?`, sampleID)
	if err != nil {
		return false, err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if removed == 0 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
