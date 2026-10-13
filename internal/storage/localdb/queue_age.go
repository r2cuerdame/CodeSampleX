package localdb

import (
	"context"
	"database/sql"
	"time"
)

// PendingUploadOldest reads only existing retryable report timestamps, with the
// same 1,000-row bound as queue counts. It never reads or changes payloads.
// A missing timestamp makes age unknown; observations have only a day epoch.
func (d *DB) PendingUploadOldest(ctx context.Context, limit int) (time.Time, int, bool, error) {
	if limit <= 0 {
		return time.Time{}, 0, true, nil
	}
	if limit > 1000 {
		limit = 1000
	}
	oldest := time.Time{}
	count := 0
	valid := true
	err := d.withDiagnosticConn(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, "SELECT CASE WHEN length(created_at)<=40 THEN created_at END FROM upload_queue INDEXED BY upload_queue_pending WHERE attempts < ? ORDER BY id LIMIT ?", MaxQueueAttempts, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw sql.NullString
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			count++
			t := parseTimeText(raw)
			if t.IsZero() {
				valid = false
				continue
			}
			if oldest.IsZero() || t.Before(oldest) {
				oldest = t
			}
		}
		return rows.Err()
	})
	return oldest, count, valid, err
}
