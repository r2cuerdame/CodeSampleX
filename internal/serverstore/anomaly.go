package serverstore

import (
	"context"
	"time"
)

// AnomalyCooldown is how long the same fingerprint waits before a repeat
// submission is worth anything to anybody.
//
// It does not refuse the repeat — a duplicate is always answered with the
// report it duplicates, which is the useful answer — it is what the response
// tells the caller to wait before saying it again. The dedupe key already
// prevents a second verification job; this exists so an agent in a retry loop
// is told to stop rather than left to discover it.
const AnomalyCooldown = 6 * time.Hour

// AnomalyReportRow is one stored verification request.
//
// Note what is NOT here: no reporter identity beyond the rotating anonymous
// bucket every other anonymous write on this server already uses, and no
// place for the report to become public. A row here is a question, and it
// stays a question until a signed receipt answers it.
type AnomalyReportRow struct {
	ID          int64
	Fingerprint string
	// ReportJSON is the canonical domain.AnomalyReport as submitted, after
	// normalization and redaction.
	ReportJSON  string
	AnomalyType string
	PURL        string
	Symbol      string
	// SampleID is the sample this report contests, when it contests one.
	// Empty means there is nothing this network can hand a verifier.
	SampleID string
	Status   string
	Verdict  string
	// UnsupportedReason says, in a sentence an operator and an agent can
	// both read, why no lane can reproduce this. A report that cannot be
	// verified must SAY so rather than sit in the queue looking busy.
	UnsupportedReason string
	// JobID is the verification job created for this report, 0 when none.
	JobID int64
	// Reports counts submissions of this fingerprint, first included.
	Reports int64
	// ReporterBucket is the rotating anonymous id of the FIRST reporter.
	// It exists to detect one client flooding, never to identify anyone.
	ReporterBucket string
	FirstSeen      time.Time
	LastSeen       time.Time
	VerdictAt      time.Time
}

// AnomalyVerdictCounts is one verdict bucket in the operator view.
type AnomalyVerdictCounts struct {
	Verdict string
	Count   int64
}

// AnomalyInsights is the bounded operator view of the feedback channel:
// how much is coming in, how much of it is the same thing said twice, how
// much of it turned out to be real, and how long the answer took.
type AnomalyInsights struct {
	WindowStart time.Time
	WindowEnd   time.Time
	// Reports counts submissions, Unique counts distinct fingerprints.
	// Duplicates is the difference, and their ratio is the honest measure
	// of whether the channel is being used or hammered.
	Reports    int64
	Unique     int64
	Duplicates int64

	Queued      int64
	Verifying   int64
	Verified    int64
	Unsupported int64

	Verdicts     []AnomalyVerdictCounts
	Confirmed    int64
	CSXDefects   int64
	Insufficient int64

	// VerdictLatencyTotal / VerdictLatencyCount give the mean; Max is the
	// tail an operator actually notices. A mean with no count behind it is
	// not reported as zero, it is reported as absent.
	VerdictLatencyTotal time.Duration
	VerdictLatencyCount int64
	VerdictLatencyMax   time.Duration

	// BusiestReporter is the highest submission count from a single
	// anonymous bucket in the window. It is a number, never a name.
	BusiestReporter int64
}

// MeanVerdictLatency reports the average report→verdict time and whether
// anything was measured at all.
func (i AnomalyInsights) MeanVerdictLatency() (time.Duration, bool) {
	if i.VerdictLatencyCount == 0 {
		return 0, false
	}
	return i.VerdictLatencyTotal / time.Duration(i.VerdictLatencyCount), true
}

// AnomalyStore is the ingest and verdict half of the feedback channel.
//
// It is part of Store rather than a side table with its own life, because
// the verdict path has to read verification jobs and receipts in the same
// transaction-shaped world those already live in.
type AnomalyStore interface {
	// RecordAnomalyReport stores a report, or recognizes it as one already
	// held. duplicate=true returns the EXISTING row untouched except for its
	// submission count and last-seen time — deliberately, because that row
	// already owns whatever verification job exists, and a duplicate must
	// not be able to queue a second one.
	RecordAnomalyReport(ctx context.Context, row AnomalyReportRow, now time.Time) (stored AnomalyReportRow, duplicate bool, err error)
	// AnomalyReportByID reads one report.
	AnomalyReportByID(ctx context.Context, id int64) (AnomalyReportRow, bool, error)
	// AttachAnomalyVerificationJob binds a report to the job that will
	// answer it and moves it to that status.
	AttachAnomalyVerificationJob(ctx context.Context, id, jobID int64, status, unsupportedReason string) error
	// SetAnomalyVerdict closes a report. It is a no-op on a report that
	// already has a verdict: a second receipt for the same sample answers a
	// different question and must not rewrite an answer already given.
	SetAnomalyVerdict(ctx context.Context, id int64, verdict string, at time.Time) (bool, error)
	// OpenAnomalyReportsForSample lists the reports still waiting on a
	// verdict for one sample, oldest first.
	OpenAnomalyReportsForSample(ctx context.Context, sampleID string) ([]AnomalyReportRow, error)
	// ListAnomalyReports returns the newest reports for the operator view.
	ListAnomalyReports(ctx context.Context, limit int) ([]AnomalyReportRow, error)
	// AnomalyInsights aggregates the last `days` days.
	AnomalyInsights(ctx context.Context, now time.Time, days int) (AnomalyInsights, error)
}
