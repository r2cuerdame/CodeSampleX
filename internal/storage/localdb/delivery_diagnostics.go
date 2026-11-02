package localdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
)

const (
	StatEvidenceAttemptDiagnostic = "lastEvidenceAttemptDiagnostic"
	StatEvidenceACKDiagnostic     = "lastEvidenceAckDiagnostic"
)

// withDiagnosticConn leases one connection and restores its original busy
// timeout before returning it. Only optional diagnostics use no-wait access;
// normal data operations retain their exact contention policy.
func (d *DB) withDiagnosticConn(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var original int
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&original); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
		return err
	}
	defer func() {
		if _, restoreErr := conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA busy_timeout=%d", original)); restoreErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	return fn(conn)
}

// SetDeliveryDiagnostics overwrites at most two existing meta slots in one
// transaction. Lock contention is immediately returned, never retried.
func (d *DB) SetDeliveryDiagnostics(ctx context.Context, values map[string]string) error {
	if len(values) > 2 {
		return errors.New("too many diagnostic values")
	}
	for key, value := range values {
		if key != StatEvidenceAttemptDiagnostic && key != StatEvidenceACKDiagnostic {
			return errors.New("unknown diagnostic key")
		}
		if len(value) > 512 {
			return errors.New("diagnostic value too large")
		}
	}
	if len(values) == 0 {
		return nil
	}
	return d.withDiagnosticConn(ctx, func(conn *sql.Conn) error {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for key, value := range values {
			if _, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", statPrefix+key, value); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}
func (d *DB) ReadDeliveryDiagnostics(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := d.withDiagnosticConn(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, "SELECT key, CASE WHEN length(value)<=512 THEN value ELSE '' END FROM meta WHERE key IN (?,?)", statPrefix+StatEvidenceAttemptDiagnostic, statPrefix+StatEvidenceACKDiagnostic)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				return err
			}
			if key == statPrefix+StatEvidenceAttemptDiagnostic {
				out[StatEvidenceAttemptDiagnostic] = value
			} else if key == statPrefix+StatEvidenceACKDiagnostic {
				out[StatEvidenceACKDiagnostic] = value
			}
		}
		return rows.Err()
	})
	return out, err
}
