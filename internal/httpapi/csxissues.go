package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The product-defect half of the feedback channel.
//
// It shares this file's neighbour's ingest shape — validate, redact, dedupe
// by fingerprint, answer with what is already known — and shares nothing
// after it. An anomaly can become compatibility evidence; a defect in this
// product never can, so it gets its own table, its own verdicts and its own
// queue, and the two never meet.
//
// The policy it enforces is conservative on purpose: no automatic ticket, no
// instruction to agents to call it on every failure, and no target for how
// many reports a healthy week has. A defect a hundred agents meet is one row
// whose occurrence count goes up. Once an operator has linked that row to a
// canonical bug, every later report answers with the link.

type csxIssueEnvelope struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Epoch         string                `json:"epoch"`
	AnonID        string                `json:"anonId"`
	Report        domain.CSXIssueReport `json:"report"`
}

type csxIssueResponse struct {
	ReportID          int64  `json:"reportId"`
	Status            string `json:"status"` // accepted | duplicate
	ReportStatus      string `json:"reportStatus,omitempty"`
	Verdict           string `json:"verdict,omitempty"`
	CanonicalRef      string `json:"canonicalRef,omitempty"`
	Occurrences       int64  `json:"occurrences,omitempty"`
	MatchedReportID   int64  `json:"matchedReportId,omitempty"`
	RetryAfterSeconds int64  `json:"retryAfterSeconds,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`
	ReplayReason      string `json:"replayReason,omitempty"`
	Note              string `json:"note"`
}

const (
	csxIssueNoteTriage = "RECORDED AS A CANDIDATE, NOT A BUG. No ticket was created and nothing has been " +
		"confirmed: a person triages this. Do not tell the user a defect has been filed, accepted or fixed."
	csxIssueNoteKnown = "ALREADY KNOWN. This was reported before and is tracked as one issue; your report was " +
		"counted as another occurrence and created no new ticket."
)

// redactCSXIssueProse re-runs the client's redaction. Same reason as the
// anomaly path: the client is a program somebody else can replace, and these
// sentences are written by a model that was told not to include a path.
func redactCSXIssueProse(r domain.CSXIssueReport) (domain.CSXIssueReport, bool) {
	redacted := false
	scrub := func(s string) string {
		clean, changed := sanitizer.Redact(s)
		redacted = redacted || changed
		return clean
	}
	r.ActualBehavior = scrub(r.ActualBehavior)
	r.ExpectedBehavior = scrub(r.ExpectedBehavior)
	r.LLMHypothesis = scrub(r.LLMHypothesis)
	return r, redacted
}

// handleCSXIssueReport implements POST /v1/csx-issues.
func (a *api) handleCSXIssueReport(w http.ResponseWriter, r *http.Request) {
	store, ok := a.d.Store.(serverstore.CSXIssueStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "issue reports are not enabled on this server")
		return
	}
	var env csxIssueEnvelope
	if !readJSON(w, r, 1<<16, &env) {
		return
	}
	if env.SchemaVersion != domain.CSXIssueReportSchemaVersion {
		writeErr(w, http.StatusBadRequest, "issue envelope schemaVersion must be 1")
		return
	}
	if strings.TrimSpace(env.Epoch) == "" || strings.TrimSpace(env.AnonID) == "" {
		writeErr(w, http.StatusBadRequest, "issue report requires epoch and anonId")
		return
	}

	report := env.Report.Normalize()
	if err := report.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Public coordinates are checked like every other coordinate that
	// reaches this server: a report naming a package we cannot confirm is
	// public does not get to store it.
	if report.PublicInput != nil {
		for _, purl := range report.PublicInput.Packages {
			if err := a.anomalyPackageIsPublic(r.Context(), purl); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}
	report, redacted := redactCSXIssueProse(report)

	now := a.now()
	stored, duplicate, err := store.RecordCSXIssueReport(r.Context(), serverstore.CSXIssueReportRow{
		Fingerprint:    report.Fingerprint(),
		ReportJSON:     string(domain.MustCanonicalJSON(report)),
		Surface:        report.AffectedSurface,
		IssueKind:      report.IssueKind,
		Component:      report.Component,
		ReporterBucket: env.Epoch + "/" + env.AnonID,
		Status:         domain.CSXIssueStatusTriage,
	}, now)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "recording the report failed")
		return
	}

	resp := csxIssueResponse{
		ReportID:     stored.ID,
		ReportStatus: stored.Status,
		Verdict:      stored.Verdict,
		CanonicalRef: stored.CanonicalRef,
		Occurrences:  stored.Occurrences,
		Redacted:     redacted,
		ReplayReason: stored.ReplayReason,
	}
	if duplicate {
		resp.Status = "duplicate"
		resp.MatchedReportID = stored.ID
		resp.Note = csxIssueNoteKnown
		if stored.Verdict == "" {
			if wait := serverstore.AnomalyCooldown - now.Sub(stored.FirstSeen); wait > 0 {
				resp.RetryAfterSeconds = int64(wait / time.Second)
			}
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Status = "accepted"
	resp.Note = csxIssueNoteTriage
	// Say up front whether anything here can re-run it. A queue that looks
	// busy with work nothing will ever pick up is the failure mode this
	// project has already had once, and the honest sentence costs nothing.
	if _, replayable, reason := domain.CSXIssueReplayLane(report); replayable {
		resp.ReportStatus = domain.CSXIssueStatusReplayQueued
	} else {
		resp.ReportStatus = domain.CSXIssueStatusNoReplayLane
		resp.ReplayReason = reason
	}
	_ = store.SetCSXIssueTriage(r.Context(), stored.ID, resp.ReportStatus, resp.ReplayReason)
	writeJSON(w, http.StatusOK, resp)
}
