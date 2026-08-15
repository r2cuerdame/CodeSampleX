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

// MaxQueueAttempts is how many times an item is retried before it is set
// aside. The drainer's own rule was "it stops being retried by attempt
// count" — and nothing read the count, so nothing ever stopped.
//
// An item the server will never accept (a 4xx: a payload from an older
// build, a sample id that no longer exists) sat at the head of a
// FIFO-with-a-limit queue and was re-POSTed on every tick forever. Once
// enough of them accumulated to fill the drain window, they crowded out
// every valid item behind them: `csx sync` kept exiting 0 while the
// adoption reports — the one signal about whether the network's answers
// actually work, and the only one that cannot be recomputed from anything
// else — silently stopped arriving.
const MaxQueueAttempts = 8

// QueuePending returns up to limit queued items, oldest first, skipping the
// ones that have exhausted their attempts. They stay in the table: an item
// set aside is evidence about a delivery problem, and deleting it would
// erase both the report and the reason it never landed.
func (d *DB) QueuePending(ctx context.Context, limit int) ([]QueueItem, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, kind, payload, created_at, attempts, last_error
		FROM upload_queue WHERE attempts < ? ORDER BY id LIMIT ?`,
		MaxQueueAttempts, limit)
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

// QueueSetAside stops an item from being retried at all, for a failure that
// retrying cannot fix.
func (d *DB) QueueSetAside(ctx context.Context, id int64, errMsg string) error {
	_, err := d.sql.ExecContext(ctx, `
		UPDATE upload_queue SET attempts = ?, last_error = ? WHERE id = ?`,
		MaxQueueAttempts, errMsg, id)
	return err
}

// QueueSetAsideCount reports how many items have stopped being retried, so
// something can say so out loud instead of the queue draining to nothing
// visible.
func (d *DB) QueueSetAsideCount(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM upload_queue WHERE attempts >= ?`, MaxQueueAttempts).Scan(&n)
	return n, err
}

// QueueMarkFailed records a delivery failure, keeping the item queued.
func (d *DB) QueueMarkFailed(ctx context.Context, id int64, errMsg string) error {
	_, err := d.sql.ExecContext(ctx, `
		UPDATE upload_queue SET attempts = attempts + 1, last_error = ? WHERE id = ?`,
		errMsg, id)
	return err
}
