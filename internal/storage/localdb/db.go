// Package localdb is the local SQLite store behind the csx daemon and CLI
// ($CSX_HOME/csx.db). It holds the evidence aggregates, sample metadata,
// shard cache, FTS index, and upload queue. Everything here stays on the
// machine; only rows explicitly drained into wire batches ever leave it.
package localdb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite handle with typed accessors.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if necessary) the store at path and applies the
// schema. Safe to call on an existing database: migration is idempotent.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	// MCP servers, the daemon and parallel sample workers intentionally
	// share this database.  Five seconds was shorter than a real
	// sample-create transaction under load, so a correct second writer was
	// rejected with SQLITE_BUSY and the candidate had to be repaired by
	// hand.  WAL keeps readers concurrent; the longer timeout serializes the
	// small write section instead of turning ordinary parallelism into data
	// loss.
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db := &DB{sql: sdb}
	if err := db.migrate(context.Background()); err != nil {
		sdb.Close()
		return nil, err
	}
	// Derive the activation funnel from what this store already holds before
	// anything reads it. Best effort: a store that cannot answer is a store
	// whose funnel stays unmeasured, which is the honest reading anyway, and
	// no caller of Open should fail because a panel would be incomplete.
	_ = db.BackfillActivation(context.Background(), time.Now().UTC())
	return db, nil
}

// Close releases the underlying handle.
func (d *DB) Close() error { return d.sql.Close() }

// statPrefix namespaces dashboard counters inside the meta table so they
// cannot collide with schema bookkeeping keys.
const statPrefix = "stat:"

// SetStat stores one dashboard counter/value.
func (d *DB) SetStat(ctx context.Context, key, value string) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		statPrefix+key, value)
	return err
}

// GetStat reads one stat; ok is false when it was never set.
func (d *DB) GetStat(ctx context.Context, key string) (value string, ok bool, err error) {
	err = d.sql.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = ?`, statPrefix+key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// AllStats returns every stat key/value.
func (d *DB) AllStats(ctx context.Context) (map[string]string, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT key, value FROM meta WHERE key LIKE ?`, statPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[strings.TrimPrefix(k, statPrefix)] = v
	}
	return out, rows.Err()
}

// nowText is the stored form of "now": RFC3339 UTC, which sorts
// lexicographically in TEXT columns.
func nowText() string { return time.Now().UTC().Format(time.RFC3339) }

// timeArg converts a time to its stored TEXT form; zero becomes NULL.
func timeArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// parseTimeText converts a stored NULL/TEXT timestamp back; malformed or
// NULL values become the zero time rather than an error, since these
// columns are advisory metadata.
func parseTimeText(s sql.NullString) time.Time {
	if !s.Valid || s.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s.String)
	if err != nil {
		return time.Time{}
	}
	return t
}
