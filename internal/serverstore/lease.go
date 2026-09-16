package serverstore

// Builder lease: the leader lock that lets exactly one compatibility Builder
// process run the aggregation pipeline at a time (CSX-451).
//
// Splitting the Builder into its own OS process means a deploy can briefly
// run two of them side by side -- the old one has not exited yet, or the
// service manager started the new one before the old one saw its signal.
// Without a lock, both processes work the same corpus at once: identical
// writes, doubled PostgreSQL load exactly when a deploy has already added
// load, and no guarantee the two converge on the same shard contents at
// their overlapping commit.
//
// The lease lives in PostgreSQL rather than in either process's memory,
// because PostgreSQL is the one thing both processes already depend on and
// already agree on the state of. It is a lease, not a lock a session holds:
// nothing requires the winning process to keep a connection open, so a
// process that is merely slow between checkouts cannot lose its own lease to
// itself, and a process that crashed outright is reclaimed once its lease
// expires rather than held forever by a connection nobody is watching.
//
// A fencing token (fence) travels with every acquisition and is required on
// every renew and release. It is what turns "the row's owner column says
// their name" into a guarantee: a network stall that makes process A believe
// it still holds the lease cannot let A's delayed renew silently re-extend a
// lease process B already took over, because B's takeover incremented fence
// and A is still presenting the old one.
//
// Callers classify their own context, exactly like every other Store method
// -- this file adds no class of its own. cmd/csx-builder runs its whole
// lease lifecycle under ClassBackground, the same class its aggregation
// passes already use.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrLeaseHeld is returned by AcquireBuilderLease when another owner holds
// an unexpired lease under the same name.
var ErrLeaseHeld = errors.New("serverstore: builder lease is held by another owner")

// ErrLeaseLost is returned by RenewBuilderLease and ReleaseBuilderLease when
// the caller's fence no longer matches the stored lease -- it expired and was
// taken over, or was released and reacquired, between the caller's last
// successful call and this one.
var ErrLeaseLost = errors.New("serverstore: builder lease was lost (expired, or taken over by another owner)")

// BuilderLeaseState is one named lease as PostgreSQL currently has it. It is
// returned on every successful Acquire/Renew so a caller always has the
// fence its next call must present, and by GetBuilderLease for read-only
// status reporting (the /progress endpoint, the admin panel).
type BuilderLeaseState struct {
	Name       string
	Owner      string
	Fence      int64
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

// Expired reports whether this lease state is stale as of now -- the same
// test PostgreSQL applies when deciding whether a new owner may take over.
func (s BuilderLeaseState) Expired(now time.Time) bool { return !now.Before(s.ExpiresAt) }

// AcquireBuilderLease creates a fresh lease, or takes over an expired one
// under the same name. It succeeds immediately for an owner that already
// holds the lease (a restart of the renew loop after a transient error must
// not need to wait out the old lease first). It returns ErrLeaseHeld when a
// different owner holds an unexpired lease.
func (p *PG) AcquireBuilderLease(ctx context.Context, name, owner string, ttl time.Duration) (BuilderLeaseState, error) {
	if ttl <= 0 {
		return BuilderLeaseState{}, fmt.Errorf("serverstore: builder lease ttl must be positive, got %s", ttl)
	}
	var st BuilderLeaseState
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		row := c.QueryRow(ctx, `
			INSERT INTO builder_lease (name, owner, fence, acquired_at, expires_at)
			VALUES ($1, $2, 1, now(), now() + $3 * interval '1 second')
			ON CONFLICT (name) DO UPDATE
				SET owner = EXCLUDED.owner,
					fence = builder_lease.fence + 1,
					acquired_at = now(),
					expires_at = now() + $3 * interval '1 second'
				WHERE builder_lease.expires_at < now() OR builder_lease.owner = EXCLUDED.owner
			RETURNING name, owner, fence, acquired_at, expires_at`,
			name, owner, ttl.Seconds())
		return row.Scan(&st.Name, &st.Owner, &st.Fence, &st.AcquiredAt, &st.ExpiresAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return BuilderLeaseState{}, ErrLeaseHeld
	}
	if err != nil {
		return BuilderLeaseState{}, fmt.Errorf("serverstore: acquire builder lease: %w", err)
	}
	return st, nil
}

// RenewBuilderLease extends a lease the caller believes it still holds. It
// fails with ErrLeaseLost when name+owner+fence no longer match the stored
// row -- the only condition under which that happens is the lease having
// expired and been taken over (or released and reacquired) since the
// caller's last successful Acquire/Renew.
func (p *PG) RenewBuilderLease(ctx context.Context, name, owner string, fence int64, ttl time.Duration) (BuilderLeaseState, error) {
	if ttl <= 0 {
		return BuilderLeaseState{}, fmt.Errorf("serverstore: builder lease ttl must be positive, got %s", ttl)
	}
	var st BuilderLeaseState
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		row := c.QueryRow(ctx, `
			UPDATE builder_lease
			SET expires_at = now() + $4 * interval '1 second'
			WHERE name = $1 AND owner = $2 AND fence = $3
			RETURNING name, owner, fence, acquired_at, expires_at`,
			name, owner, fence, ttl.Seconds())
		return row.Scan(&st.Name, &st.Owner, &st.Fence, &st.AcquiredAt, &st.ExpiresAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return BuilderLeaseState{}, ErrLeaseLost
	}
	if err != nil {
		return BuilderLeaseState{}, fmt.Errorf("serverstore: renew builder lease: %w", err)
	}
	return st, nil
}

// ReleaseBuilderLease gives up a lease the caller holds, so the next
// acquirer does not have to wait out the remaining TTL. It is best-effort:
// callers must still rely on TTL expiry for recovery, because a process that
// dies (rather than shutting down cleanly) never calls it at all.
func (p *PG) ReleaseBuilderLease(ctx context.Context, name, owner string, fence int64) error {
	var rows int64
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, `
			DELETE FROM builder_lease WHERE name = $1 AND owner = $2 AND fence = $3`,
			name, owner, fence)
		if err != nil {
			return err
		}
		rows = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return fmt.Errorf("serverstore: release builder lease: %w", err)
	}
	if rows == 0 {
		return ErrLeaseLost
	}
	return nil
}

// PauseBuilderLease tells whoever holds (or next takes) this lease to stop
// starting passes, for ttl (CSX-454, migration 0046). It is the resource
// governor's control, so it is deliberately NOT fenced the way renew and
// release are: the process that pauses is csx-server, which never holds this
// lease, and requiring the fencing token would mean the only process allowed
// to shed the Builder's load is the Builder itself.
//
// What keeps that safe is the ttl. The pause is a deadline the caller must
// keep refreshing; a governor that dies stops refreshing, the deadline
// passes, and the Builder resumes without anyone intervening. Nothing here
// touches owner, fence, acquired_at or expires_at, so a paused lease expires
// and is reclaimed on exactly the schedule an unpaused one does -- a crashed
// Builder is still recovered by TTL while paused, which was CSX-451's whole
// point.
//
// When no row exists yet -- no Builder has ever run against this database --
// it inserts a placeholder that is already expired, so the first real
// Builder takes it over normally and sees the pause it would otherwise have
// missed.
func (p *PG) PauseBuilderLease(ctx context.Context, name string, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("serverstore: builder pause ttl must be positive, got %s", ttl)
	}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO builder_lease (name, owner, fence, acquired_at, expires_at, paused_until)
			VALUES ($1, $2, 0, now(), now(), now() + $3 * interval '1 second')
			ON CONFLICT (name) DO UPDATE
				SET paused_until = EXCLUDED.paused_until`,
			name, pauseHolder, ttl.Seconds())
		return err
	})
	if err != nil {
		return fmt.Errorf("serverstore: pause builder lease: %w", err)
	}
	return nil
}

// ResumeBuilderLease clears the pause immediately instead of waiting out its
// deadline. It is a no-op when the lease is not paused, or does not exist.
func (p *PG) ResumeBuilderLease(ctx context.Context, name string) error {
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			UPDATE builder_lease SET paused_until = NULL
			WHERE name = $1 AND paused_until IS NOT NULL`, name)
		return err
	})
	if err != nil {
		return fmt.Errorf("serverstore: resume builder lease: %w", err)
	}
	return nil
}

// BuilderLeasePaused reports whether this lease is paused as of now. Any
// caller may read it; it takes nothing and changes nothing.
func (p *PG) BuilderLeasePaused(ctx context.Context, name string) (bool, error) {
	paused := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		row := c.QueryRow(ctx, `
			SELECT COALESCE(paused_until > now(), false)
			FROM builder_lease WHERE name = $1`, name)
		err := row.Scan(&paused)
		if errors.Is(err, pgx.ErrNoRows) {
			paused = false
			return nil
		}
		return err
	})
	if err != nil {
		return false, fmt.Errorf("serverstore: read builder lease pause: %w", err)
	}
	return paused, nil
}

// pauseHolder is the owner written on a placeholder row created by a pause
// that found no lease at all. The row is inserted already expired, so it
// holds nothing and the first real Builder takes it over; the name exists
// only so an operator reading the table sees who wrote the row.
const pauseHolder = "csx-server-governor"

// GetBuilderLease reads a named lease's current state without taking or
// affecting it, for status reporting.
func (p *PG) GetBuilderLease(ctx context.Context, name string) (BuilderLeaseState, bool, error) {
	var st BuilderLeaseState
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		row := c.QueryRow(ctx, `
			SELECT name, owner, fence, acquired_at, expires_at
			FROM builder_lease WHERE name = $1`, name)
		err := row.Scan(&st.Name, &st.Owner, &st.Fence, &st.AcquiredAt, &st.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return BuilderLeaseState{}, false, fmt.Errorf("serverstore: get builder lease: %w", err)
	}
	return st, found, nil
}
