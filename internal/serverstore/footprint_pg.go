package serverstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var _ ExecutionFootprintStore = (*PG)(nil)

const executionFootprintColumns = `id, dedup_key, sample_id, outcome, stage,
	env_os, env_arch, env_runtime, env_runtime_version, failure_fingerprint,
	epoch, source_bucket, created_at, updated_at`

func scanExecutionFootprintRow(scan func(dest ...any) error, extra ...any) (ExecutionFootprintRow, error) {
	var row ExecutionFootprintRow
	dest := []any{&row.ID, &row.DedupKey, &row.SampleID, &row.Outcome, &row.Stage,
		&row.Environment.OS, &row.Environment.Arch, &row.Environment.Runtime,
		&row.Environment.RuntimeVersion, &row.FailureFingerprint,
		&row.Epoch, &row.SourceBucket, &row.CreatedAt, &row.UpdatedAt}
	err := scan(append(dest, extra...)...)
	return row, err
}

// RecordExecutionFootprint is one statement, like the adoption and the
// issue ingests: the same caller reporting the same sample twice in a day
// is one event, and the later report is the one that stands. A caller that
// first says "fail" and then, after fixing its own environment, says
// "pass" is telling us more about the same run, not about a second one.
func (p *PG) RecordExecutionFootprint(ctx context.Context, row ExecutionFootprintRow, now time.Time) (ExecutionFootprintRow, bool, error) {
	var stored ExecutionFootprintRow
	var inserted bool
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var err error
		stored, err = scanExecutionFootprintRow(c.QueryRow(ctx, `
			INSERT INTO execution_footprints(
				dedup_key, sample_id, outcome, stage,
				env_os, env_arch, env_runtime, env_runtime_version,
				failure_fingerprint, epoch, source_bucket, created_at, updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
			ON CONFLICT (dedup_key) DO UPDATE SET
				outcome = EXCLUDED.outcome,
				env_os = EXCLUDED.env_os,
				env_arch = EXCLUDED.env_arch,
				env_runtime = EXCLUDED.env_runtime,
				env_runtime_version = EXCLUDED.env_runtime_version,
				failure_fingerprint = EXCLUDED.failure_fingerprint,
				updated_at = EXCLUDED.updated_at
			RETURNING `+executionFootprintColumns+`, (xmax = 0) AS inserted`,
			row.DedupKey, row.SampleID, string(row.Outcome), string(row.Stage),
			row.Environment.OS, row.Environment.Arch, row.Environment.Runtime,
			row.Environment.RuntimeVersion, row.FailureFingerprint,
			row.Epoch, row.SourceBucket, now).Scan, &inserted)
		return err
	})
	if err != nil {
		return ExecutionFootprintRow{}, false, err
	}
	return stored, !inserted, nil
}

// ExecutionFootprintCounts tallies one sample's footprints by outcome. Each
// row is already one (source, day, stage), so a count of rows is a count of
// daily sources and a single caller cannot inflate it inside a day.
func (p *PG) ExecutionFootprintCounts(ctx context.Context, sampleID string) (ExecutionFootprintCounts, error) {
	var out ExecutionFootprintCounts
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `
			SELECT COUNT(*) FILTER (WHERE outcome = $2),
			       COUNT(*) FILTER (WHERE outcome = $3),
			       COUNT(*) FILTER (WHERE outcome = $4)
			  FROM execution_footprints WHERE sample_id = $1`,
			sampleID, string(domain.FootprintPass), string(domain.FootprintFail),
			string(domain.FootprintCouldNotRun)).Scan(&out.Pass, &out.Fail, &out.CouldNotRun)
	})
	return out, err
}

func (p *PG) ListExecutionFootprints(ctx context.Context, limit int) ([]ExecutionFootprintRow, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []ExecutionFootprintRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT `+executionFootprintColumns+`
			FROM execution_footprints ORDER BY id DESC LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanExecutionFootprintRow(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}
