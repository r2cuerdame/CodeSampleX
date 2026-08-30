package localdb

import (
	"context"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// RefusedEvidence is one coordinate the server said it will never accept, and
// when it said so.
type RefusedEvidence struct {
	Key       ObsKey
	Code      string
	Reason    string
	RefusedAt string
}

// RecordRefusedEvidence writes the tombstone for a terminal refusal.
//
// The alternative was to keep restoring the batch to pending, which is what
// pinned production's queue at its cap — the same refusals returning every
// sync. The alternative to THAT is dropping it, and evidence that vanishes
// with nothing to say it existed is the larger reliability problem. So it
// stops being retried and it stays sayable.
//
// Idempotent on the coordinate: the same refusal recorded twice is one fact,
// and the timestamp is the most recent time the server said it.
func (d *DB) RecordRefusedEvidence(ctx context.Context, key ObsKey, code, reason string) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO refused_evidence(epoch, purl, symbol, env_hash, stage, result, code, reason, refused_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(epoch, purl, symbol, env_hash, stage, result) DO UPDATE SET
		  code = excluded.code, reason = excluded.reason, refused_at = excluded.refused_at`,
		key.Epoch, key.PURL, key.Symbol, key.EnvHash, string(key.Stage), string(key.Result),
		code, reason, nowText())
	return err
}

// RefusedEvidenceCount is how many coordinates the server has refused for
// good, so `csx stats` can say it out loud instead of the number simply not
// being in the pending count any more.
func (d *DB) RefusedEvidenceCount(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM refused_evidence`).Scan(&n)
	return n, err
}

// RefusedEvidenceRows lists the tombstones, newest first, for an operator
// asking what stopped being sent and why.
func (d *DB) RefusedEvidenceRows(ctx context.Context, limit int) ([]RefusedEvidence, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.sql.QueryContext(ctx, `
		SELECT epoch, purl, symbol, env_hash, stage, result, code, reason, refused_at
		FROM refused_evidence ORDER BY refused_at DESC, purl LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RefusedEvidence
	for rows.Next() {
		var r RefusedEvidence
		var stage, result string
		if err := rows.Scan(&r.Key.Epoch, &r.Key.PURL, &r.Key.Symbol, &r.Key.EnvHash,
			&stage, &result, &r.Code, &r.Reason, &r.RefusedAt); err != nil {
			return nil, err
		}
		r.Key.Stage, r.Key.Result = domain.Stage(stage), domain.Result(result)
		out = append(out, r)
	}
	return out, rows.Err()
}
