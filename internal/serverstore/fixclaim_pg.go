package serverstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
)

var _ FixClaimStore = (*PG)(nil)

const fixCandidateColumns = `id, dedup_key, candidate::text, status, pair_outcome, score, attempts,
	(closed_at IS NOT NULL), closed_reason, reproducer_source, reproducer_sample_id, claimed_by,
	COALESCE(claimed_at, 'epoch'::timestamptz), COALESCE(lease_expires_at, 'epoch'::timestamptz),
	evaluation::text, run_count, farm_seconds, created_at, updated_at,
	COALESCE(evaluated_at, 'epoch'::timestamptz)`

func scanFixCandidateRow(scan func(dest ...any) error) (FixCandidateRow, error) {
	var (
		row                           FixCandidateRow
		candidateJSON, evaluationJSON string
		claimedAt, leaseAt, evalAt    time.Time
	)
	err := scan(&row.ID, &row.DedupKey, &candidateJSON, &row.Status, &row.PairOutcome, &row.Score, &row.Attempts,
		&row.Closed, &row.ClosedReason, &row.ReproducerSource, &row.ReproducerSampleID, &row.ClaimedBy,
		&claimedAt, &leaseAt, &evaluationJSON, &row.RunCount, &row.FarmSeconds, &row.CreatedAt, &row.UpdatedAt, &evalAt)
	if err != nil {
		return row, err
	}
	if err := json.Unmarshal([]byte(candidateJSON), &row.Candidate); err != nil {
		return row, fmt.Errorf("fix candidate %d: decode candidate: %w", row.ID, err)
	}
	if evaluationJSON != "" && evaluationJSON != "{}" {
		if err := json.Unmarshal([]byte(evaluationJSON), &row.Evaluation); err != nil {
			return row, fmt.Errorf("fix candidate %d: decode evaluation: %w", row.ID, err)
		}
	}
	if claimedAt.Unix() != 0 {
		row.ClaimedAt = claimedAt
	}
	if leaseAt.Unix() != 0 {
		row.LeaseExpiresAt = leaseAt
	}
	if evalAt.Unix() != 0 {
		row.EvaluatedAt = evalAt
	}
	return row, nil
}

func (p *PG) UpsertFixCandidates(ctx context.Context, rows []FixCandidateRow, rejected int64, now time.Time) ([]FixCandidateUpsert, error) {
	var out []FixCandidateUpsert
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
		out = nil
		if _, err := tx.Exec(ctx, `
			INSERT INTO fix_claim_ingest(singleton, ingested, rejected, updated_at)
			VALUES('ingest', $1, $2, $3)
			ON CONFLICT (singleton) DO UPDATE SET
				ingested = fix_claim_ingest.ingested + EXCLUDED.ingested,
				rejected = fix_claim_ingest.rejected + EXCLUDED.rejected,
				updated_at = EXCLUDED.updated_at`, int64(len(rows))+rejected, rejected, now); err != nil {
			return err
		}
		for _, row := range rows {
			cand := row.Candidate.Normalized()
			candJSON, err := json.Marshal(cand)
			if err != nil {
				return err
			}
			evalJSON, _ := json.Marshal(fixclaims.Evaluation{Status: fixclaims.StatusClaimedFix})
			var inserted bool
			stored, err := scanFixCandidateRow(func(dest ...any) error {
				return tx.QueryRow(ctx, `
					INSERT INTO fix_candidates(
						dedup_key, ecosystem, name, claimed_bad_version, claimed_fixed_version,
						candidate, status, pair_outcome, score, evaluation, created_at, updated_at)
					VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,'',$8,$9::jsonb,$10,$10)
					ON CONFLICT (dedup_key) DO UPDATE SET updated_at = fix_candidates.updated_at
					RETURNING `+fixCandidateColumns+`, (xmax = 0) AS inserted`,
					cand.DedupKey(), cand.Ecosystem, cand.Name, cand.ClaimedBadVersion, cand.ClaimedFixedVersion,
					string(candJSON), string(fixclaims.StatusClaimedFix), row.Score, string(evalJSON), now,
				).Scan(append(dest, &inserted)...)
			})
			if err != nil {
				return err
			}
			out = append(out, FixCandidateUpsert{Row: stored, Duplicate: !inserted})
		}
		return tx.Commit(ctx)
	})
	return out, err
}

func (p *PG) ClaimFixWork(ctx context.Context, sessionID string, limits FixClaimLimits, now, leaseExpiresAt time.Time) (FixCandidateRow, FixClaimStatus, error) {
	if limits.MaxLeases <= 0 {
		limits.MaxLeases = DefaultFixClaimLimits().MaxLeases
	}
	var (
		row    FixCandidateRow
		status FixClaimStatus
	)
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		// One claim at a time across the fleet: the lease count and the pick
		// must be read under the same lock or two workers could both see
		// one free slot.
		if _, err := tx.Exec(ctx, `LOCK TABLE fix_candidates IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		held, err := scanFixCandidateRow(func(dest ...any) error {
			return tx.QueryRow(ctx, `
				UPDATE fix_candidates SET lease_expires_at = $3, updated_at = $2
				WHERE claimed_by = $1 AND lease_expires_at > $2
				RETURNING `+fixCandidateColumns, sessionID, now, leaseExpiresAt).Scan(dest...)
		})
		switch {
		case err == nil:
			row, status = held, FixClaimAssigned
			return tx.Commit(ctx)
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM fix_candidates WHERE claimed_by <> '' AND lease_expires_at > $1`, now).Scan(&live); err != nil {
			return err
		}
		if live >= limits.MaxLeases {
			status = FixClaimBudgetExhausted
			return tx.Commit(ctx)
		}
		picked, err := scanFixCandidateRow(func(dest ...any) error {
			return tx.QueryRow(ctx, `
				UPDATE fix_candidates SET claimed_by = $1, claimed_at = $2, lease_expires_at = $3,
					attempts = attempts + 1, updated_at = $2
				WHERE id = (
					SELECT id FROM fix_candidates
					WHERE closed_at IS NULL AND (claimed_by = '' OR lease_expires_at IS NULL OR lease_expires_at <= $2)
					ORDER BY attempts ASC, score DESC, id ASC
					LIMIT 1)
				RETURNING `+fixCandidateColumns, sessionID, now, leaseExpiresAt).Scan(dest...)
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			status = FixClaimNoWork
			return tx.Commit(ctx)
		case err != nil:
			return err
		}
		row, status = picked, FixClaimAssigned
		return tx.Commit(ctx)
	})
	if err != nil {
		return FixCandidateRow{}, "", err
	}
	return row, status, nil
}

// lockLeasedFixRow reads a candidate FOR UPDATE and checks the session's
// live lease, inside the caller's transaction.
func lockLeasedFixRow(ctx context.Context, tx pgx.Tx, id int64, sessionID string, now time.Time) (FixCandidateRow, error) {
	row, err := scanFixCandidateRow(func(dest ...any) error {
		return tx.QueryRow(ctx, `SELECT `+fixCandidateColumns+` FROM fix_candidates WHERE id = $1 FOR UPDATE`, id).Scan(dest...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return FixCandidateRow{}, ErrFixCandidateMissing
	}
	if err != nil {
		return FixCandidateRow{}, err
	}
	if !row.Leased(now) || row.ClaimedBy != sessionID {
		return FixCandidateRow{}, ErrFixLeaseMissing
	}
	return row, nil
}

func writeFixCandidateState(ctx context.Context, tx pgx.Tx, row FixCandidateRow) error {
	evalJSON, err := json.Marshal(row.Evaluation)
	if err != nil {
		return err
	}
	var claimedAt, leaseAt, evalAt, closedAt *time.Time
	if row.Closed {
		// closed_at is the fact; the time is when this write closed it,
		// and an already-closed row keeps its own.
		at := row.UpdatedAt
		closedAt = &at
	}
	if !row.ClaimedAt.IsZero() {
		claimedAt = &row.ClaimedAt
	}
	if !row.LeaseExpiresAt.IsZero() {
		leaseAt = &row.LeaseExpiresAt
	}
	if !row.EvaluatedAt.IsZero() {
		evalAt = &row.EvaluatedAt
	}
	_, err = tx.Exec(ctx, `
		UPDATE fix_candidates SET status = $2, pair_outcome = $3, attempts = $4,
			closed_at = COALESCE(closed_at, $5), closed_reason = $6,
			reproducer_source = $7, reproducer_sample_id = $8, claimed_by = $9, claimed_at = $10,
			lease_expires_at = $11, evaluation = $12::jsonb, run_count = $13, farm_seconds = $14,
			updated_at = $15, evaluated_at = $16
		WHERE id = $1`,
		row.ID, string(row.Status), string(row.PairOutcome), row.Attempts, closedAt, row.ClosedReason,
		string(row.ReproducerSource), row.ReproducerSampleID, row.ClaimedBy, claimedAt, leaseAt,
		string(evalJSON), row.RunCount, row.FarmSeconds, row.UpdatedAt, evalAt)
	return err
}

func (p *PG) SetFixReproducer(ctx context.Context, id int64, sessionID string, source fixclaims.ReproducerSource, sampleID string, now time.Time) (FixCandidateRow, error) {
	var row FixCandidateRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		row, err = lockLeasedFixRow(ctx, tx, id, sessionID, now)
		if err != nil {
			return err
		}
		row.ReproducerSource = source
		if sampleID != "" {
			row.ReproducerSampleID = sampleID
		}
		row.UpdatedAt = now
		if err := writeFixCandidateState(ctx, tx, row); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return FixCandidateRow{}, err
	}
	return row, nil
}

const fixRunColumns = `id, candidate_id, session_id, version, env_os, env_arch, env_runtime, env_runtime_version,
	verdict, failure_fingerprint, receipt_id, sample_id, farm_seconds, observed_at, created_at`

func scanFixRunRow(scan func(dest ...any) error) (FixRunRow, error) {
	var r FixRunRow
	err := scan(&r.ID, &r.CandidateID, &r.SessionID, &r.Version, &r.Environment.OS, &r.Environment.Arch,
		&r.Environment.Runtime, &r.Environment.RuntimeVersion, &r.Verdict, &r.FailureFingerprint,
		&r.ReceiptID, &r.SampleID, &r.FarmSeconds, &r.ObservedAt, &r.CreatedAt)
	return r, err
}

func readFixRuns(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, id int64) ([]FixRunRow, error) {
	rows, err := q.Query(ctx, `SELECT `+fixRunColumns+` FROM fix_runs WHERE candidate_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FixRunRow
	for rows.Next() {
		r, err := scanFixRunRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *PG) RecordFixRuns(ctx context.Context, id int64, sessionID string, runs []fixclaims.Run, limits FixClaimLimits, now time.Time) (FixCandidateRow, error) {
	var row FixCandidateRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		row, err = lockLeasedFixRow(ctx, tx, id, sessionID, now)
		if err != nil {
			return err
		}
		for _, r := range runs {
			if r.ObservedAt.IsZero() {
				r.ObservedAt = now
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO fix_runs(candidate_id, session_id, version, env_os, env_arch, env_runtime, env_runtime_version,
					verdict, failure_fingerprint, receipt_id, sample_id, farm_seconds, observed_at, created_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
				id, sessionID, r.Version, r.Environment.OS, r.Environment.Arch, r.Environment.Runtime, r.Environment.RuntimeVersion,
				string(r.Verdict), r.FailureFingerprint, r.ReceiptID, r.SampleID, r.FarmSeconds, r.ObservedAt, now); err != nil {
				return err
			}
		}
		stored, err := readFixRuns(ctx, tx, id)
		if err != nil {
			return err
		}
		all := make([]fixclaims.Run, 0, len(stored))
		for _, r := range stored {
			all = append(all, r.Run)
		}
		applyFixRuns(&row, all, now)
		row.ClaimedBy, row.ClaimedAt, row.LeaseExpiresAt = "", time.Time{}, time.Time{}
		settleFixCandidate(&row, limits)
		if err := writeFixCandidateState(ctx, tx, row); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return FixCandidateRow{}, err
	}
	return row, nil
}

func (p *PG) ReleaseFixWork(ctx context.Context, id int64, sessionID string, outcome FixWorkOutcome, detail string, limits FixClaimLimits, now time.Time) (FixCandidateRow, error) {
	var row FixCandidateRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		row, err = lockLeasedFixRow(ctx, tx, id, sessionID, now)
		if err != nil {
			return err
		}
		row.ClaimedBy, row.ClaimedAt, row.LeaseExpiresAt = "", time.Time{}, time.Time{}
		row.UpdatedAt = now
		switch outcome {
		case FixOutcomeComplete:
			row.Closed, row.ClosedReason = true, FixClosedComplete
		case FixOutcomeInfrastructure, FixOutcomeTransient:
			if row.Attempts > 0 {
				row.Attempts--
			}
		}
		settleFixCandidate(&row, limits)
		if err := writeFixCandidateState(ctx, tx, row); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return FixCandidateRow{}, err
	}
	return row, nil
}

func (p *PG) GetFixCandidate(ctx context.Context, id int64) (FixCandidateRow, bool, error) {
	var row FixCandidateRow
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var err error
		row, err = scanFixCandidateRow(func(dest ...any) error {
			return c.QueryRow(ctx, `SELECT `+fixCandidateColumns+` FROM fix_candidates WHERE id = $1`, id).Scan(dest...)
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			found = true
		}
		return err
	})
	return row, found, err
}

func (p *PG) ListFixRuns(ctx context.Context, id int64) ([]FixRunRow, error) {
	var out []FixRunRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var err error
		out, err = readFixRuns(ctx, c, id)
		return err
	})
	return out, err
}

func (p *PG) ListFixCandidates(ctx context.Context, q FixClaimQuery, limit int) ([]FixCandidateRow, error) {
	if limit <= 0 {
		limit = 100
	}
	var where []string
	var args []any
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if q.Ecosystem != "" {
		add("ecosystem = $%d", strings.ToLower(q.Ecosystem))
	}
	if q.Name != "" {
		add("lower(name) = lower($%d)", q.Name)
	}
	if q.Version != "" {
		add("(claimed_fixed_version = $%d OR claimed_bad_version = $%[1]d)", q.Version)
	}
	if q.Status != "" {
		add("status = $%d", string(q.Status))
	}
	if q.Open {
		where = append(where, "closed_at IS NULL")
	}
	sql := `SELECT ` + fixCandidateColumns + ` FROM fix_candidates`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	sql += fmt.Sprintf(" ORDER BY score DESC, id ASC LIMIT $%d", len(args))
	var out []FixCandidateRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanFixCandidateRow(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

func (p *PG) FixClaimStates(ctx context.Context) ([]fixclaims.CandidateState, int64, int64, error) {
	var (
		states             []fixclaims.CandidateState
		ingested, rejected int64
	)
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		err := c.QueryRow(ctx, `SELECT ingested, rejected FROM fix_claim_ingest WHERE singleton = 'ingest'`).Scan(&ingested, &rejected)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := c.Query(ctx, `SELECT status, pair_outcome, attempts, (closed_at IS NOT NULL), reproducer_source, run_count, farm_seconds FROM fix_candidates`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var st fixclaims.CandidateState
			if err := rows.Scan(&st.Status, &st.PairOutcome, &st.Attempts, &st.Exhausted, &st.ReproducerSource, &st.RunCount, &st.FarmSeconds); err != nil {
				return err
			}
			states = append(states, st)
		}
		return rows.Err()
	})
	return states, ingested, rejected, err
}
