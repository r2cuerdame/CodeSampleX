package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The consumption side answering back.
//
// Everything below exists to keep one property true: a report is a QUESTION
// until a signed receipt answers it. Nothing here writes evidence, touches a
// snapshot, or reaches any surface a reader can see. What it does is turn a
// concrete local contradiction into a job the verification fleet already
// knows how to run, and then wait.
//
// The fleet is the reason this reuses the cross queue rather than growing a
// verification system of its own. A new job `reason` would be invisible to
// every worker already deployed — they skip what they do not recognize — and
// the report would sit "queued" forever while the dashboard showed work. A
// cross job is claimed by workers in the field today, and an independent
// clean re-run of the sample the answer came from is precisely the
// measurement that settles "CSX says this passes and my machine says it
// does not".

// anomalyEnvelope is the wire request: the report plus the same rotating
// anonymous bucket every other anonymous write on this server uses.
type anomalyEnvelope struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Epoch         string               `json:"epoch"`
	AnonID        string               `json:"anonId"`
	Report        domain.AnomalyReport `json:"report"`
}

// anomalyResponse is what the agent gets back.
//
// Note is not decoration. An agent that has just filed a report is one
// sentence away from telling its user "I reported the bug and it is fixed",
// and the response is the only place that can be prevented. Every accepted
// answer says, in the payload the client renders, that nothing has been
// verified yet.
type anomalyResponse struct {
	ReportID          int64  `json:"reportId"`
	Status            string `json:"status"` // accepted | duplicate | rejected
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

const (
	anomalyNotePending = "VERIFICATION PENDING. This is a verification request, not a finding. " +
		"Nothing about it is public and nothing has been confirmed: a verifier must reproduce it first. " +
		"Do not describe this as a fixed or confirmed bug."
	anomalyNoteDuplicate = "VERIFICATION PENDING. This mismatch was already reported and is already queued; " +
		"your submission was counted against the existing report and created no new verification work."
	anomalyNoteUnsupported = "ACCEPTED BUT NOT VERIFIABLE HERE. The report is stored and visible to operators, " +
		"but no verifier lane in this network can reproduce it, so no verification was queued. " +
		"It is not waiting on anything — treat it as unanswered, not as pending."
)

// redactAnomalyFreeText re-runs the client's redaction on every free-text
// field. Structured ids and enums are validated separately; everything else
// is scrubbed because a replacement client can put a path or token anywhere.
//
// The client redacts before sending. This runs again because the client is a
// program somebody else can replace, and because the fields are written by a
// language model that was told not to include a path and may have anyway.
//
// It redacts rather than refuses on purpose: the local facts in a report are
// the part worth having, and throwing the whole report away because one
// sentence named a directory would lose them to protect something the
// redaction already removed.
func redactAnomalyFreeText(r domain.AnomalyReport) (domain.AnomalyReport, bool) {
	redacted := false
	scrub := func(s string) string {
		clean, changed := sanitizer.Redact(s)
		redacted = redacted || changed
		return clean
	}
	r.CSXObserved.Detail = scrub(r.CSXObserved.Detail)
	r.LocalObserved.Detail = scrub(r.LocalObserved.Detail)
	r.ErrorTemplate = scrub(r.ErrorTemplate)
	r.LLMHypothesis = scrub(r.LLMHypothesis)
	r.Symbol = scrub(r.Symbol)
	for _, field := range []*string{
		&r.Environment.Ecosystem, &r.Environment.OS, &r.Environment.OSVersionBucket,
		&r.Environment.Arch, &r.Environment.Runtime, &r.Environment.RuntimeVersion,
		&r.Environment.Language, &r.Environment.LanguageVersion,
		&r.Environment.Compiler, &r.Environment.CompilerVersion,
		&r.Environment.PackageManager, &r.Environment.PackageManagerVersion,
		&r.Environment.ModuleSystem, &r.Environment.ExecutionContext,
		&r.Environment.BrowserFamily, &r.Environment.BrowserMajor,
		&r.Environment.Engine, &r.Environment.EngineVersion,
		&r.Environment.Virtualization, &r.Environment.ContainerRuntime,
		&r.Environment.Libc, &r.Environment.LibcVersion, &r.Environment.Distro,
	} {
		*field = scrub(*field)
	}
	for i := range r.Environment.Frameworks {
		r.Environment.Frameworks[i] = scrub(r.Environment.Frameworks[i])
	}
	return r, redacted
}

// anomalyIdentifierIsSafe is the second half of syntax validation. Domain
// validation checks the id shapes; this server-boundary check refuses values
// the privacy sanitizer recognizes as identifying material rather than
// silently changing references into ids that never existed.
func anomalyIdentifierIsSafe(s string) bool {
	if s == "" {
		return true
	}
	// Content hashes are intentionally token-shaped. Their complete canonical
	// syntax was already checked by domain.Validate, and redacting one would
	// turn a public reference into a different, nonexistent id.
	if len(s) == len("sha256:")+64 && strings.HasPrefix(s, "sha256:") {
		return true
	}
	_, changed := sanitizer.Redact(s)
	return !changed
}

func anomalyIdentifiersAreSafe(r domain.AnomalyReport) bool {
	for _, value := range []string{
		r.SampleID, r.EvidenceID, r.SearchFingerprint,
		r.ErrorCode, r.ErrorFingerprint,
	} {
		if !anomalyIdentifierIsSafe(value) {
			return false
		}
	}
	for _, value := range r.RelatedIDs {
		if !anomalyIdentifierIsSafe(value) {
			return false
		}
	}
	return true
}

func anomalyStructuredValueIsSafe(s string, maxBytes int) bool {
	if s == "" {
		return true
	}
	if len(s) > maxBytes || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return false
		}
	}
	return true
}

func anomalyStructuredFieldsAreSafe(r domain.AnomalyReport) bool {
	if !anomalyStructuredValueIsSafe(r.Symbol, 256) {
		return false
	}
	for _, value := range []string{
		r.Environment.Ecosystem, r.Environment.OS, r.Environment.OSVersionBucket,
		r.Environment.Arch, r.Environment.Runtime, r.Environment.RuntimeVersion,
		r.Environment.Language, r.Environment.LanguageVersion,
		r.Environment.Compiler, r.Environment.CompilerVersion,
		r.Environment.PackageManager, r.Environment.PackageManagerVersion,
		r.Environment.ModuleSystem, r.Environment.ExecutionContext,
		r.Environment.BrowserFamily, r.Environment.BrowserMajor,
		r.Environment.Engine, r.Environment.EngineVersion,
		r.Environment.Virtualization, r.Environment.ContainerRuntime,
		r.Environment.Libc, r.Environment.LibcVersion, r.Environment.Distro,
	} {
		if !anomalyStructuredValueIsSafe(value, 128) {
			return false
		}
	}
	for _, value := range r.Environment.Frameworks {
		if !anomalyStructuredValueIsSafe(value, 128) {
			return false
		}
	}
	return true
}

func anomalyBucketPartIsSafe(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 128 || !anomalyIdentifierIsSafe(s) {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// anomalyPackageIsPublic refuses a report about a package this network cannot
// confirm is public. A private coordinate must not be stored, and a report
// nobody outside the reporter's machine could reproduce is not admissible
// anyway — the two reasons agree, which is how it should be.
func (a *api) anomalyPackageIsPublic(ctx context.Context, purl string) error {
	if a.trustMode() {
		return nil
	}
	p, err := domain.ParsePURL(purl)
	if err != nil {
		return domain.ErrAnomalyPackage
	}
	if a.d.Checker == nil {
		return errors.New("public package check unavailable")
	}
	if a.d.Checker.Check(ctx, p) != scanner.PublicnessPublic {
		return errors.New("package is not a confirmed public coordinate; only public packages may be reported")
	}
	return nil
}

// handleAnomalyReport implements POST /v1/anomalies.
func (a *api) handleAnomalyReport(w http.ResponseWriter, r *http.Request) {
	store, ok := a.d.Store.(serverstore.AnomalyStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "anomaly reports are not enabled on this server")
		return
	}
	var env anomalyEnvelope
	if !readJSON(w, r, 1<<16, &env) {
		return
	}
	if env.SchemaVersion != domain.AnomalyReportSchemaVersion {
		writeErr(w, http.StatusBadRequest, "anomaly envelope schemaVersion must be 1")
		return
	}
	env.Epoch = strings.TrimSpace(env.Epoch)
	env.AnonID = strings.TrimSpace(env.AnonID)
	if !anomalyBucketPartIsSafe(env.Epoch) || !anomalyBucketPartIsSafe(env.AnonID) {
		writeErr(w, http.StatusBadRequest, "anomaly report requires safe epoch and anonId identifiers")
		return
	}

	report := env.Report.Normalize()
	// Validate BEFORE redaction: an admission failure is about the facts,
	// and telling the caller which fact is missing is more useful than
	// telling it about a sentence that was scrubbed on the way in.
	if err := report.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !anomalyIdentifiersAreSafe(report) {
		writeErr(w, http.StatusBadRequest, "anomaly report identifiers contain path, URL or token material")
		return
	}
	if err := a.anomalyPackageIsPublic(r.Context(), report.Package); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	report, redacted := redactAnomalyFreeText(report)
	if !anomalyStructuredFieldsAreSafe(report) {
		writeErr(w, http.StatusBadRequest, "anomaly symbol and environment values must be bounded structured values, not arbitrary text")
		return
	}

	// A sample id that this server does not have is not a reason to refuse:
	// "the answer references a sample that does not exist" is one of the
	// anomaly types. It does mean there is nothing to hand a verifier.
	sampleKnown := false
	if report.SampleID != "" {
		if _, found, err := a.d.Store.GetSample(r.Context(), report.SampleID); err != nil {
			writeErr(w, http.StatusInternalServerError, "sample lookup failed")
			return
		} else if found {
			sampleKnown = true
		}
	}

	now := a.now()
	row := serverstore.AnomalyReportRow{
		Fingerprint:    report.Fingerprint(),
		ReportJSON:     string(domain.MustCanonicalJSON(report)),
		AnomalyType:    report.AnomalyType,
		PURL:           report.Package,
		Symbol:         report.Symbol,
		ReporterBucket: env.Epoch + "/" + env.AnonID,
		Status:         domain.AnomalyStatusQueued,
	}
	if sampleKnown {
		row.SampleID = report.SampleID
	}
	stored, duplicate, err := store.RecordAnomalyReport(r.Context(), row, now)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "recording the report failed")
		return
	}

	if duplicate {
		// The existing row already owns whatever verification exists. Saying
		// so — and saying when it is worth asking again — is the answer; a
		// second job for the same fingerprint is exactly what dedupe is for.
		resp := anomalyResponse{
			ReportID:          stored.ID,
			Status:            "duplicate",
			ReportStatus:      stored.Status,
			Verdict:           stored.Verdict,
			VerificationJobID: stored.JobID,
			MatchedReportID:   stored.ID,
			Submissions:       stored.Reports,
			Redacted:          redacted,
			VerificationState: anomalyVerificationState(stored),
			Note:              anomalyNoteDuplicate,
		}
		if stored.Verdict == "" {
			if wait := serverstore.AnomalyCooldown - now.Sub(stored.FirstSeen); wait > 0 {
				resp.RetryAfterSeconds = int64(wait / time.Second)
			}
		}
		if stored.Status == domain.AnomalyStatusUnsupported {
			resp.Reason = stored.UnsupportedReason
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp := anomalyResponse{
		ReportID:    stored.ID,
		Status:      "accepted",
		Submissions: stored.Reports,
		Redacted:    redacted,
		Note:        anomalyNotePending,
	}
	jobID, reason := a.queueAnomalyVerification(r.Context(), report, sampleKnown)
	if jobID != 0 {
		resp.ReportStatus = domain.AnomalyStatusQueued
		resp.VerificationJobID = jobID
		_ = store.AttachAnomalyVerificationJob(r.Context(), stored.ID, jobID, domain.AnomalyStatusQueued, "")
	} else {
		resp.ReportStatus = domain.AnomalyStatusUnsupported
		resp.Reason = reason
		resp.Note = anomalyNoteUnsupported
		_ = store.AttachAnomalyVerificationJob(r.Context(), stored.ID, 0, domain.AnomalyStatusUnsupported, reason)
	}
	stored.Status = resp.ReportStatus
	stored.JobID = jobID
	resp.VerificationState = anomalyVerificationState(stored)
	writeJSON(w, http.StatusOK, resp)
}

// anomalyVerificationState renders where the report is, in the vocabulary an
// agent and an operator can both read.
func anomalyVerificationState(row serverstore.AnomalyReportRow) string {
	switch row.Status {
	case domain.AnomalyStatusVerified:
		return "verified"
	case domain.AnomalyStatusVerifying:
		return "claimed by a verifier"
	case domain.AnomalyStatusUnsupported:
		return "not queued: " + domain.AnomalyStatusUnsupported
	default:
		if row.JobID != 0 {
			return "queued for verification"
		}
		return "queued"
	}
}

// queueAnomalyVerification puts the report in front of the verification
// fleet, and says why it could not when it could not.
//
// It returns (0, reason) rather than pretending: a report nothing can
// reproduce must SAY that to the caller and to the operator page. Leaving it
// "queued" against a queue that will never offer it is the failure mode the
// unsupported job status was added for, three days of an open queue every
// worker skipped in silence.
func (a *api) queueAnomalyVerification(ctx context.Context, report domain.AnomalyReport, sampleKnown bool) (int64, string) {
	if !sampleKnown {
		if report.SampleID != "" {
			return 0, "the report names sample " + report.SampleID + ", which this server does not hold, " +
				"so there is no artifact any verifier could re-run"
		}
		return 0, "no verifier lane: the report names no sample this network published, and the fleet " +
			"reproduces published sample artifacts rather than arbitrary coordinates. " +
			"Publishing a minimal sample for " + report.Package + " would make it reproducible"
	}
	sample, found, err := a.d.Store.GetSample(ctx, report.SampleID)
	if err != nil || !found {
		return 0, "sample lookup failed while queueing verification"
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal([]byte(sample.ManifestJSON), &manifest); err != nil {
		return 0, "the stored sample manifest cannot be read, so no reproduction can be built from it"
	}

	job := crossJobFor(report.SampleID, manifest)
	if job.Status == serverstore.JobStatusUnsupported {
		return 0, fmt.Sprintf("no verifier image in this network serves %s/%s, so the sample cannot be re-run here",
			strings.TrimSpace(manifest.Environment.Ecosystem), strings.TrimSpace(manifest.Environment.Runtime))
	}
	// Two distinct anomaly fingerprints can contest one sample at the same
	// instant. The reuse check and insert must therefore be one store
	// operation; a handler-level JobsForSample→CreateJob pair races open.
	id, err := a.d.Store.EnsureCrossJob(ctx, job)
	if err != nil || id == 0 {
		return 0, "creating the verification job failed"
	}
	return id, ""
}

// resolveAnomalyReports closes the reports one receipt just answered.
//
// Called from the receipt path, after the receipt has been stored and the
// sample's status recomputed, so a verdict is only ever derived from a
// signature this server has already verified. It is best-effort by design:
// a failure here must never reject a receipt a verifier legitimately signed.
//
// The reporter's hypothesis, its confidence and how sure it sounded are not
// read anywhere below. The receipt decides.
func (a *api) resolveAnomalyReports(ctx context.Context, receipt domain.VerificationReceipt) {
	store, ok := a.d.Store.(serverstore.AnomalyStore)
	if !ok {
		return
	}
	open, err := store.OpenAnomalyReportsForSample(ctx, receipt.SampleID)
	if err != nil || len(open) == 0 {
		return
	}
	now := a.now()
	for _, row := range open {
		var report domain.AnomalyReport
		if err := json.Unmarshal([]byte(row.ReportJSON), &report); err != nil {
			continue
		}
		verdict, decided := domain.AnomalyVerdictFromReceipt(report, receipt)
		if !decided {
			// The contract never ran, so this measured the verifier rather
			// than the sample. Inventing a verdict from a resolve failure is
			// the one thing that must not happen here.
			//
			// But "no verdict" may not mean "wait forever". The receipt path
			// has already decided whether to retry, and a sample whose cross
			// attempts are spent has nothing left coming: the honest close
			// is that the network tried and could not measure it. A report
			// left open against a queue with no work in it is the pending
			// that never ends, which this channel is not allowed to produce.
			if a.sampleHasVerificationComing(ctx, receipt.SampleID) {
				continue
			}
			verdict = domain.AnomalyVerdictInsufficientEvidence
		}
		if _, err := store.SetAnomalyVerdict(ctx, row.ID, verdict, now); err != nil {
			continue
		}
	}
}

// sampleHasVerificationComing reports whether any cross job for this sample
// is still open or in flight — that is, whether anything will ever measure it
// again. It is read AFTER the receipt path has had its chance to requeue, so
// it observes the decision rather than second-guessing it.
func (a *api) sampleHasVerificationComing(ctx context.Context, sampleID string) bool {
	jobs, err := a.d.Store.JobsForSample(ctx, sampleID)
	if err != nil {
		// An unreadable queue is not evidence that nothing is coming, and a
		// report closed on a failed read is a report closed for no reason.
		return true
	}
	for _, j := range jobs {
		if j.Reason == "cross" && (j.Status == "open" || j.Status == "claimed") {
			return true
		}
	}
	return false
}

// markAnomalyReportsVerifying records that a verifier picked the work up, so
// the operator page can tell a claimed report from one nobody has touched —
// the difference between a slow fleet and a stuck one.
func (a *api) markAnomalyReportsVerifying(ctx context.Context, jobID int64) {
	store, ok := a.d.Store.(serverstore.AnomalyStore)
	if !ok {
		return
	}
	job, found, err := a.d.Store.Job(ctx, jobID)
	if err != nil || !found || job.SampleID == "" {
		return
	}
	open, err := store.OpenAnomalyReportsForSample(ctx, job.SampleID)
	if err != nil {
		return
	}
	for _, row := range open {
		if row.JobID != jobID || row.Status != domain.AnomalyStatusQueued {
			continue
		}
		_ = store.AttachAnomalyVerificationJob(ctx, row.ID, jobID, domain.AnomalyStatusVerifying, "")
	}
}
