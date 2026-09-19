package serverstore

import (
	"context"
	"errors"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
)

// Fix-claim verification (#444): upstream bug-fix claims as a bounded work
// queue whose only way forward is receipted execution.
//
// The queue is deliberately its own table rather than rows in the
// authoring work lane. A fix candidate is not a coordinate to write a
// sample FOR; it is a pair of coordinates to run the same reproducer
// AGAINST, and it carries provenance, a running evaluation and a lease.
// What it shares with the authoring lane is everything that matters for
// trust: the same writer sessions, the same lease-and-outcome shape, the
// same receipts as the only admissible evidence, and an independent
// capacity ceiling (ServerConfig.FixWorkMaxLeases) so speculative
// verification can never starve WANTED, EXPANSION or DEPENDENCY work.

// FixCandidateRow is one queued FIX_CANDIDATE with its state.
type FixCandidateRow struct {
	ID        int64
	DedupKey  string
	Candidate fixclaims.Candidate
	Status    fixclaims.Status
	// PairOutcome is the evaluator's pair reading, kept beside the status
	// because REPRODUCED_BUG with a fixed side that failed the same way and
	// REPRODUCED_BUG with an untested fixed side are different facts.
	PairOutcome fixclaims.PairOutcome
	Score       int64
	// Attempts counts handouts, ever. It is the hard cap for candidates
	// that never produce a signal.
	Attempts int
	// Closed takes the row off the board: the attempt cap, the run cap, or
	// the planner having nothing more to ask. The record and its evidence
	// stay readable; only work stops.
	Closed       bool
	ClosedReason string
	// ReproducerSource and ReproducerSampleID are what the worker resolved
	// (fixclaims.Resolve) and, once one exists, the sample it ran.
	ReproducerSource   fixclaims.ReproducerSource
	ReproducerSampleID string
	ClaimedBy          string
	ClaimedAt          time.Time
	LeaseExpiresAt     time.Time
	Evaluation         fixclaims.Evaluation
	RunCount           int
	FarmSeconds        int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	EvaluatedAt        time.Time
}

// State is the bounded measurement projection of the row.
func (r FixCandidateRow) State() fixclaims.CandidateState {
	return fixclaims.CandidateState{
		Status: r.Status, PairOutcome: r.PairOutcome, Attempts: r.Attempts, Exhausted: r.Closed,
		ReproducerSource: r.ReproducerSource, RunCount: r.RunCount, FarmSeconds: r.FarmSeconds,
	}
}

// Leased reports whether a live lease is held on the row at now.
func (r FixCandidateRow) Leased(now time.Time) bool {
	return r.ClaimedBy != "" && r.LeaseExpiresAt.After(now)
}

// FixRunRow is one stored run.
type FixRunRow struct {
	ID          int64
	CandidateID int64
	SessionID   string
	fixclaims.Run
	CreatedAt time.Time
}

// FixCandidateUpsert is the ingest's answer for one submitted candidate.
type FixCandidateUpsert struct {
	Row       FixCandidateRow
	Duplicate bool
}

// FixClaimQuery narrows ListFixCandidates. Version matches either claimed
// version, so "was X fixed in 2.4.1" and "is 2.4.0 known bad" both answer.
type FixClaimQuery struct {
	Ecosystem string
	Name      string
	Version   string
	Status    fixclaims.Status
	// Open restricts to rows that are not closed (the queue view).
	Open bool
}

// FixWorkOutcome is what a worker reports when it hands work back without
// runs. The names mirror the authoring outcomes on purpose.
type FixWorkOutcome string

const (
	// FixOutcomeNoReproducer: no sample exists, the upstream carries none,
	// and the worker could not write one that builds.
	FixOutcomeNoReproducer FixWorkOutcome = "NO_REPRODUCER"
	// FixOutcomeInfrastructure: the worker's own machine failed.
	FixOutcomeInfrastructure FixWorkOutcome = "INFRASTRUCTURE"
	// FixOutcomeTransient: a registry or toolchain would not answer.
	FixOutcomeTransient FixWorkOutcome = "TRANSIENT"
	// FixOutcomeNoOutput: gave up, cannot say which.
	FixOutcomeNoOutput FixWorkOutcome = "NO_OUTPUT"
	// FixOutcomeComplete is the server's own bookkeeping when the planner
	// has nothing more to ask; it is refused from a client.
	FixOutcomeComplete FixWorkOutcome = "COMPLETE"
)

// ValidFixWorkOutcome reports whether a client may report o.
func ValidFixWorkOutcome(o FixWorkOutcome) bool {
	switch o {
	case FixOutcomeNoReproducer, FixOutcomeInfrastructure, FixOutcomeTransient, FixOutcomeNoOutput:
		return true
	}
	return false
}

// Closed reasons.
const (
	FixClosedAttemptCap = "attempt-cap"
	FixClosedRunCap     = "run-cap"
	FixClosedComplete   = "complete"
)

// FixClaimLimits are the budget knobs the store applies. They are passed
// per call rather than held by the store so the fake and PostgreSQL apply
// exactly what the handler read from ServerConfig.
type FixClaimLimits struct {
	// MaxLeases is the independent capacity ceiling: how many candidates
	// may be leased at once across the whole fleet.
	MaxLeases int
	// MaxAttempts is the hard cap on handouts for a candidate that has not
	// produced a signal.
	MaxAttempts int
	// MaxRuns is the hard cap on runs per candidate (fixclaims.Policy).
	MaxRuns int
}

// DefaultFixClaimLimits are the Phase 0 numbers.
func DefaultFixClaimLimits() FixClaimLimits {
	return FixClaimLimits{MaxLeases: 2, MaxAttempts: 3, MaxRuns: fixclaims.DefaultPolicy().MaxRuns}
}

// FixClaimStatus is ClaimFixWork's answer.
type FixClaimStatus string

const (
	FixClaimAssigned        FixClaimStatus = "ASSIGNED"
	FixClaimNoWork          FixClaimStatus = "NO_WORK"
	FixClaimBudgetExhausted FixClaimStatus = "BUDGET_EXHAUSTED"
)

var (
	ErrFixCandidateMissing = errors.New("fix candidate not found")
	// ErrFixLeaseMissing: the session does not hold a live lease on the
	// candidate. A run can only speak for work the writer actually has.
	ErrFixLeaseMissing = errors.New("fix work lease not held")
)

// FixClaimStore is the optional capability behind /v1/fix-claims. A store
// that does not implement it makes the endpoints answer 503.
type FixClaimStore interface {
	// UpsertFixCandidates stores validated candidates. A candidate whose
	// DedupKey exists is reported as a duplicate and left as it is -- its
	// evidence is not reset by a second producer noticing the same fix.
	// rejected is the count the validator refused at the door, recorded
	// for extraction precision.
	UpsertFixCandidates(ctx context.Context, rows []FixCandidateRow, rejected int64, now time.Time) ([]FixCandidateUpsert, error)
	// ClaimFixWork leases the best open candidate to a session, or returns
	// the one it already holds. Every new handout counts an attempt.
	//
	// The order is breadth first: fewest attempts, then score, then age.
	// Every candidate gets its pair before any gets a third look, because
	// the Phase 0 question is how many claims the pipeline can take
	// end-to-end, and a verified fix waiting for a boundary walk is worth
	// less than an untested claim that could be the next one.
	ClaimFixWork(ctx context.Context, sessionID string, limits FixClaimLimits, now, leaseExpiresAt time.Time) (FixCandidateRow, FixClaimStatus, error)
	// SetFixReproducer records what the leased worker resolved.
	SetFixReproducer(ctx context.Context, id int64, sessionID string, source fixclaims.ReproducerSource, sampleID string, now time.Time) (FixCandidateRow, error)
	// RecordFixRuns appends runs, re-evaluates the candidate over every run
	// it has, releases the lease, and applies the run cap.
	RecordFixRuns(ctx context.Context, id int64, sessionID string, runs []fixclaims.Run, limits FixClaimLimits, now time.Time) (FixCandidateRow, error)
	// ReleaseFixWork hands leased work back without runs and applies the
	// attempt cap. FixOutcomeComplete closes the row.
	ReleaseFixWork(ctx context.Context, id int64, sessionID string, outcome FixWorkOutcome, detail string, limits FixClaimLimits, now time.Time) (FixCandidateRow, error)
	GetFixCandidate(ctx context.Context, id int64) (FixCandidateRow, bool, error)
	ListFixRuns(ctx context.Context, id int64) ([]FixRunRow, error)
	ListFixCandidates(ctx context.Context, q FixClaimQuery, limit int) ([]FixCandidateRow, error)
	// FixClaimStates is the measurement read: every row's bounded state
	// plus the ingest counters.
	FixClaimStates(ctx context.Context) (states []fixclaims.CandidateState, ingested, rejected int64, err error)
}

// settleFixCandidate applies the caps after a handout ends. Shared byte for
// byte by the fake and PostgreSQL so the two cannot drift.
func settleFixCandidate(row *FixCandidateRow, limits FixClaimLimits) {
	if limits.MaxRuns <= 0 {
		limits.MaxRuns = DefaultFixClaimLimits().MaxRuns
	}
	if limits.MaxAttempts <= 0 {
		limits.MaxAttempts = DefaultFixClaimLimits().MaxAttempts
	}
	if row.Closed {
		return
	}
	if row.RunCount >= limits.MaxRuns {
		row.Closed, row.ClosedReason = true, FixClosedRunCap
		return
	}
	if fixSignal(*row) {
		return
	}
	if row.Attempts >= limits.MaxAttempts {
		row.Closed, row.ClosedReason = true, FixClosedAttemptCap
	}
}

// fixSignal reports whether the evidence so far gives the planner
// something to expand from. Without one, the attempt cap is what retires
// the candidate.
func fixSignal(row FixCandidateRow) bool {
	if row.Status.Verified() || row.Status == fixclaims.StatusRegressed {
		return true
	}
	if row.Status == fixclaims.StatusReproducedBug {
		return row.PairOutcome == fixclaims.PairPending || row.PairOutcome == fixclaims.PairReproducedNotFixed
	}
	return false
}

// applyFixRuns re-evaluates a candidate over all its runs.
func applyFixRuns(row *FixCandidateRow, runs []fixclaims.Run, now time.Time) {
	ev := fixclaims.Evaluate(row.Candidate, runs)
	row.Evaluation = ev
	row.Status = ev.Status
	row.PairOutcome = ev.PairOutcome
	row.RunCount = len(runs)
	row.FarmSeconds = 0
	for _, r := range runs {
		row.FarmSeconds += r.FarmSeconds
	}
	row.EvaluatedAt = now
	row.UpdatedAt = now
	if row.ReproducerSampleID == "" && len(ev.SampleIDs) > 0 {
		row.ReproducerSampleID = ev.SampleIDs[0]
	}
}
