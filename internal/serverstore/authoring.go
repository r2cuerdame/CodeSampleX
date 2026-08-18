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

// AuthoringDraftRow is an internal, non-public sample uploaded by a sample
// worker. Its artifact is content-addressed in the server blob store, but it
// is not inserted into samples and therefore cannot appear in public search.
type AuthoringDraftRow struct {
	SampleID           string
	SessionID          string
	WorkerLabel        string
	ManifestJSON       string
	LocalStatus        string
	VerificationStatus string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type AuthoringWorkRow struct {
	Ecosystem      string
	Name           string
	Version        string
	Symbol         string
	Asks           int64
	Kind           string
	Score          int64
	SessionID      string
	ClaimedAt      time.Time
	LeaseExpiresAt time.Time
	SampleID       string
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
	SaveAuthoringDraft(ctx context.Context, row AuthoringDraftRow) error
	ListAuthoringDrafts(ctx context.Context, limit int) ([]AuthoringDraftRow, error)
	// ListAuthoringExpansionCandidates supplies ranked, exact public package
	// coordinates when every unresolved Wanted row is already being authored.
	// Failure-cluster symbols rank first, then observed package-version-symbol
	// coordinates that still have no independently verified sample.
	ListAuthoringExpansionCandidates(ctx context.Context, limit int) ([]WantedRow, error)
	ClaimAuthoringWork(ctx context.Context, sessionID string, candidates []WantedRow, now, leaseExpiresAt time.Time) (AuthoringWorkRow, bool, error)
	AuthoringWorkForSubmission(ctx context.Context, sessionID, sampleID string, now time.Time) (AuthoringWorkRow, bool, error)
	AttachAuthoringWorkSample(ctx context.Context, sessionID string, work AuthoringWorkRow, sampleID string, now time.Time) (bool, error)
}
