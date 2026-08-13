package localdb

import (
	"context"
	"database/sql"
	"time"
)

// QueueItem is one pending upload (evidence batch, adoption, or receipt)
// waiting for the server to be reachable.
type QueueItem struct {
	ID        int64
	Kind      string // 'evidence' | 'adoption' | 'receipt'
	Payload   string
	CreatedAt time.Time
	Attempts  int
	LastError string
}

// Enqueue appends a payload to the upload queue and returns its id.
func (d *DB) Enqueue(ctx context.Context, kind, payload string) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `
		INSERT INTO upload_queue(kind, payload, created_at, attempts, last_error)
		VALUES(?, ?, ?, 0, '')`, kind, payload, nowText())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// QueuePending returns up to limit queued items, oldest first.
func (d *DB) QueuePending(ctx context.Context, limit int) ([]QueueItem, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, kind, payload, created_at, attempts, last_error
		FROM upload_queue ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		var it QueueItem
		var created, lastErr sql.NullString
		if err := rows.Scan(&it.ID, &it.Kind, &it.Payload, &created, &it.Attempts, &lastErr); err != nil {
			return nil, err
		}
		it.CreatedAt = parseTimeText(created)
		it.LastError = lastErr.String
		out = append(out, it)
	}
	return out, rows.Err()
}

// QueueMarkDone removes a successfully delivered item.
func (d *DB) QueueMarkDone(ctx context.Context, id int64) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM upload_queue WHERE id = ?`, id)
	return err
}

// QueueMarkFailed records a delivery failure, keeping the item queued.
func (d *DB) QueueMarkFailed(ctx context.Context, id int64, errMsg string) error {
	_, err := d.sql.ExecContext(ctx, `
		UPDATE upload_queue SET attempts = attempts + 1, last_error = ? WHERE id = ?`,
		errMsg, id)
	return err
}
