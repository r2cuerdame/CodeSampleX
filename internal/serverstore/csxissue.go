package serverstore

import (
	"context"
	"time"
)

// CSXIssueReportRow is one stored product-defect candidate.
//
// Occurrences is the field that makes the policy work. A defect many agents
// meet is ONE row whose count goes up — never a growing pile of tickets —
// and once an operator has linked that row to a canonical bug, every later
// occurrence answers with that link instead of creating anything.
type CSXIssueReportRow struct {
	ID          int64
	Fingerprint string
	ReportJSON  string
	Surface     string
	IssueKind   string
	Component   string
	Status      string
	Verdict     string
	// ReplayReason says why nothing here can re-run it, when nothing can.
	ReplayReason string
	// CanonicalRef is the existing bug this defect belongs to, e.g.
	// "R2C-51". Set by an operator, never by a reporter: a reporter that
	// could name the ticket could also aim reports at it.
	CanonicalRef   string
	Occurrences    int64
	ReporterBucket string
	FirstSeen      time.Time
	LastSeen       time.Time
	VerdictAt      time.Time
}

// CSXIssueInsights is the bounded operator view.
type CSXIssueInsights struct {
	WindowStart time.Time
	WindowEnd   time.Time

	Occurrences int64
	Unique      int64
	Duplicates  int64

	Triage       int64
	ReplayQueued int64
	NoReplayLane int64
	Resolved     int64

	Confirmed int64
	Linked    int64
}

// CSXIssueStore is the product-defect half of the feedback channel.
//
// It shares nothing with AnomalyStore after ingest, on purpose. An anomaly
// can become compatibility evidence; a product defect never can, and giving
// them one table would be the way that boundary eventually leaks.
type CSXIssueStore interface {
	// RecordCSXIssueReport stores a report or recognizes it as another
	// occurrence of one already held. duplicate=true returns the existing
	// row with its occurrence count incremented — including its canonical
	// ref, which is the answer a repeat reporter actually wants.
	RecordCSXIssueReport(ctx context.Context, row CSXIssueReportRow, now time.Time) (stored CSXIssueReportRow, duplicate bool, err error)
	CSXIssueReportByID(ctx context.Context, id int64) (CSXIssueReportRow, bool, error)
	// SetCSXIssueTriage records the replay decision for a report.
	SetCSXIssueTriage(ctx context.Context, id int64, status, replayReason string) error
	// SetCSXIssueVerdict closes a report. Like an anomaly verdict it never
	// overwrites one already given.
	SetCSXIssueVerdict(ctx context.Context, id int64, verdict string, at time.Time) (bool, error)
	// LinkCSXIssueCanonical attaches an existing bug reference. Only a
	// confirmed defect may be linked, which is enforced here rather than in
	// a handler so no second caller can forget it.
	LinkCSXIssueCanonical(ctx context.Context, id int64, ref string) (bool, error)
	ListCSXIssueReports(ctx context.Context, limit int) ([]CSXIssueReportRow, error)
	CSXIssueInsights(ctx context.Context, now time.Time, days int) (CSXIssueInsights, error)
}
