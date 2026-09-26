package serverstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// BuilderStatusStore keeps the compatibility Builder's own account of its
// passes (#517): the last success, the last failure and why, and the cursor
// of an exhaustive repair that is being walked in chunks.
//
// It is an optional capability rather than a Store method, the pattern
// builder test doubles already rely on: a store without it simply has no
// status to publish, and the Builder keeps the cursor in memory only.
type BuilderStatusStore interface {
	GetBuilderStatus(ctx context.Context, name string) (statusJSON string, ok bool, err error)
	PutBuilderStatus(ctx context.Context, name, statusJSON string) error
}

func (p *PG) GetBuilderStatus(ctx context.Context, name string) (string, bool, error) {
	var js string
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		err := c.QueryRow(ctx, `SELECT status::text FROM builder_status WHERE name = $1`, name).Scan(&js)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return js, found, err
}

func (p *PG) PutBuilderStatus(ctx context.Context, name, statusJSON string) error {
	return p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO builder_status(name, status, updated_at)
			VALUES($1, $2, now())
			ON CONFLICT (name) DO UPDATE SET status = EXCLUDED.status, updated_at = EXCLUDED.updated_at`,
			name, []byte(statusJSON))
		return err
	})
}

func (f *Fake) GetBuilderStatus(_ context.Context, name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	js, ok := f.builderStatus[name]
	return js, ok, nil
}

func (f *Fake) PutBuilderStatus(_ context.Context, name, statusJSON string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.builderStatus == nil {
		f.builderStatus = map[string]string{}
	}
	f.builderStatus[name] = statusJSON
	return nil
}
