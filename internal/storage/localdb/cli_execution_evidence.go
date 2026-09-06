package localdb

import (
	"context"
	"database/sql"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func recordCLIExecutionEvidence(ctx context.Context, exec migrationExecutor, obs domain.CLIExperienceObservation) error {
	canon := obs.Coordinate.Canonical()
	obs.Coordinate = canon
	if obs.Provenance == "" {
		obs.Provenance = domain.ProvenanceField
	}
	if obs.EvidenceQuality == "" {
		obs.EvidenceQuality = domain.EvidencePartial
	}
	// The identity is derived from the canonical environment; callers cannot
	// supply a mismatched label for a different machine coordinate.
	obs.EnvironmentID = canon.Environment.Hash()
	if obs.FinishedAt == "" {
		obs.FinishedAt = obs.ObservedAt
	}
	count := obs.Count
	if count <= 0 {
		count = 1
	}
	_, err := exec.ExecContext(ctx, `
		INSERT INTO cli_execution_evidence(
		  evidence_id, coordinate_id, tool, tool_version, subcommand, args_pattern,
		  shell, env_hash, provenance, result, termination_kind, exit_code, signal,
		  timeout_millis, error_fp, error_code, error_summary, evidence_quality,
		  stdout_fp, stdout_excerpt, stdout_truncated,
		  stderr_fp, stderr_excerpt, stderr_truncated,
		  started_at, finished_at, count)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(evidence_id) DO UPDATE SET
		  started_at = CASE
		    WHEN cli_execution_evidence.started_at = '' THEN excluded.started_at
		    WHEN excluded.started_at = '' THEN cli_execution_evidence.started_at
		    WHEN excluded.started_at < cli_execution_evidence.started_at THEN excluded.started_at
		    ELSE cli_execution_evidence.started_at END,
		  finished_at = CASE
		    WHEN excluded.finished_at > cli_execution_evidence.finished_at THEN excluded.finished_at
		    ELSE cli_execution_evidence.finished_at END,
		  count = cli_execution_evidence.count + excluded.count`,
		obs.EvidenceID(), canon.CoordinateID(), canon.Tool, canon.ToolVersion,
		canon.Subcommand, canon.ArgsPattern, canon.Shell, obs.EnvironmentID,
		obs.Provenance, obs.Result, obs.Termination.Kind, obs.Termination.ExitCode,
		obs.Termination.Signal, obs.Termination.TimeoutMillis, obs.ErrorFingerprint,
		obs.ErrorCode, obs.ErrorSummary, obs.EvidenceQuality,
		obs.Stdout.Fingerprint, obs.Stdout.Excerpt, obs.Stdout.Truncated,
		obs.Stderr.Fingerprint, obs.Stderr.Excerpt, obs.Stderr.Truncated,
		obs.StartedAt, obs.FinishedAt, count)
	return err
}

// ListCLIExecutionEvidence returns the structured, secret-safe executions for
// one exact command/version/shell/environment coordinate, newest first.
func (d *DB) ListCLIExecutionEvidence(ctx context.Context, coord domain.CLIExperienceCoordinate, limit int) ([]domain.CLIExperienceObservation, error) {
	canon := coord.Canonical()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := d.sql.QueryContext(ctx, `
		SELECT tool, tool_version, subcommand, args_pattern, shell, env_hash,
		       provenance, result, termination_kind, exit_code, signal, timeout_millis,
		       error_fp, error_code, error_summary, evidence_quality,
		       stdout_fp, stdout_excerpt, stdout_truncated,
		       stderr_fp, stderr_excerpt, stderr_truncated,
		       started_at, finished_at, count
		FROM cli_execution_evidence
		WHERE coordinate_id = ?
		ORDER BY finished_at DESC, evidence_id
		LIMIT ?`, canon.CoordinateID(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.CLIExperienceObservation
	for rows.Next() {
		var (
			tool, version, subcommand, argsPattern, shell, envHash string
			provenance, result, termKind                           string
			exitCode                                               sql.NullInt64
			signal                                                 string
			timeoutMillis                                          int64
			errorFP, errorCode, errorSummary, quality              string
			stdoutFP, stdoutExcerpt                                string
			stderrFP, stderrExcerpt                                string
			stdoutTruncated, stderrTruncated                       bool
			startedAt, finishedAt                                  string
			count                                                  int64
		)
		if err := rows.Scan(&tool, &version, &subcommand, &argsPattern, &shell, &envHash,
			&provenance, &result, &termKind, &exitCode, &signal, &timeoutMillis,
			&errorFP, &errorCode, &errorSummary, &quality,
			&stdoutFP, &stdoutExcerpt, &stdoutTruncated,
			&stderrFP, &stderrExcerpt, &stderrTruncated,
			&startedAt, &finishedAt, &count); err != nil {
			return nil, err
		}
		env, _, err := d.GetEnvironment(ctx, envHash)
		if err != nil {
			return nil, err
		}
		var ec *int
		if exitCode.Valid {
			v := int(exitCode.Int64)
			ec = &v
		}
		out = append(out, domain.CLIExperienceObservation{
			Coordinate: domain.CLIExperienceCoordinate{
				Tool: tool, ToolVersion: version, Subcommand: subcommand,
				ArgsPattern: argsPattern, Shell: shell, Environment: env,
			},
			Provenance: domain.ExperienceProvenance(provenance),
			Result:     domain.Result(result),
			Termination: domain.FailureTermination{
				Kind: domain.TerminationKind(termKind), ExitCode: ec,
				Signal: signal, TimeoutMillis: timeoutMillis,
			},
			ErrorFingerprint: errorFP,
			ErrorCode:        errorCode,
			ErrorSummary:     errorSummary,
			EvidenceQuality:  domain.EvidenceQuality(quality),
			ObservedAt:       finishedAt,
			StartedAt:        startedAt,
			FinishedAt:       finishedAt,
			EnvironmentID:    envHash,
			Stdout: domain.CLIStreamEvidence{
				Fingerprint: stdoutFP, Excerpt: stdoutExcerpt, Truncated: stdoutTruncated,
			},
			Stderr: domain.CLIStreamEvidence{
				Fingerprint: stderrFP, Excerpt: stderrExcerpt, Truncated: stderrTruncated,
			},
			Count: count,
		})
	}
	return out, rows.Err()
}
