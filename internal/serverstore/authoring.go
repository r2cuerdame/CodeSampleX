package serverstore

import (
	"context"
	"errors"
	"time"
)

type authoringPollContextKey struct{}

// WithAuthoringPoll marks the read half of /v1/authoring/work/next. It lets
// TopWanted use the fleet poll's database-owned statement ceiling without
// changing unrelated background/admin callers or the pool-guard-off contract.
func WithAuthoringPoll(ctx context.Context) context.Context {
	return context.WithValue(ctx, authoringPollContextKey{}, true)
}

func isAuthoringPoll(ctx context.Context) bool {
	marked, _ := ctx.Value(authoringPollContextKey{}).(bool)
	return marked
}

const MaxAuthoringSessions = 64

var (
	ErrAuthoringSessionLimit   = errors.New("too many active authoring sessions")
	ErrAuthoringSessionMissing = errors.New("authoring session unavailable")
	ErrAuthoringSessionExpired = errors.New("authoring session expired")
)

// AuthoringSessionRow is private operator state. TokenHash is a lowercase
// SHA-256 digest; the bearer itself is never stored. IP is retained only for
// the private admin list and never joins public evidence or access logs.
// authoringSiblingVersionsPerPackage caps how many unmeasured releases of one
// package may enter the candidate window.
//
// Every unmeasured sibling is a first job, so every one of them lands at
// version_depth 1. Uncapped, a package with a long release history fills the
// whole window with score-0 rows and pushes every other package's real work
// past the limit -- and because the window does not skip leased rows, the
// fleet then reads NO_WORK while thousands of candidates sit just outside it.
// Six matches the version axis the site actually renders.
const authoringSiblingVersionsPerPackage = 6

// authoringDirectWeight is how much more a directly-chosen sighting counts
// than a carried one. Mirrored in the PG query.
const authoringDirectWeight = 1000

// authoringResolveWeight is what one distinct project-day that RESOLVED this
// exact release is worth in the package-level score. Mirrored in the PG query.
//
// R2C-90. The two weights beside it are the ends of one scale: a carried
// sighting (1) is a machine mentioning a package, a chosen one
// (authoringDirectWeight) is a person deciding to use it. A resolved
// project-day sits between them and closer to the second -- it is a machine
// that actually installed this release, which is a fact about the world
// rather than about a manifest, but it is still nobody having asked for it.
//
// Ten resolved project-days for one chosen sighting is the ratio, and it is
// deterministic in both directions: a carried-only coordinate needs ten
// projects resolving it to overtake a coordinate one person listed once, and
// it can never overtake one that more than a tenth as many people chose.
// Measured against production on 2026-08-23, the busiest carried-only
// coordinate was resolved by 22 project-days and the median by 2, so this
// lifts the top of that distribution into the window without moving the tail
// past anybody's real demand.
//
// It is added to the sighting score rather than replacing it. A coordinate
// that is both chosen and widely resolved should rank above one that is only
// chosen, and no coordinate loses ground because this term exists.
const authoringResolveWeight = 100

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
	Ecosystem string
	Name      string
	Version   string
	Symbol    string
	Asks      int64
	Kind      string
	// Axis is the asset this assignment must produce. Kind remains where the
	// work came from, so demand/finding/expansion reporting stays compatible.
	Axis           string
	Score          int64
	SessionID      string
	ClaimedAt      time.Time
	LeaseExpiresAt time.Time
	SampleID       string
}

const (
	AuthoringAxisSample     = "SAMPLE"
	AuthoringAxisEvidence   = "EVIDENCE"
	AuthoringAxisDependency = "DEPENDENCY"
)

func normalizeAuthoringAxis(axis string) string {
	switch axis {
	case AuthoringAxisEvidence, AuthoringAxisDependency:
		return axis
	default:
		return AuthoringAxisSample
	}
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
	// ListAuthoringExpansionCandidatesUnhurried is the same read under a
	// budget sized for a background refresh rather than a request. On
	// production the read takes minutes -- it is ~700MB from disk on a host
	// whose cache holds 320MB -- and the 10s ceiling that is right for a
	// poll guaranteed it could never finish (#173). Nothing that answers a
	// caller directly may use this.
	ListAuthoringExpansionCandidatesUnhurried(ctx context.Context, limit int) ([]WantedRow, error)
	ClaimAuthoringWork(ctx context.Context, sessionID string, candidates []WantedRow, now, leaseExpiresAt time.Time) (AuthoringWorkRow, bool, error)
	AuthoringWorkForSubmission(ctx context.Context, sessionID, sampleID string, now time.Time) (AuthoringWorkRow, bool, error)
	AttachAuthoringWorkSample(ctx context.Context, sessionID string, work AuthoringWorkRow, sampleID string, now time.Time) (bool, error)
	// ReportAuthoringOutcome records how the work this session holds turned
	// out and hands the claim back. It returns the released work and false
	// when the session holds nothing: a report can only speak for work the
	// writer actually has.
	ReportAuthoringOutcome(ctx context.Context, sessionID string, outcome AuthoringOutcome, detail string, now time.Time) (AuthoringWorkRow, bool, error)
	// ListAuthoringQuarantine returns the coordinates currently withheld from
	// authoring, newest withholding first, with the reason and the evidence.
	ListAuthoringQuarantine(ctx context.Context, now time.Time, limit int) ([]AuthoringAttemptState, error)
	// AuthoringAttemptState reads one coordinate's ledger, withheld or not.
	AuthoringAttemptState(ctx context.Context, ecosystem, name, version, symbol string) (AuthoringAttemptState, bool, error)
	// ReopenAuthoringQuarantine puts a withheld coordinate back on the board
	// and resets the counters that took it off. It returns false when nothing
	// was withheld, which is not an error.
	ReopenAuthoringQuarantine(ctx context.Context, ecosystem, name, version, symbol string, now time.Time) (bool, error)
}

// AuthoringCompletenessStore rechecks cached axis candidates against the
// live tables just before a claim. Candidate discovery is intentionally
// cached for thirty minutes; without this cheap read, completed Sample,
// Evidence, or Dependency work would be handed out again until the next full
// refresh.
type AuthoringCompletenessStore interface {
	FilterIncompleteAuthoringCandidates(ctx context.Context, candidates []WantedRow) ([]WantedRow, error)
}
