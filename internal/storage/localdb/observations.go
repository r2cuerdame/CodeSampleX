package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ObsKey is the aggregate identity of one local observation row —
// exactly the observations table primary key plus the two descriptive
// columns carried along with it. It holds no source, path, or project
// name by construction.
type ObsKey struct {
	Epoch              string // daily bucket "2026-08-13"
	PURL               string // canonical purl string
	Symbol             string
	SymbolConfidence   domain.SymbolConfidence
	EnvHash            string
	Stage              domain.Stage
	Result             domain.Result
	ErrorFP            string
	ErrorCode          string
	TerminationKind    domain.TerminationKind
	ExitCode           *int
	Signal             string
	TimeoutMillis      int64
	ErrorSummary       string
	EvidenceQuality    domain.EvidenceQuality
	OuterCommand       string
	OuterStage         domain.Stage
	ActualToolchain    string
	StageEvidence      domain.FailureStageEvidence
	FailureEvidenceGap domain.FailureEvidenceGap
	// Direct says the reporter listed this package in their own manifest
	// rather than receiving it through somebody else's dependency.
	Direct bool
	// Coresident is the other versions of this library in the same
	// resolution, in sorted order.
	Coresident []string
	// DependsOn is the packages this one pulled in the same resolution.
	DependsOn []string
}

// ObsRow is one aggregate with its accumulated count.
type ObsRow struct {
	ObsKey
	Count                 int
	LegacyReconciledCount int
}

// LegacyWindowsObservations returns the durable unsigned-DWORD rows, including
// rows an older uploader already marked complete. The upgraded batcher compares
// their raw+canonical local total with LegacyReconciledCount so an old process
// cannot permanently steal a repair by toggling uploaded between two calls.
func (d *DB) LegacyWindowsObservations(ctx context.Context) ([]ObsRow, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT epoch, purl, symbol, env_hash, stage, result, error_fp, error_code,
		       termination_kind, exit_code, signal, timeout_millis, error_summary,
		       evidence_quality, actual_toolchain, count, legacy_reconciled_count
		FROM observations
		WHERE exit_code > 2147483647 AND exit_code <= 4294967295
		ORDER BY epoch, purl, symbol, env_hash, stage, result, error_fp`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObsRow
	for rows.Next() {
		var r ObsRow
		if err := rows.Scan(&r.Epoch, &r.PURL, &r.Symbol, &r.EnvHash, &r.Stage, &r.Result,
			&r.ErrorFP, &r.ErrorCode, &r.TerminationKind, &r.ExitCode, &r.Signal,
			&r.TimeoutMillis, &r.ErrorSummary, &r.EvidenceQuality, &r.ActualToolchain,
			&r.Count, &r.LegacyReconciledCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LegacyWindowsObservationCount is the cheap eligibility probe used before
// the full rolling-upgrade reconciliation. Most homes never wrote an unsigned
// Windows exit status, so their empty upload path should stop at this partial
// index instead of scanning historical observations.
func (d *DB) LegacyWindowsObservationCount(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	var n int
	err := d.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
		  SELECT 1 FROM observations INDEXED BY observations_legacy_windows
		  WHERE exit_code > 2147483647 AND exit_code <= 4294967295 LIMIT ?
		)`, limit).Scan(&n)
	return n, err
}

// RequeueLegacyWindowsObservation marks one raw row pending only while its
// latest combined total has not been acknowledged by an upgraded client.
// The predicate makes concurrent upgraded callers idempotent; an old client
// does not know or modify the high-water column, so its later mark/upload is
// detected again on the next pass.
func (d *DB) RequeueLegacyWindowsObservation(ctx context.Context, key ObsKey, combinedCount int) (bool, error) {
	result, err := d.sql.ExecContext(ctx, `
		UPDATE observations SET uploaded = 0
		WHERE epoch = ? AND purl = ? AND symbol = ? AND env_hash = ?
		  AND stage = ? AND result = ? AND error_fp = ?
		  AND legacy_reconciled_count < ?`,
		key.Epoch, key.PURL, key.Symbol, key.EnvHash, string(key.Stage), string(key.Result), key.ErrorFP,
		combinedCount)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// MarkLegacyWindowsObservationReconciled advances the local delivery
// high-water only after the server accepted the canonical combined batch.
// MAX prevents a late acknowledgement from moving the durable proof backward.
func (d *DB) MarkLegacyWindowsObservationReconciled(ctx context.Context, key ObsKey, combinedCount int) error {
	_, err := d.sql.ExecContext(ctx, `
		UPDATE observations
		SET legacy_reconciled_count = MAX(legacy_reconciled_count, ?)
		WHERE epoch = ? AND purl = ? AND symbol = ? AND env_hash = ?
		  AND stage = ? AND result = ? AND error_fp = ?`,
		combinedCount, key.Epoch, key.PURL, key.Symbol, key.EnvHash,
		string(key.Stage), string(key.Result), key.ErrorFP)
	return err
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
		INSERT INTO observations(epoch, purl, symbol, symbol_confidence, env_hash, stage, result, count, error_fp, error_code,
		  termination_kind, exit_code, signal, timeout_millis, error_summary, evidence_quality,
		  outer_command, outer_stage, actual_toolchain, stage_evidence, failure_evidence_gap,
		  direct, coresident, depends_on, uploaded)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(epoch, purl, symbol, env_hash, stage, result, error_fp) DO UPDATE SET
		  count = count + excluded.count,
		  symbol_confidence = excluded.symbol_confidence,
		  error_code = excluded.error_code,
		  termination_kind = excluded.termination_kind,
		  exit_code = excluded.exit_code,
		  signal = excluded.signal,
		  timeout_millis = excluded.timeout_millis,
		  error_summary = excluded.error_summary,
		  evidence_quality = excluded.evidence_quality,
		  outer_command = excluded.outer_command,
		  outer_stage = excluded.outer_stage,
		  actual_toolchain = excluded.actual_toolchain,
		  stage_evidence = excluded.stage_evidence,
		  failure_evidence_gap = excluded.failure_evidence_gap,
		  -- Chosen wins: one build resolving a package transitively does not
		  -- unsay another that listed it.
		  direct = MAX(observations.direct, excluded.direct),
		  -- Last resolution wins: a project that FIXED its duplicate should
		  -- stop reporting one, and MAX would pin the old collision forever.
		  coresident = excluded.coresident,
		  depends_on = excluded.depends_on,
		  uploaded = 0`,
		key.Epoch, key.PURL, key.Symbol, string(conf), key.EnvHash,
		string(key.Stage), string(key.Result), incr, key.ErrorFP, key.ErrorCode,
		string(key.TerminationKind), key.ExitCode, key.Signal, key.TimeoutMillis,
		key.ErrorSummary, string(key.EvidenceQuality),
		key.OuterCommand, string(key.OuterStage), key.ActualToolchain, string(key.StageEvidence), string(key.FailureEvidenceGap),
		boolToInt(key.Direct), strings.Join(key.Coresident, ","),
		strings.Join(key.DependsOn, ","))
	return err
}

// PendingObservations returns up to limit not-yet-uploaded aggregates in
// deterministic order.
func (d *DB) PendingObservations(ctx context.Context, limit int) ([]ObsRow, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT epoch, purl, symbol, symbol_confidence, env_hash, stage, result, error_fp, error_code,
		       termination_kind, exit_code, signal, timeout_millis, error_summary, evidence_quality,
		       outer_command, outer_stage, actual_toolchain, stage_evidence, failure_evidence_gap,
		       direct, coresident, depends_on, count
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
		var coresident, dependsOn string
		if err := rows.Scan(&r.Epoch, &r.PURL, &r.Symbol, &r.SymbolConfidence,
			&r.EnvHash, &r.Stage, &r.Result, &r.ErrorFP, &r.ErrorCode,
			&r.TerminationKind, &r.ExitCode, &r.Signal, &r.TimeoutMillis, &r.ErrorSummary, &r.EvidenceQuality,
			&r.OuterCommand, &r.OuterStage, &r.ActualToolchain, &r.StageEvidence, &r.FailureEvidenceGap, &direct,
			&coresident, &dependsOn, &r.Count); err != nil {
			return nil, err
		}
		r.Direct = direct != 0
		if coresident != "" {
			r.Coresident = strings.Split(coresident, ",")
		}
		if dependsOn != "" {
			r.DependsOn = strings.Split(dependsOn, ",")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingObservationCount counts pending aggregates without materializing
// their payload-bearing rows. limit bounds the diagnostic just as the old
// PendingObservations(ctx, 1000) call did; the partial observations_pending
// index makes an empty queue an index probe rather than a database scan.
func (d *DB) PendingObservationCount(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	var n int
	err := d.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
		  SELECT 1 FROM observations INDEXED BY observations_pending
		  WHERE uploaded = 0 LIMIT ?
		)`, limit).Scan(&n)
	return n, err
}

// ObservationCount returns the durable full-epoch count for one aggregate
// identity, whether it is pending or already uploaded. The evidence batcher
// uses this during a rolling exit-code repair: pre-fix and canonical
// fingerprints can be two local rows for one server aggregate, and the wire
// contribution must include both without rewriting or deleting either row.
func (d *DB) ObservationCount(ctx context.Context, key ObsKey) (int, bool, error) {
	var count int
	err := d.sql.QueryRowContext(ctx, `
		SELECT count FROM observations
		WHERE epoch = ? AND purl = ? AND symbol = ? AND env_hash = ?
		  AND stage = ? AND result = ? AND error_fp = ?`,
		key.Epoch, key.PURL, key.Symbol, key.EnvHash,
		string(key.Stage), string(key.Result), key.ErrorFP).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return count, err == nil, err
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
