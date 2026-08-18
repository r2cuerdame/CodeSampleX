package serverstore

import (
	"context"
	"errors"
	"time"
)

const MaxAuthoringSessions = 64

var (
	ErrAuthoringSessionLimit   = errors.New("too many active authoring sessions")
	ErrAuthoringSessionMissing = errors.New("authoring session unavailable")
	ErrAuthoringSessionExpired = errors.New("authoring session expired")
)

// AuthoringSessionRow is private operator state. TokenHash is a lowercase
// SHA-256 digest; the bearer itself is never stored. IP is retained only for
// the private admin list and never joins public evidence or access logs.
type AuthoringSessionRow struct {
	TokenHash     string
	SessionID     string
	Label         string
	Model         string
	Reasoning     string
	IssuedAt      time.Time
	LastRefreshAt time.Time
	IdleExpiresAt time.Time
	LastRefreshIP string
	ComputerName  string
	RevokedAt     time.Time
}

// AuthoringSessionStore keeps internal authoring sessions alive across server
// restarts while preserving their one-hour idle expiry and individual revoke
// boundary.
type AuthoringSessionStore interface {
	IssueAuthoringSessions(ctx context.Context, rows []AuthoringSessionRow, now time.Time) error
	RotateAuthoringSession(ctx context.Context, sessionID, tokenHash string, now, idleExpiresAt time.Time) (AuthoringSessionRow, error)
	RefreshAuthoringSession(ctx context.Context, tokenHash, ip, computerName string, now, idleExpiresAt time.Time) (AuthoringSessionRow, error)
	RevokeAuthoringSession(ctx context.Context, sessionID string, now time.Time) (bool, error)
	ListAuthoringSessions(ctx context.Context, now time.Time, limit int) ([]AuthoringSessionRow, error)
}
