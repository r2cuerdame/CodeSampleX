package localdb

import (
	"context"
	"database/sql"
	"time"
)

// ShardRow is one cached compatibility shard from the server.
type ShardRow struct {
	Key      string // "npm/axios/1"
	ETag     string
	JSON     string
	SyncedAt time.Time
}

// SaveShard upserts a shard body with its ETag, stamping synced_at.
func (d *DB) SaveShard(ctx context.Context, key, etag, jsonBody string) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO shards(key, etag, json, synced_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
		  etag = excluded.etag, json = excluded.json, synced_at = excluded.synced_at`,
		key, etag, jsonBody, nowText())
	return err
}

// GetShard loads one cached shard.
func (d *DB) GetShard(ctx context.Context, key string) (ShardRow, bool, error) {
	var r ShardRow
	var etag, synced sql.NullString
	err := d.sql.QueryRowContext(ctx,
		`SELECT key, etag, json, synced_at FROM shards WHERE key = ?`, key).
		Scan(&r.Key, &etag, &r.JSON, &synced)
	if err == sql.ErrNoRows {
		return ShardRow{}, false, nil
	}
	if err != nil {
		return ShardRow{}, false, err
	}
	r.ETag = etag.String
	r.SyncedAt = parseTimeText(synced)
	return r, true, nil
}

// ListShards returns all cached shards ordered by key.
func (d *DB) ListShards(ctx context.Context) ([]ShardRow, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT key, etag, json, synced_at FROM shards ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShardRow
	for rows.Next() {
		var r ShardRow
		var etag, synced sql.NullString
		if err := rows.Scan(&r.Key, &etag, &r.JSON, &synced); err != nil {
			return nil, err
		}
		r.ETag = etag.String
		r.SyncedAt = parseTimeText(synced)
		out = append(out, r)
	}
	return out, rows.Err()
}
