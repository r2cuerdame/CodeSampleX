package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ObsKey is the aggregate identity of one local observation row —
// exactly the observations table primary key plus the two descriptive
// columns carried along with it. It holds no source, path, or project
// name by construction.
type ObsKey struct {
	Epoch            string // daily bucket "2026-08-13"
	PURL             string // canonical purl string
	Symbol           string
	SymbolConfidence domain.SymbolConfidence
	EnvHash          string
	Stage            domain.Stage
	Result           domain.Result
	ErrorFP          string
	ErrorCode        string
	// Direct says the reporter listed this package in their own manifest
	// rather than receiving it through somebody else's dependency.
	Direct bool
}

// ObsRow is one aggregate with its accumulated count.
type ObsRow struct {
	ObsKey
	Count int
}

// RecordObservation adds incr to the aggregate identified by key,
// creating the row if needed. New evidence re-marks the row as pending
// so it is picked up by the next upload; the batcher must therefore
// flush-then-mark, since a row uploaded and then incremented again
// carries its full count, not a delta.
func (d *DB) RecordObservation(ctx context.Context, key ObsKey, incr int) error {
	conf := key.SymbolConfidence
	if conf == "" {
		conf = domain.SymbolUnknown
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO observations(epoch, purl, symbol, symbol_confidence, env_hash, stage, result, count, error_fp, error_code, direct, uploaded)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(epoch, purl, symbol, env_hash, stage, result, error_fp) DO UPDATE SET
		  count = count + excluded.count,
		  symbol_confidence = excluded.symbol_confidence,
		  error_code = excluded.error_code,
		  -- Chosen wins: one build resolving a package transitively does not
		  -- unsay another that listed it.
		  direct = MAX(observations.direct, excluded.direct),
		  uploaded = 0`,
		key.Epoch, key.PURL, key.Symbol, string(conf), key.EnvHash,
		string(key.Stage), string(key.Result), incr, key.ErrorFP, key.ErrorCode,
		boolToInt(key.Direct))
	return err
}

// PendingObservations returns up to limit not-yet-uploaded aggregates in
// deterministic order.
func (d *DB) PendingObservations(ctx context.Context, limit int) ([]ObsRow, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT epoch, purl, symbol, symbol_confidence, env_hash, stage, result, error_fp, error_code, direct, count
		FROM observations WHERE uploaded = 0
		ORDER BY epoch, purl, symbol, env_hash, stage, result, error_fp
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObsRow
	for rows.Next() {
		var r ObsRow
		var direct int
		if err := rows.Scan(&r.Epoch, &r.PURL, &r.Symbol, &r.SymbolConfidence,
			&r.EnvHash, &r.Stage, &r.Result, &r.ErrorFP, &r.ErrorCode, &direct, &r.Count); err != nil {
			return nil, err
		}
		r.Direct = direct != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkObservationsUploaded flags the given aggregates as drained.
func (d *DB) MarkObservationsUploaded(ctx context.Context, keys []ObsKey) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		UPDATE observations SET uploaded = 1
		WHERE epoch = ? AND purl = ? AND symbol = ? AND env_hash = ? AND stage = ? AND result = ? AND error_fp = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, k := range keys {
		if _, err := stmt.ExecContext(ctx, k.Epoch, k.PURL, k.Symbol,
			k.EnvHash, string(k.Stage), string(k.Result), k.ErrorFP); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveEnvironment caches a fingerprint by its content hash.
func (d *DB) SaveEnvironment(ctx context.Context, fp domain.EnvironmentFingerprint) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO environments(hash, json) VALUES(?, ?)
		ON CONFLICT(hash) DO UPDATE SET json = excluded.json`,
		fp.Hash(), string(domain.MustCanonicalJSON(fp)))
	return err
}

// GetEnvironment loads a cached fingerprint by hash.
func (d *DB) GetEnvironment(ctx context.Context, hash string) (domain.EnvironmentFingerprint, bool, error) {
	var raw string
	err := d.sql.QueryRowContext(ctx,
		`SELECT json FROM environments WHERE hash = ?`, hash).Scan(&raw)
	if err == sql.ErrNoRows {
		return domain.EnvironmentFingerprint{}, false, nil
	}
	if err != nil {
		return domain.EnvironmentFingerprint{}, false, err
	}
	var fp domain.EnvironmentFingerprint
	if err := json.Unmarshal([]byte(raw), &fp); err != nil {
		return domain.EnvironmentFingerprint{}, false, err
	}
	return fp, true, nil
}

// SymbolUsageRow is one observed symbol use of a local project
// (project_bucket is the rotating HMAC bucket, never a path or name).
type SymbolUsageRow struct {
	PURL          string
	Symbol        string
	Confidence    domain.SymbolConfidence
	ProjectBucket string
	LastSeen      time.Time
}

// RecordSymbolUsage upserts one symbol sighting.
func (d *DB) RecordSymbolUsage(ctx context.Context, purl domain.PURL, symbol string, confidence domain.SymbolConfidence, projectBucket string) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO symbol_usages(purl, symbol, confidence, project_bucket, last_seen)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(purl, symbol, project_bucket) DO UPDATE SET
		  confidence = excluded.confidence, last_seen = excluded.last_seen`,
		purl.String(), symbol, string(confidence), projectBucket, nowText())
	return err
}

// SymbolUsages lists recorded usages for one package.
func (d *DB) SymbolUsages(ctx context.Context, purl domain.PURL) ([]SymbolUsageRow, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT purl, symbol, confidence, project_bucket, last_seen
		FROM symbol_usages WHERE purl = ? ORDER BY symbol, project_bucket`, purl.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolUsageRow
	for rows.Next() {
		var r SymbolUsageRow
		var last sql.NullString
		if err := rows.Scan(&r.PURL, &r.Symbol, &r.Confidence, &r.ProjectBucket, &last); err != nil {
			return nil, err
		}
		r.LastSeen = parseTimeText(last)
		out = append(out, r)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
