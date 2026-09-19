package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Fix-claim verification (#444): /v1/fix-claims.
//
// Producers (the collector, an AGY lane) submit FIX_CANDIDATE records under
// a writer session; the deterministic validator decides what enters the
// queue. Farm workers lease a candidate under the same session, receive the
// reproducer resolution and the probes the planner wants, run them, and
// report each run WITH the receipt that proves it. The server checks that
// the receipt exists, belongs to the sample named, reached the verdict
// claimed, and that the sample pins the release the run is filed under --
// so nothing a producer, a model or a worker merely says can move a claim
// past CLAIMED_FIX. The public reads mark verified=true only for
// VERIFIED_FIX and PARTIAL_FIX.

const (
	fixClaimsLease         = 6 * time.Hour
	fixClaimsIngestLimit   = 256 << 10
	fixClaimsMaxCandidates = 50
	fixClaimsMaxRuns       = 16
	fixClaimsListLimit     = 50
	fixClaimsReproducerCap = 50
	// fixClaimsClaimTurns bounds how many closed-by-planner rows one poll
	// may skip past before answering NO_WORK.
	fixClaimsClaimTurns = 5
)

var (
	fixHexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// fixEnvToken is the same shape a footprint's environment dimension
	// takes: a short lowercase token, never a description.
	fixEnvToken = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,31}$`)
)

func (a *api) fixClaimStore() (serverstore.FixClaimStore, bool) {
	store, ok := a.d.Store.(serverstore.FixClaimStore)
	return store, ok
}

// fixClaimSession authenticates a writer session for the producer and
// worker routes. The fix lane shares the authoring lane's sessions on
// purpose: one bearer, one revoke list, one idle expiry.
func (a *api) fixClaimSession(w http.ResponseWriter, r *http.Request) (serverstore.AuthoringSessionRow, bool) {
	sessions, ok := a.d.Store.(serverstore.AuthoringSessionStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "authoring session storage unavailable")
		return serverstore.AuthoringSessionRow{}, false
	}
	tokenHash, ok := authoringDraftTokenHash(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "authoring session unavailable")
		return serverstore.AuthoringSessionRow{}, false
	}
	now := a.now().UTC()
	ip := ""
	if addr, ok := activity.ExternalRequestAddress(r); ok {
		ip = addr.String()
	}
	session, err := sessions.RefreshAuthoringSession(r.Context(), tokenHash, ip, "", now, now.Add(time.Hour))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "authoring session unavailable")
		return serverstore.AuthoringSessionRow{}, false
	}
	return session, true
}

func (a *api) fixLimits() serverstore.FixClaimLimits {
	lim := a.d.Cfg.FixClaim
	def := serverstore.DefaultFixClaimLimits()
	if lim.MaxAttempts <= 0 {
		lim.MaxAttempts = def.MaxAttempts
	}
	if lim.MaxRuns <= 0 {
		lim.MaxRuns = def.MaxRuns
	}
	if lim.MaxLeases == 0 && a.d.Cfg.FixClaim == (serverstore.FixClaimLimits{}) {
		// An unconfigured server (tests, dev) runs the Phase 0 defaults; a
		// configured zero is the operator's rollback and stays zero.
		lim.MaxLeases = def.MaxLeases
	}
	return lim
}

// decodeStrict reads one JSON document with no unknown fields and nothing
// after it.
func decodeStrict(w http.ResponseWriter, r *http.Request, limit int64, v any, what string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeErr(w, http.StatusBadRequest, "invalid "+what+": "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid "+what+": trailing data")
		return false
	}
	return true
}

// ------------------------------------------------------------ producers --

type fixCandidatesRequest struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Candidates    []fixclaims.Candidate `json:"candidates"`
}

type fixCandidateRejection struct {
	Index      int                   `json:"index"`
	Rejections []fixclaims.Rejection `json:"rejections"`
}

type fixCandidateAccepted struct {
	Index     int              `json:"index"`
	ID        int64            `json:"id"`
	Status    fixclaims.Status `json:"status"`
	Score     int64            `json:"score"`
	Duplicate bool             `json:"duplicate"`
}

// handleFixCandidates is POST /v1/fix-claims/candidates.
func (a *api) handleFixCandidates(w http.ResponseWriter, r *http.Request) {
	store, ok := a.fixClaimStore()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "fix-claim storage unavailable")
		return
	}
	if _, ok := a.fixClaimSession(w, r); !ok {
		return
	}
	var req fixCandidatesRequest
	if !decodeStrict(w, r, fixClaimsIngestLimit, &req, "fix candidates") {
		return
	}
	if req.SchemaVersion != 1 || len(req.Candidates) == 0 {
		writeErr(w, http.StatusBadRequest, "schemaVersion 1 with at least one candidate is required")
		return
	}
	if len(req.Candidates) > fixClaimsMaxCandidates {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("at most %d candidates per request", fixClaimsMaxCandidates))
		return
	}
	now := a.now().UTC()
	var (
		rows     []serverstore.FixCandidateRow
		indexes  []int
		rejected []fixCandidateRejection
	)
	for i, raw := range req.Candidates {
		c, rejections := fixclaims.Validate(raw)
		if len(rejections) > 0 {
			rejected = append(rejected, fixCandidateRejection{Index: i, Rejections: rejections})
			continue
		}
		rows = append(rows, serverstore.FixCandidateRow{Candidate: c, Score: fixclaims.Score(c, a.fixScoreInputs(r, c, now))})
		indexes = append(indexes, i)
	}
	var accepted []fixCandidateAccepted
	if len(rows) > 0 {
		upserts, err := store.UpsertFixCandidates(r.Context(), rows, int64(len(rejected)), now)
		if err != nil {
			writeStoreErr(w, err, http.StatusInternalServerError, "storing fix candidates failed")
			return
		}
		for j, u := range upserts {
			accepted = append(accepted, fixCandidateAccepted{Index: indexes[j], ID: u.Row.ID, Status: u.Row.Status, Score: u.Row.Score, Duplicate: u.Duplicate})
		}
	}
	if accepted == nil {
		accepted = []fixCandidateAccepted{}
	}
	if rejected == nil {
		rejected = []fixCandidateRejection{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted, "rejected": rejected})
}

// fixScoreInputs reads what the corpus already knows about a candidate's
// package: whether the fingerprint it hints at was observed, and whether a
// sample already exercises the symbol. Both are bounded per-package reads.
func (a *api) fixScoreInputs(r *http.Request, c fixclaims.Candidate, now time.Time) fixclaims.ScoreInputs {
	in := fixclaims.ScoreInputs{Now: now}
	if c.FailureFingerprintHint != "" {
		if clusters, err := a.d.Store.ListFailureClusters(r.Context(), c.Name); err == nil {
			for _, cl := range clusters {
				if cl.Ecosystem == c.Ecosystem && cl.ErrorFingerprint == c.FailureFingerprintHint {
					in.FingerprintMatch = true
					break
				}
			}
		}
	}
	if res := a.fixResolve(r, c); res.Source == fixclaims.ReproducerExistingSample {
		in.SampleReuse = true
	}
	return in
}

func (a *api) fixResolve(r *http.Request, c fixclaims.Candidate) fixclaims.Resolution {
	pattern := domain.PURL{Ecosystem: c.Ecosystem, Name: c.Name}.AnyVersionPattern()
	rows, err := a.d.Store.VerifiedSamplesForPackages(r.Context(), []string{pattern}, fixClaimsReproducerCap)
	if err != nil {
		rows = nil
	}
	samples := make([]fixclaims.SampleCandidate, 0, len(rows))
	for _, row := range rows {
		samples = append(samples, fixclaims.SampleCandidate{SampleID: row.SampleID, ManifestJSON: row.ManifestJSON})
	}
	return fixclaims.Resolve(c, samples)
}

// -------------------------------------------------------------- workers --

type fixWorkRequest struct {
	SchemaVersion int      `json:"schemaVersion"`
	VerifierOS    []string `json:"verifierOS,omitempty"`
	ClientVersion string   `json:"clientVersion,omitempty"`
}

// handleFixWorkNext is POST /v1/fix-claims/work/next.
func (a *api) handleFixWorkNext(w http.ResponseWriter, r *http.Request) {
	store, ok := a.fixClaimStore()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "fix-claim storage unavailable")
		return
	}
	session, ok := a.fixClaimSession(w, r)
	if !ok {
		return
	}
	var req fixWorkRequest
	if !decodeStrict(w, r, 4<<10, &req, "fix work request") {
		return
	}
	if req.SchemaVersion != 1 {
		writeErr(w, http.StatusBadRequest, "schemaVersion 1 is required")
		return
	}
	limits := a.fixLimits()
	if limits.MaxLeases <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{"status": serverstore.FixClaimBudgetExhausted, "reason": "fix work disabled"})
		return
	}
	now := a.now().UTC()
	for turn := 0; turn < fixClaimsClaimTurns; turn++ {
		row, status, err := store.ClaimFixWork(r.Context(), session.SessionID, limits, now, now.Add(fixClaimsLease))
		if err != nil {
			writeStoreErr(w, err, http.StatusInternalServerError, "claiming fix work failed")
			return
		}
		if status != serverstore.FixClaimAssigned {
			writeJSON(w, http.StatusOK, map[string]any{"status": status})
			return
		}
		runs, err := store.ListFixRuns(r.Context(), row.ID)
		if err != nil {
			writeStoreErr(w, err, http.StatusInternalServerError, "reading fix runs failed")
			return
		}
		probes := a.fixProbes(r, row, runs, req.VerifierOS, limits)
		if len(probes) == 0 && len(runs) > 0 {
			// The planner has nothing more to ask of this record: close it
			// and look again rather than hand a worker nothing to do.
			if _, err := store.ReleaseFixWork(r.Context(), row.ID, session.SessionID, serverstore.FixOutcomeComplete, "", limits, now); err != nil {
				writeStoreErr(w, err, http.StatusInternalServerError, "closing fix work failed")
				return
			}
			continue
		}
		if len(probes) == 0 {
			// A fresh candidate the worker's lanes cannot run at all.
			if _, err := store.ReleaseFixWork(r.Context(), row.ID, session.SessionID, serverstore.FixOutcomeTransient, "no runnable probe for this worker", limits, now); err != nil {
				writeStoreErr(w, err, http.StatusInternalServerError, "releasing fix work failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": serverstore.FixClaimNoWork})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": serverstore.FixClaimAssigned,
			"work": map[string]any{
				"id":             row.ID,
				"candidate":      row.Candidate,
				"record":         fixRecord(row, nil),
				"reproducer":     a.fixResolve(r, row.Candidate),
				"probes":         probes,
				"runs":           fixRunViews(runs),
				"leaseExpiresAt": now.Add(fixClaimsLease),
				"limits":         map[string]int{"maxRuns": limits.MaxRuns, "maxAttempts": limits.MaxAttempts},
			},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": serverstore.FixClaimNoWork})
}

func (a *api) fixProbes(r *http.Request, row serverstore.FixCandidateRow, runs []serverstore.FixRunRow, os []string, limits serverstore.FixClaimLimits) []fixclaims.Probe {
	var known []string
	if versions, err := a.d.Store.ListPackageVersions(r.Context(), row.Candidate.Ecosystem, row.Candidate.Name); err == nil {
		for _, v := range versions {
			known = append(known, v.Version)
		}
	}
	pol := fixclaims.DefaultPolicy()
	pol.MaxRuns = limits.MaxRuns
	pol.OS = os
	plain := make([]fixclaims.Run, 0, len(runs))
	for _, run := range runs {
		plain = append(plain, run.Run)
	}
	return fixclaims.Plan(row.Candidate, plain, known, pol)
}

type fixReproducerRequest struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Source        fixclaims.ReproducerSource `json:"source"`
	SampleID      string                     `json:"sampleId,omitempty"`
}

// handleFixReproducer is POST /v1/fix-claims/{id}/reproducer.
func (a *api) handleFixReproducer(w http.ResponseWriter, r *http.Request) {
	store, ok := a.fixClaimStore()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "fix-claim storage unavailable")
		return
	}
	session, ok := a.fixClaimSession(w, r)
	if !ok {
		return
	}
	id, ok := fixClaimID(w, r)
	if !ok {
		return
	}
	var req fixReproducerRequest
	if !decodeStrict(w, r, 4<<10, &req, "fix reproducer") {
		return
	}
	if req.SchemaVersion != 1 || !fixclaims.ValidReproducerSource(req.Source) {
		writeErr(w, http.StatusBadRequest, "schemaVersion 1 and a source of EXISTING_SAMPLE, UPSTREAM_REPRO or GENERATED are required")
		return
	}
	if req.SampleID != "" {
		if _, found, err := a.d.Store.GetSample(r.Context(), req.SampleID); err != nil || !found {
			writeErr(w, http.StatusBadRequest, "sampleId is not a stored sample")
			return
		}
	}
	row, err := store.SetFixReproducer(r.Context(), id, session.SessionID, req.Source, req.SampleID, a.now().UTC())
	if !a.writeFixLeaseErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "RECORDED", "record": fixRecord(row, nil)})
}

type fixRunsRequest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Runs          []fixclaims.Run `json:"runs"`
}

// handleFixRuns is POST /v1/fix-claims/{id}/runs: the only write that can
// move a claim's status, and it moves it only as far as receipts allow.
func (a *api) handleFixRuns(w http.ResponseWriter, r *http.Request) {
	store, ok := a.fixClaimStore()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "fix-claim storage unavailable")
		return
	}
	session, ok := a.fixClaimSession(w, r)
	if !ok {
		return
	}
	id, ok := fixClaimID(w, r)
	if !ok {
		return
	}
	var req fixRunsRequest
	if !decodeStrict(w, r, 64<<10, &req, "fix runs") {
		return
	}
	if req.SchemaVersion != 1 || len(req.Runs) == 0 || len(req.Runs) > fixClaimsMaxRuns {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("schemaVersion 1 with 1..%d runs is required", fixClaimsMaxRuns))
		return
	}
	row, found, err := store.GetFixCandidate(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "reading fix candidate failed")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "fix candidate not found")
		return
	}
	now := a.now().UTC()
	for i := range req.Runs {
		if reason := a.admitFixRun(r, row.Candidate, &req.Runs[i], now); reason != "" {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("run %d: %s", i, reason))
			return
		}
	}
	limits := a.fixLimits()
	updated, err := store.RecordFixRuns(r.Context(), id, session.SessionID, req.Runs, limits, now)
	if !a.writeFixLeaseErr(w, err) {
		return
	}
	runs, err := store.ListFixRuns(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "reading fix runs failed")
		return
	}
	var next []fixclaims.Probe
	if !updated.Closed {
		next = a.fixProbes(r, updated, runs, nil, limits)
	}
	if next == nil {
		next = []fixclaims.Probe{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "RECORDED",
		"record":     fixRecord(updated, runs),
		"nextProbes": next,
	})
}

// admitFixRun is the integrity rule: a PASS or FAIL is admitted only with
// a receipt this server holds, for the sample named, that reached the same
// verdict, on a sample whose manifest pins the release the run is filed
// under. It normalizes the run in place and returns a reason to refuse.
func (a *api) admitFixRun(r *http.Request, c fixclaims.Candidate, run *fixclaims.Run, now time.Time) string {
	run.Version = domain.CanonicalVersion(c.Ecosystem, strings.TrimSpace(run.Version))
	if !domain.ConcreteResolvedVersion(run.Version) {
		return "version is not a concrete release"
	}
	env := &run.Environment
	env.OS = strings.ToLower(strings.TrimSpace(env.OS))
	env.Arch = strings.ToLower(strings.TrimSpace(env.Arch))
	env.Runtime = strings.ToLower(strings.TrimSpace(env.Runtime))
	env.RuntimeVersion = strings.ToLower(strings.TrimSpace(env.RuntimeVersion))
	if env.OS == "" {
		return "environment.os is required"
	}
	for _, tok := range []string{env.OS, env.Arch, env.Runtime, env.RuntimeVersion} {
		if tok != "" && !fixEnvToken.MatchString(tok) {
			return "environment values must be short lowercase tokens"
		}
	}
	run.Verdict = fixclaims.Verdict(strings.ToUpper(strings.TrimSpace(string(run.Verdict))))
	if !fixclaims.ValidVerdict(run.Verdict) {
		return "verdict must be PASS, FAIL or UNRUNNABLE"
	}
	run.FailureFingerprint = strings.ToLower(strings.TrimSpace(run.FailureFingerprint))
	if run.FarmSeconds < 0 {
		return "farmSeconds must not be negative"
	}
	if run.ObservedAt.IsZero() || run.ObservedAt.After(now.Add(5*time.Minute)) {
		run.ObservedAt = now
	}
	if run.Verdict == fixclaims.VerdictUnrunnable {
		run.ReceiptID, run.FailureFingerprint = "", ""
		return ""
	}
	if run.ReceiptID == "" || run.SampleID == "" {
		return "a PASS or FAIL run must cite receiptId and sampleId"
	}
	if run.Verdict == fixclaims.VerdictFail && !fixHexDigest.MatchString(run.FailureFingerprint) {
		return "a FAIL run must carry a 64-hex failureFingerprint"
	}
	if run.Verdict == fixclaims.VerdictPass {
		run.FailureFingerprint = ""
	}
	sample, found, err := a.d.Store.GetSample(r.Context(), run.SampleID)
	if err != nil || !found {
		return "sampleId is not a stored sample"
	}
	if !fixSamplePins(sample, c, run.Version) {
		return "the sample does not pin " + domain.PURL{Ecosystem: c.Ecosystem, Name: c.Name, Version: run.Version}.String()
	}
	receipts, err := a.d.Store.ReceiptsForSample(r.Context(), run.SampleID)
	if err != nil {
		return "receipts unavailable"
	}
	for _, rc := range receipts {
		if rc.ReceiptID != run.ReceiptID {
			continue
		}
		if rc.ContractResult != string(run.Verdict) {
			return fmt.Sprintf("receipt %s recorded contract %s, not %s", rc.ReceiptID, rc.ContractResult, run.Verdict)
		}
		return ""
	}
	return "receiptId is not a stored receipt for this sample"
}

// fixSamplePins reports whether the sample's manifest names exactly this
// release of the candidate's package.
func fixSamplePins(sample serverstore.SampleRow, c fixclaims.Candidate, version string) bool {
	var m domain.SampleManifest
	if err := json.Unmarshal([]byte(sample.ManifestJSON), &m); err != nil {
		return false
	}
	want := domain.PURL{Ecosystem: c.Ecosystem, Name: c.Name, Version: version}
	names := append([]string{m.Subject}, m.Packages...)
	names = append(names, m.Case.Packages...)
	for _, raw := range names {
		if raw == "" {
			continue
		}
		p, err := domain.ParsePURL(raw)
		if err != nil {
			continue
		}
		if p.Ecosystem == want.Ecosystem && strings.EqualFold(p.Name, want.Name) && domain.CanonicalVersion(p.Ecosystem, p.Version) == want.Version {
			return true
		}
	}
	return false
}

type fixOutcomeRequest struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Outcome       serverstore.FixWorkOutcome `json:"outcome"`
	Detail        string                     `json:"detail,omitempty"`
}

// handleFixOutcome is POST /v1/fix-claims/{id}/outcome.
func (a *api) handleFixOutcome(w http.ResponseWriter, r *http.Request) {
	store, ok := a.fixClaimStore()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "fix-claim storage unavailable")
		return
	}
	session, ok := a.fixClaimSession(w, r)
	if !ok {
		return
	}
	id, ok := fixClaimID(w, r)
	if !ok {
		return
	}
	var req fixOutcomeRequest
	if !decodeStrict(w, r, 4<<10, &req, "fix outcome") {
		return
	}
	req.Outcome = serverstore.FixWorkOutcome(strings.ToUpper(strings.TrimSpace(string(req.Outcome))))
	if req.SchemaVersion != 1 || !serverstore.ValidFixWorkOutcome(req.Outcome) {
		writeErr(w, http.StatusBadRequest, "unsupported fix work outcome")
		return
	}
	if len(req.Detail) > 500 {
		req.Detail = req.Detail[:500]
	}
	row, err := store.ReleaseFixWork(r.Context(), id, session.SessionID, req.Outcome, req.Detail, a.fixLimits(), a.now().UTC())
	if !a.writeFixLeaseErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "RELEASED", "record": fixRecord(row, nil)})
}

func (a *api) writeFixLeaseErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, serverstore.ErrFixCandidateMissing):
		writeErr(w, http.StatusNotFound, "fix candidate not found")
	case errors.Is(err, serverstore.ErrFixLeaseMissing):
		writeErr(w, http.StatusConflict, "this session does not hold a live lease on the candidate")
	default:
		writeStoreErr(w, err, http.StatusInternalServerError, "fix work update failed")
	}
	return false
}

func fixClaimID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid fix candidate id")
		return 0, false
	}
	return id, true
}

// ---------------------------------------------------------------- reads --

// handleFixClaimsList is GET /v1/fix-claims: "was bug X fixed in Y?".
// A package is required so the read stays bounded by an index.
func (a *api) handleFixClaimsList(w http.ResponseWriter, r *http.Request) {
	store, ok := a.fixClaimStore()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "fix-claim storage unavailable")
		return
	}
	q := r.URL.Query()
	query := serverstore.FixClaimQuery{
		Ecosystem: strings.ToLower(strings.TrimSpace(q.Get("ecosystem"))),
		Name:      strings.TrimSpace(q.Get("name")),
		Version:   strings.TrimSpace(q.Get("version")),
		Status:    fixclaims.Status(strings.ToUpper(strings.TrimSpace(q.Get("status")))),
	}
	if raw := strings.TrimSpace(q.Get("purl")); raw != "" {
		p, err := domain.ParsePURL(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid purl")
			return
		}
		query.Ecosystem, query.Name = p.Ecosystem, p.Name
		if p.Version != "" && p.Version != "*" {
			query.Version = p.Version
		}
	}
	if query.Ecosystem == "" || query.Name == "" {
		writeErr(w, http.StatusBadRequest, "purl, or ecosystem and name, is required")
		return
	}
	if query.Version != "" {
		query.Version = domain.CanonicalVersion(query.Ecosystem, query.Version)
	}
	if query.Status != "" && !fixclaims.ValidStatus(query.Status) {
		writeErr(w, http.StatusBadRequest, "unknown status")
		return
	}
	rows, err := store.ListFixCandidates(r.Context(), query, fixClaimsListLimit)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "reading fix claims failed")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, fixRecord(row, nil))
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "semantics": fixSemantics()})
}

// handleFixClaimGet is GET /v1/fix-claims/{id}: one record with its runs.
func (a *api) handleFixClaimGet(w http.ResponseWriter, r *http.Request) {
	store, ok := a.fixClaimStore()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "fix-claim storage unavailable")
		return
	}
	id, ok := fixClaimID(w, r)
	if !ok {
		return
	}
	row, found, err := store.GetFixCandidate(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "reading fix claim failed")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "fix claim not found")
		return
	}
	runs, err := store.ListFixRuns(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "reading fix runs failed")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, fixRecord(row, runs))
}

// handleFixMetrics is GET /v1/fix-claims/metrics: the Phase 0 numbers.
// Session-gated because it is a whole-table read, bounded only by the
// Phase 0 candidate cap.
func (a *api) handleFixMetrics(w http.ResponseWriter, r *http.Request) {
	store, ok := a.fixClaimStore()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "fix-claim storage unavailable")
		return
	}
	if _, ok := a.fixClaimSession(w, r); !ok {
		return
	}
	states, ingested, rejected, err := store.FixClaimStates(r.Context())
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "reading fix claim states failed")
		return
	}
	writeJSON(w, http.StatusOK, fixclaims.Measure(states, ingested, rejected))
}

// fixRecord is the public shape of a claim record. verified is true only
// for VERIFIED_FIX and PARTIAL_FIX; a CLAIMED_FIX carries its upstream
// provenance and no evidence, and says so.
func fixRecord(row serverstore.FixCandidateRow, runs []serverstore.FixRunRow) map[string]any {
	c := row.Candidate
	ev := row.Evaluation
	fingerprint := ""
	for _, e := range ev.Environments {
		if e.BugFingerprint != "" {
			fingerprint = e.BugFingerprint
			break
		}
	}
	// Empty lists travel as [] and never as null: a reader counting
	// evidence must not have to special-case a claim that has none.
	nonNil := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	envs := ev.Environments
	if envs == nil {
		envs = []fixclaims.EnvironmentResult{}
	}
	rec := map[string]any{
		"id":                  row.ID,
		"status":              row.Status,
		"verified":            row.Status.Verified(),
		"pairOutcome":         row.PairOutcome,
		"package":             c.Package().String(),
		"ecosystem":           c.Ecosystem,
		"name":                c.Name,
		"claimedBadVersion":   c.ClaimedBadVersion,
		"claimedFixedVersion": c.ClaimedFixedVersion,
		"badVersion":          ev.BadVersion,
		"goodVersion":         ev.GoodVersion,
		"symbols":             nonNil(c.Symbols),
		"environmentHints":    nonNil(c.EnvironmentHints),
		"environments":        envs,
		"failureFingerprint":  fingerprint,
		"sampleId":            row.ReproducerSampleID,
		"evidence":            nonNil(ev.Evidence),
		"upstream": map[string]any{
			"type":       c.SourceType,
			"url":        c.SourceURL,
			"references": nonNil(c.References),
			"claim":      c.Claim,
			"confidence": c.Confidence,
		},
		"reproducer": map[string]any{"source": row.ReproducerSource, "sampleId": row.ReproducerSampleID},
		"runCount":   row.RunCount,
		"attempts":   row.Attempts,
		"closed":     row.Closed,
		"createdAt":  row.CreatedAt,
		"updatedAt":  row.UpdatedAt,
	}
	if row.ClosedReason != "" {
		rec["closedReason"] = row.ClosedReason
	}
	if !ev.VerifiedAt.IsZero() {
		rec["verifiedAt"] = ev.VerifiedAt
	}
	if !row.EvaluatedAt.IsZero() {
		rec["evaluatedAt"] = row.EvaluatedAt
	}
	if runs != nil {
		rec["runs"] = fixRunViews(runs)
	}
	return rec
}

func fixRunViews(runs []serverstore.FixRunRow) []map[string]any {
	out := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		out = append(out, map[string]any{
			"version":            r.Version,
			"environment":        r.Environment,
			"verdict":            r.Verdict,
			"failureFingerprint": r.FailureFingerprint,
			"receiptId":          r.ReceiptID,
			"sampleId":           r.SampleID,
			"farmSeconds":        r.FarmSeconds,
			"observedAt":         r.ObservedAt,
		})
	}
	return out
}

// fixSemantics travels with every list so a reader never has to guess what
// a status means, and never reads CLAIMED_FIX as verification.
func fixSemantics() map[string]string {
	return map[string]string{
		string(fixclaims.StatusClaimedFix):         "upstream says it is fixed; CodeSampleX has not executed anything",
		string(fixclaims.StatusReproducedBug):      "the reproducer failed on the bad release; the fixed release is untested or failed the same way",
		string(fixclaims.StatusVerifiedFix):        "same reproducer, same environment: FAIL on the bad release, PASS on the claimed-fixed release, both under receipts",
		string(fixclaims.StatusPartialFix):         "reproduced in more than one environment and fixed only in some",
		string(fixclaims.StatusClaimNotReproduced): "the bounded test could not make the bad release fail; not a PASS for the fix",
		string(fixclaims.StatusRegressed):          "the bug's fingerprint appeared again above a verified-good boundary",
	}
}
