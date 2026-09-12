package localdb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const evidenceStatsBudget = 5 * time.Second

// EvidenceStats is the bounded evidence/upload part of the local dashboard.
// Counts retain the existing 1000-per-source diagnostic cap. Upload metadata
// describes the evidence uploader, not every typed report's delivery time.
type EvidenceStats struct {
	QueueDepth int `json:"queueDepth"`
	Queue      struct {
		EvidenceBatches int `json:"evidenceBatches"`
		Uploads         int `json:"uploads"`
	} `json:"queue"`
	EvidenceRefusedTerminal int    `json:"evidenceRefusedTerminal"`
	LastUpload              string `json:"lastUpload,omitempty"`
	LastUploadAttempt       string `json:"lastUploadAttempt,omitempty"`
	LastUploadError         string `json:"lastUploadError,omitempty"`
}

// evidenceStatsDSN escapes the filesystem path separately from URI options;
// a profile named with '#' or '?' cannot change mode=ro or add pragmas.
func evidenceStatsDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	name := filepath.ToSlash(abs)
	if !strings.HasPrefix(name, "/") {
		name = "/" + name // Windows drive-letter file URI.
	}
	u := url.URL{Scheme: "file", Path: name}
	q := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(250)"}}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ReadEvidenceStats deliberately does not call Open: no initialization,
// migration, activation/backfill, daemon, CAS walk or data writes belong in
// this diagnostic. mode=ro still uses SQLite's normal WAL reader bookkeeping;
// immutable=1 would incorrectly hide concurrent committed WAL data.
//
// There is no schema-version shortcut: the narrow required queries validate
// their actual tables/columns/indexes. Any failed read discards the whole view.
func ReadEvidenceStats(ctx context.Context, path string) (*EvidenceStats, error) {
	ctx, cancel := context.WithTimeout(ctx, evidenceStatsBudget)
	defer cancel()
	dsn, err := evidenceStatsDSN(path)
	if err != nil {
		return nil, err
	}
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer sdb.Close()
	// The existing accessors use sql.DB. One private connection lets them
	// share a deferred read transaction without changing every store method.
	sdb.SetMaxOpenConns(1)
	sdb.SetMaxIdleConns(1)
	if _, err := sdb.ExecContext(ctx, "BEGIN"); err != nil {
		return nil, err
	}
	// Closing the private connection rolls back this read-only transaction.
	db := &DB{sql: sdb}
	out := &EvidenceStats{}
	if out.Queue.EvidenceBatches, err = db.PendingObservationCount(ctx, 1000); err != nil {
		return nil, err
	}
	queued, err := db.QueuePendingCounts(ctx, 1000)
	if err != nil {
		return nil, err
	}
	out.Queue.Uploads = queued.Total
	out.QueueDepth = out.Queue.EvidenceBatches + out.Queue.Uploads
	if out.EvidenceRefusedTerminal, err = db.RefusedEvidenceCount(ctx); err != nil {
		return nil, err
	}
	if out.LastUpload, err = evidenceUploadTime(ctx, db, "lastUpload"); err != nil {
		return nil, err
	}
	if out.LastUploadAttempt, err = evidenceUploadTime(ctx, db, "lastUploadAttempt"); err != nil {
		return nil, err
	}
	raw, err := evidenceUploadMeta(ctx, db, "lastUploadError")
	if err != nil {
		return nil, err
	}
	if out.LastUploadError, err = publicEvidenceUploadError(raw); err != nil {
		return nil, err
	}
	return out, nil
}

func evidenceUploadMeta(ctx context.Context, db *DB, key string) (string, error) {
	const maxRunes = 512
	const maxBytes = maxRunes * utf8.UTFMax
	var value []byte
	var byteLen int64
	// TEXT substr/length stop at NUL. Read a bounded BLOB prefix and the
	// full byte length instead, so validation includes every stored byte
	// before accepting text. A valid 512-rune UTF-8 value fits in maxBytes;
	// one extra byte detects overflow without returning unbounded data.
	err := db.sql.QueryRowContext(ctx,
		`SELECT substr(CAST(value AS BLOB), 1, ?), length(CAST(value AS BLOB)) FROM meta WHERE key = ?`,
		maxBytes+1, statPrefix+key).Scan(&value, &byteLen)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil // Never measured is distinct from a failed query.
	}
	if err != nil {
		return "", err
	}
	if byteLen > maxBytes || byteLen != int64(len(value)) ||
		bytes.IndexByte(value, 0) >= 0 || !utf8.Valid(value) || utf8.RuneCount(value) > maxRunes {
		return "", errors.New("invalid evidence upload metadata")
	}
	return string(value), nil
}

func evidenceUploadTime(ctx context.Context, db *DB, key string) (string, error) {
	value, err := evidenceUploadMeta(ctx, db, key)
	if err != nil || value == "" {
		return value, err
	}
	stamp, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", errors.New("invalid evidence upload timestamp")
	}
	return stamp.UTC().Format(time.RFC3339Nano), nil
}

var evidenceRefusalPrefix = regexp.MustCompile(`^evidence: the server refused ([1-9][0-9]*) (batch: |batches, first: )`)

func publicEvidenceUploadError(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if !strings.HasPrefix(raw, "evidence: the server refused ") {
		return "upload failed", nil
	}
	match := evidenceRefusalPrefix.FindStringSubmatch(raw)
	if match == nil {
		return "", errors.New("invalid evidence refusal count")
	}
	count, err := strconv.ParseInt(match[1], 10, 32)
	if err != nil || (count == 1) != (match[2] == "batch: ") {
		return "", errors.New("invalid evidence refusal count")
	}
	// Preserve the Farm-readable refusal count, never the stored raw reason.
	if count == 1 {
		return "evidence: the server refused 1 batch", nil
	}
	return fmt.Sprintf("evidence: the server refused %d batches", count), nil
}
