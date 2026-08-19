package serverstore

import (
	"context"
	"time"
)

// AdminTokenRow is one long-lived operator API credential.
//
// Only the SHA-256 digest is ever stored, the same rule every other bearer in
// this server follows: a row is useful to nobody who does not already hold the
// token.
//
// A zero ExpiresAt means the token never expires on its own. That is a
// deliberate option rather than an oversight — an operator running a farm
// needs a credential that outlives any session — and it is exactly why
// LastUsedAt and LastUsedIP exist. A credential that cannot expire needs some
// other way for its owner to notice somebody else is using it.
type AdminTokenRow struct {
	TokenHash  string
	TokenID    string
	Label      string
	IssuedAt   time.Time
	ExpiresAt  time.Time // zero = never expires
	LastUsedAt time.Time
	LastUsedIP string
	RevokedAt  time.Time
}

// Live reports whether the row may still authorize a request.
func (r AdminTokenRow) Live(now time.Time) bool {
	if !r.RevokedAt.IsZero() {
		return false
	}
	return r.ExpiresAt.IsZero() || now.Before(r.ExpiresAt)
}

// AdminTokenStore keeps operator API credentials across restarts.
type AdminTokenStore interface {
	IssueAdminTokens(ctx context.Context, rows []AdminTokenRow) error
	// ResolveAdminToken validates a presented digest and, on success, records
	// that it was used. Recording is part of resolving because a token that
	// never expires is only observable through its use.
	ResolveAdminToken(ctx context.Context, tokenHash, ip string, now time.Time) (AdminTokenRow, bool, error)
	ListAdminTokens(ctx context.Context, limit int) ([]AdminTokenRow, error)
	RevokeAdminToken(ctx context.Context, tokenID string, now time.Time) (bool, error)
}

// MaxAdminTokens bounds how many live credentials may exist at once, so an
// authenticated browser cannot mint an unbounded set of permanent keys.
const MaxAdminTokens = 64
