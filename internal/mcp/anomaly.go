package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
)

// The client half of the anomaly feedback channel.
//
// Two things happen here that cannot happen on the server. The first is
// redaction: raw stderr, a path, a token — those exist only on this machine,
// and this is the last place they can be removed rather than transmitted and
// regretted. The second is the refusal: a report with nothing measured behind
// it never reaches the wire at all, so an agent producing suspicion in a loop
// costs the network nothing.

// AnomalySubmission is the server's answer, as an agent needs to read it.
//
// VerificationState and Note travel because the failure mode this channel has
// is not a lost report, it is an agent telling its user "I reported the bug"
// in a sentence that sounds like "the bug is fixed". Nothing else can prevent
// that except the words that come back.
type AnomalySubmission struct {
	ReportID          int64  `json:"reportId"`
	Status            string `json:"status"`
	ReportStatus      string `json:"reportStatus,omitempty"`
	Verdict           string `json:"verdict,omitempty"`
	VerificationState string `json:"verificationState"`
	VerificationJobID int64  `json:"verificationJobId,omitempty"`
	MatchedReportID   int64  `json:"matchedReportId,omitempty"`
	Submissions       int64  `json:"submissions,omitempty"`
	RetryAfterSeconds int64  `json:"retryAfterSeconds,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`
	Reason            string `json:"reason,omitempty"`
	Note              string `json:"note"`
}

// anomalyEnvelope mirrors the server's wire envelope.
type anomalyEnvelope struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Epoch         string               `json:"epoch"`
	AnonID        string               `json:"anonId"`
	Report        domain.AnomalyReport `json:"report"`
}

// ErrAnomalyLocalOnly is returned in local-only mode. It is not a failure:
// the mode exists to send nothing, and a report is a thing that is sent.
var ErrAnomalyLocalOnly = errors.New(
	"this install is local-only, so nothing is uploaded and no verification can be requested. " +
		"The mismatch you found is real and worth acting on locally; `csx init --community` " +
		"is what would let the network re-verify it")

// PrepareAnomalyReport turns tool arguments into a submittable report, or
// says why it cannot be one.
//
// Redaction happens BEFORE validation deliberately. A report is refused for
// what it does not measure, never for what it accidentally included — and the
// thing it accidentally included must be gone either way, including from the
// error message.
func PrepareAnomalyReport(report domain.AnomalyReport, rawErrorText string,
	machine domain.EnvironmentFingerprint) (domain.AnomalyReport, bool, error) {
	report.SchemaVersion = domain.AnomalyReportSchemaVersion
	report.Environment = fillFromMachine(report.Environment, machine)

	redacted := false
	scrub := func(s string) string {
		clean, changed := sanitizer.Redact(strings.TrimSpace(s))
		redacted = redacted || changed
		return clean
	}
	report.CSXObserved.Detail = scrub(report.CSXObserved.Detail)
	report.LocalObserved.Detail = scrub(report.LocalObserved.Detail)
	report.LLMHypothesis = scrub(report.LLMHypothesis)
	report.Symbol = scrub(report.Symbol)
	for _, field := range []*string{
		&report.Environment.Ecosystem, &report.Environment.OS, &report.Environment.OSVersionBucket,
		&report.Environment.Arch, &report.Environment.Runtime, &report.Environment.RuntimeVersion,
		&report.Environment.Language, &report.Environment.LanguageVersion,
		&report.Environment.Compiler, &report.Environment.CompilerVersion,
		&report.Environment.PackageManager, &report.Environment.PackageManagerVersion,
		&report.Environment.ModuleSystem, &report.Environment.ExecutionContext,
		&report.Environment.BrowserFamily, &report.Environment.BrowserMajor,
		&report.Environment.Engine, &report.Environment.EngineVersion,
		&report.Environment.Virtualization, &report.Environment.ContainerRuntime,
		&report.Environment.Libc, &report.Environment.LibcVersion, &report.Environment.Distro,
	} {
		*field = scrub(*field)
	}
	for i := range report.Environment.Frameworks {
		report.Environment.Frameworks[i] = scrub(report.Environment.Frameworks[i])
	}

	// Raw output never travels. What travels is what the sanitizer already
	// produces for every other failure this product records: a template with
	// placeholders where the identifying material was, the error class, and
	// the fingerprint that makes two reports of one failure one report.
	if raw := strings.TrimSpace(rawErrorText); raw != "" {
		stage := domain.Stage(strings.ToUpper(strings.TrimSpace(report.LocalObserved.Stage)))
		sanitized := sanitizer.Sanitize(raw, stage, nil)
		report.ErrorTemplate = truncateTemplate(sanitized.Template)
		if report.ErrorCode == "" {
			report.ErrorCode = sanitized.Code
		}
		if report.ErrorFingerprint == "" {
			report.ErrorFingerprint = sanitized.Fingerprint
		}
		redacted = redacted || sanitized.Template != raw
	} else if report.ErrorTemplate != "" {
		report.ErrorTemplate = truncateTemplate(scrub(report.ErrorTemplate))
	}

	report = report.Normalize()
	if err := report.Validate(); err != nil {
		return report, redacted, err
	}
	return report, redacted, nil
}

// maxErrorTemplate keeps "a short sanitized core" from becoming a log dump.
// A template long enough to be a log is long enough to have carried
// something the placeholders missed.
const maxErrorTemplate = 2000

func truncateTemplate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxErrorTemplate {
		return s
	}
	return s[:maxErrorTemplate] + "\n…[truncated]"
}

// SubmitAnomalyReport posts one prepared report and returns the server's
// answer. It never retries: a lost response is answered by asking again, and
// the fingerprint makes that safe.
func SubmitAnomalyReport(ctx context.Context, client *http.Client, serverURL, epoch, anonID string,
	report domain.AnomalyReport) (AnomalySubmission, error) {
	var out AnomalySubmission
	body, err := json.Marshal(anomalyEnvelope{
		SchemaVersion: domain.AnomalyReportSchemaVersion,
		Epoch:         epoch,
		AnonID:        anonID,
		Report:        report,
	})
	if err != nil {
		return out, err
	}
	url := strings.TrimRight(serverURL, "/") + "/v1/anomalies"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("submitting the report failed: %w", err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&raw); err != nil {
		return out, fmt.Errorf("the server's reply could not be read (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return out, errors.New(e.Error)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("the server's reply could not be read: %w", err)
	}
	return out, nil
}

// --- report_csx_issue client half ---

// CSXIssueSubmission is the server's answer to a product-defect report.
//
// TicketFiled does not exist as a field because no ticket is ever filed.
// What travels instead is CanonicalRef — set only when an operator has
// already linked this defect to a bug — and a Note that tells the agent, in
// the words the client renders, not to claim otherwise.
type CSXIssueSubmission struct {
	ReportID          int64  `json:"reportId"`
	Status            string `json:"status"`
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

type csxIssueEnvelope struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Epoch         string                `json:"epoch"`
	AnonID        string                `json:"anonId"`
	Report        domain.CSXIssueReport `json:"report"`
}

// ErrCSXIssueLocalOnly mirrors ErrAnomalyLocalOnly: the mode exists to send
// nothing, and a report is a thing that is sent.
var ErrCSXIssueLocalOnly = errors.New(
	"this install is local-only, so nothing is uploaded and no report can be submitted. " +
		"The problem you found is still real; `csx init --community` is what would let it be reported")

// PrepareCSXIssueReport redacts the prose and applies the admission test
// before anything reaches the wire.
func PrepareCSXIssueReport(report domain.CSXIssueReport) (domain.CSXIssueReport, bool, error) {
	report.SchemaVersion = domain.CSXIssueReportSchemaVersion
	redacted := false
	scrub := func(s string) string {
		clean, changed := sanitizer.Redact(strings.TrimSpace(s))
		redacted = redacted || changed
		return clean
	}
	report.ActualBehavior = scrub(report.ActualBehavior)
	report.ExpectedBehavior = scrub(report.ExpectedBehavior)
	report.LLMHypothesis = scrub(report.LLMHypothesis)

	report = report.Normalize()
	if err := report.Validate(); err != nil {
		return report, redacted, err
	}
	return report, redacted, nil
}

// SubmitCSXIssueReport posts one prepared report. Like the anomaly path it
// never retries: the fingerprint makes asking again safe.
func SubmitCSXIssueReport(ctx context.Context, client *http.Client, serverURL, epoch, anonID string,
	report domain.CSXIssueReport) (CSXIssueSubmission, error) {
	var out CSXIssueSubmission
	body, err := json.Marshal(csxIssueEnvelope{
		SchemaVersion: domain.CSXIssueReportSchemaVersion,
		Epoch:         epoch,
		AnonID:        anonID,
		Report:        report,
	})
	if err != nil {
		return out, err
	}
	url := strings.TrimRight(serverURL, "/") + "/v1/csx-issues"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("submitting the report failed: %w", err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return out, fmt.Errorf("the server's reply could not be read (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return out, errors.New(e.Error)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("the server's reply could not be read: %w", err)
	}
	return out, nil
}
