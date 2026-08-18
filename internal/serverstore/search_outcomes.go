package serverstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SearchOutcome is the complete persisted classification of one successful
// public search response. It intentionally cannot carry the request, package,
// symbol, path, client, or an identity.
type SearchOutcome string

const (
	SearchOutcomeSampleHit SearchOutcome = "sample_hit"
	SearchOutcomeNoMatch   SearchOutcome = "no_match"
)

// SearchOutcomeRecorder is optional at the HTTP boundary. Search remains
// available with alternate stores that do not implement operational metrics.
type SearchOutcomeRecorder interface {
	RecordSearchOutcome(ctx context.Context, at time.Time, outcome SearchOutcome) error
}

var _ SearchOutcomeRecorder = (*PG)(nil)

// RecordSearchOutcome atomically adds one result to its UTC daily aggregate.
// A storage failure is returned to the caller, which deliberately treats this
// analytics write as best-effort rather than failing an otherwise valid search.
func (p *PG) RecordSearchOutcome(ctx context.Context, at time.Time, outcome SearchOutcome) error {
	var sampleHits, noMatches int64
	switch outcome {
	case SearchOutcomeSampleHit:
		sampleHits = 1
	case SearchOutcomeNoMatch:
		noMatches = 1
	default:
		return fmt.Errorf("serverstore: unsupported search outcome %q", outcome)
	}
	day := at.UTC().Format("2006-01-02")
	return p.withConn(ctx, func(conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `
			INSERT INTO search_outcomes_daily(day, sample_hits, no_matches, updated_at)
			VALUES($1::date, $2, $3, $4)
			ON CONFLICT(day) DO UPDATE SET
				sample_hits = search_outcomes_daily.sample_hits + EXCLUDED.sample_hits,
				no_matches = search_outcomes_daily.no_matches + EXCLUDED.no_matches,
				updated_at = GREATEST(search_outcomes_daily.updated_at, EXCLUDED.updated_at)`,
			day, sampleHits, noMatches, at.UTC())
		return err
	})
}
