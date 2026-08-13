package localdb

import (
	"context"
	"strings"
)

// DocHit is one FTS match. Score is the negated bm25 rank: higher is a
// better match.
type DocHit struct {
	DocID string
	Kind  string
	Score float64
}

// IndexDoc (re)indexes one searchable document. Delete-then-insert keeps
// the call idempotent — FTS5 has no ON CONFLICT.
func (d *DB) IndexDoc(ctx context.Context, docID, kind, title, body, packages, symbols, errorCodes string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM search_fts WHERE doc_id = ?`, docID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO search_fts(doc_id, kind, title, body, packages, symbols, error_codes)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		docID, kind, title, body, packages, symbols, errorCodes); err != nil {
		return err
	}
	return tx.Commit()
}

// FTSQuery runs a BM25-ranked full-text query. User input is never spliced
// into FTS5 syntax: each whitespace token becomes a quoted phrase and
// tokens are OR-combined, so metacharacters cannot alter the query.
func (d *DB) FTSQuery(ctx context.Context, q string, limit int) ([]DocHit, error) {
	match := ftsMatchExpr(q)
	if match == "" {
		return nil, nil
	}
	rows, err := d.sql.QueryContext(ctx, `
		SELECT doc_id, kind, bm25(search_fts) FROM search_fts
		WHERE search_fts MATCH ? ORDER BY rank LIMIT ?`, match, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocHit
	for rows.Next() {
		var h DocHit
		var rank float64
		if err := rows.Scan(&h.DocID, &h.Kind, &rank); err != nil {
			return nil, err
		}
		h.Score = -rank // bm25() is negative-better; flip to positive-better
		out = append(out, h)
	}
	return out, rows.Err()
}

// maxQueryTokens bounds query cost against pathological input.
const maxQueryTokens = 16

// ftsMatchExpr builds the safe MATCH expression, dropping tokens with no
// indexable characters (they would tokenize to an empty phrase).
func ftsMatchExpr(q string) string {
	var parts []string
	for _, tok := range strings.Fields(q) {
		if !strings.ContainsFunc(tok, isIndexable) {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
		if len(parts) == maxQueryTokens {
			break
		}
	}
	return strings.Join(parts, " OR ")
}

// isIndexable approximates the unicode61 tokenizer's token characters.
func isIndexable(r rune) bool {
	return r == '_' ||
		('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') ||
		r > 127
}
