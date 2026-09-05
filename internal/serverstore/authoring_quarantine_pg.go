package serverstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// authoringExec is the part of a connection or transaction the attempt ledger
// needs. Both *pgx.Conn and pgx.Tx satisfy it, so the same load/save pair runs
// inside the claim transaction and on its own.
type authoringExec interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// authoringQuerier adds the multi-row read the candidate window needs.
type authoringQuerier interface {
	authoringExec
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadAuthoringLedger reads one coordinate's ledger. A coordinate nobody has
// been handed yet has no row, and a nil ledger is the honest answer for it —
// creating an empty one on read would fill the table with rows that record
// nothing.
func loadAuthoringLedger(ctx context.Context, q authoringExec, ecosystem, name, version, symbol string) (*authoringLedger, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT ledger FROM authoring_attempts
		WHERE ecosystem=$1 AND name=$2 AND version=$3 AND symbol=$4`,
		ecosystem, name, version, symbol).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeAuthoringLedger(raw, ecosystem, name, version, symbol)
}

func decodeAuthoringLedger(raw []byte, ecosystem, name, version, symbol string) (*authoringLedger, error) {
	ledger := newAuthoringLedger(ecosystem, name, version, symbol)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, ledger); err != nil {
			return nil, err
		}
	}
	// The key is authoritative: a document written under an older shape must
	// still answer for the coordinate it is filed under.
	ledger.Ecosystem, ledger.Name, ledger.Version, ledger.Symbol = ecosystem, name, version, symbol
	ledger.ensure()
	return ledger, nil
}

func saveAuthoringLedger(ctx context.Context, q authoringExec, l *authoringLedger, now time.Time) error {
	encoded, err := json.Marshal(l)
	if err != nil {
		return err
	}
	var quarantinedAt, reopensAt *time.Time
	if !l.QuarantinedAt.IsZero() {
		at := l.QuarantinedAt
		quarantinedAt = &at
	}
	if !l.ReopensAt.IsZero() {
		at := l.ReopensAt
		reopensAt = &at
	}
	_, err = q.Exec(ctx, `INSERT INTO authoring_attempts(
		ecosystem,name,version,symbol,ledger,quarantined_at,reopens_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(ecosystem,name,version,symbol) DO UPDATE SET
		  ledger=EXCLUDED.ledger, quarantined_at=EXCLUDED.quarantined_at,
		  reopens_at=EXCLUDED.reopens_at, updated_at=EXCLUDED.updated_at`,
		l.Ecosystem, l.Name, l.Version, l.Symbol, encoded, quarantinedAt, reopensAt, now)
	return err
}

// loadAuthoringLedgers reads every attempt ledger among these candidates in
// one round trip. The candidate window is up to four hundred rows and this
// runs inside the claim transaction on an endpoint the whole fleet polls, so
// asking per candidate is not an option.
func loadAuthoringLedgers(ctx context.Context, q authoringQuerier, candidates []WantedRow) (map[[4]string]*authoringLedger, error) {
	out := make(map[[4]string]*authoringLedger, len(candidates))
	if len(candidates) == 0 {
		return out, nil
	}
	ecosystems := make([]string, 0, len(candidates))
	names := make([]string, 0, len(candidates))
	versions := make([]string, 0, len(candidates))
	symbols := make([]string, 0, len(candidates))
	seen := make(map[[4]string]bool, len(candidates))
	for _, candidate := range candidates {
		key := authoringWorkKey(candidate.Ecosystem, candidate.Name, candidate.Version, candidate.Symbol)
		if seen[key] {
			continue
		}
		seen[key] = true
		ecosystems = append(ecosystems, key[0])
		names = append(names, key[1])
		versions = append(versions, key[2])
		symbols = append(symbols, key[3])
	}
	rows, err := q.Query(ctx, `SELECT ecosystem,name,version,symbol,ledger FROM authoring_attempts
		WHERE (ecosystem,name,version,symbol) IN (
		  SELECT * FROM unnest($1::text[],$2::text[],$3::text[],$4::text[]))`,
		ecosystems, names, versions, symbols)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ecosystem, name, version, symbol string
		var raw []byte
		if err := rows.Scan(&ecosystem, &name, &version, &symbol, &raw); err != nil {
			return nil, err
		}
		ledger, err := decodeAuthoringLedger(raw, ecosystem, name, version, symbol)
		if err != nil {
			return nil, err
		}
		out[authoringWorkKey(ecosystem, name, version, symbol)] = ledger
	}
	return out, rows.Err()
}

// noteAuthoringHandout opens an attempt against a coordinate inside whatever
// transaction handed the work out. ledger is the already-loaded row, or nil
// for a coordinate nobody has been handed before.
func noteAuthoringHandout(ctx context.Context, q authoringExec, ledger *authoringLedger, work AuthoringWorkRow, sessionID string, now time.Time) error {
	if ledger == nil {
		ledger = newAuthoringLedger(work.Ecosystem, work.Name, work.Version, work.Symbol)
	}
	ledger.handout(work.Kind, work.Axis, sessionID, now)
	return saveAuthoringLedger(ctx, q, ledger, now)
}

func (p *PG) ReportAuthoringOutcome(ctx context.Context, sessionID string, outcome AuthoringOutcome, detail string, now time.Time) (AuthoringWorkRow, bool, error) {
	var work AuthoringWorkRow
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		work, err = scanAuthoringWork(tx.QueryRow(ctx, `SELECT ecosystem,name,version,symbol,asks,kind,axis,score,
			session_id,claimed_at,lease_expires_at,sample_id FROM authoring_assignments
			WHERE session_id=$1 AND sample_id IS NULL AND lease_expires_at>$2`, sessionID, now))
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.Commit(ctx)
		}
		if err != nil {
			return err
		}
		ledger, err := loadAuthoringLedger(ctx, tx, work.Ecosystem, work.Name, work.Version, work.Symbol)
		if err != nil {
			return err
		}
		if ledger == nil {
			ledger = newAuthoringLedger(work.Ecosystem, work.Name, work.Version, work.Symbol)
		}
		ledger.report(sessionID, outcome, detail, now)
		if err := saveAuthoringLedger(ctx, tx, ledger, now); err != nil {
			return err
		}
		// The claim goes back immediately. A writer that has said what it
		// found should not also have to sit on the lease.
		if _, err := tx.Exec(ctx, `DELETE FROM authoring_assignments
			WHERE ecosystem=$1 AND name=$2 AND version=$3 AND symbol=$4
			  AND session_id=$5 AND sample_id IS NULL`,
			work.Ecosystem, work.Name, work.Version, work.Symbol, sessionID); err != nil {
			return err
		}
		found = true
		return tx.Commit(ctx)
	})
	if err != nil {
		return AuthoringWorkRow{}, false, err
	}
	return work, found, nil
}

func (p *PG) ListAuthoringQuarantine(ctx context.Context, now time.Time, limit int) ([]AuthoringAttemptState, error) {
	if limit < 1 {
		return nil, nil
	}
	if limit > 500 {
		limit = 500
	}
	var out []AuthoringAttemptState
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT ecosystem,name,version,symbol,ledger FROM authoring_attempts
			WHERE quarantined_at IS NOT NULL AND (reopens_at IS NULL OR reopens_at > $1)
			ORDER BY quarantined_at DESC,ecosystem,name,version,symbol LIMIT $2`, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ecosystem, name, version, symbol string
			var raw []byte
			if err := rows.Scan(&ecosystem, &name, &version, &symbol, &raw); err != nil {
				return err
			}
			ledger, err := decodeAuthoringLedger(raw, ecosystem, name, version, symbol)
			if err != nil {
				return err
			}
			// The SQL predicate and Withheld are the same rule stated twice;
			// asking the shared rule again is what keeps them honest.
			if ledger.Withheld(now) {
				out = append(out, ledger.state())
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	sortAuthoringQuarantine(out)
	return out, nil
}

func (p *PG) AuthoringAttemptState(ctx context.Context, ecosystem, name, version, symbol string) (AuthoringAttemptState, bool, error) {
	var state AuthoringAttemptState
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		ledger, err := loadAuthoringLedger(ctx, c, ecosystem, name, version, symbol)
		if err != nil || ledger == nil {
			return err
		}
		state = ledger.state()
		found = true
		return nil
	})
	return state, found, err
}

func (p *PG) ReopenAuthoringQuarantine(ctx context.Context, ecosystem, name, version, symbol string, now time.Time) (bool, error) {
	reopened := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		ledger, err := loadAuthoringLedger(ctx, tx, ecosystem, name, version, symbol)
		if err != nil || ledger == nil {
			return err
		}
		if !ledger.reopen(now) {
			return tx.Commit(ctx)
		}
		if err := saveAuthoringLedger(ctx, tx, ledger, now); err != nil {
			return err
		}
		reopened = true
		return tx.Commit(ctx)
	})
	return reopened, err
}
