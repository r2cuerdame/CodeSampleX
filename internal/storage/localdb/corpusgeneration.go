package localdb

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// corpusGenerationKey names the meta row the triggers below keep.
const corpusGenerationKey = "corpus_generation"

// corpusGenerationDDL keeps a counter that moves whenever the corpus the
// search engine reads — samples and shards — changes in any way.
//
// The engine re-reads and re-parses all of it on every query: 1,770 sample
// manifests and 974 shard rows holding 12 MB of JSON on the install this was
// measured on. It can only stop doing that if it can tell, cheaply and
// without ever being wrong, whether anything moved since it last looked.
//
// Triggers rather than the writers. A cache whose invalidation depends on
// somebody remembering to bump a number is a cache that will one day answer
// from a corpus that has moved — and a stale answer here is worse than a slow
// one, because it is indistinguishable from a true one. A trigger cannot be
// forgotten by the next person who adds a writer.
var corpusGenerationDDL = func() []string {
	var out []string
	bump := `INSERT INTO meta(key, value) VALUES('` + corpusGenerationKey + `', '1')
	         ON CONFLICT(key) DO UPDATE SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT);`
	for _, table := range []string{"samples", "shards"} {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			out = append(out, `CREATE TRIGGER IF NOT EXISTS `+
				table+`_corpus_generation_`+event+` AFTER `+event+` ON `+table+
				` BEGIN `+bump+` END`)
		}
	}
	return out
}()

// CorpusGeneration reports the current value of that counter.
//
// A missing row is generation zero rather than an error: a database that has
// never been written to has a corpus, and it is empty.
func (d *DB) CorpusGeneration(ctx context.Context) (int64, error) {
	var raw string
	err := d.sql.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = ?`, corpusGenerationKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.ParseInt(raw, 10, 64)
	if convErr != nil {
		// Unreadable means "assume it moved": the caller uses this to decide
		// whether a cache still holds, and the safe answer is that it does
		// not. Returning an error would take search down over a bad row.
		return -1, nil
	}
	return n, nil
}
