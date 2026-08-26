package httpapi

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// getBody reads one public response as text, so a test can assert what is
// NOT in it.
func getBody(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	return string(raw)
}

func base64Sign(t *testing.T, priv ed25519.PrivateKey, r domain.VerificationReceipt) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(signWith(t, priv, r))
}

func anomalyEnvelopeFor(report domain.AnomalyReport) anomalyEnvelope {
	return anomalyEnvelope{
		SchemaVersion: 1,
		Epoch:         "2026-08-24",
		AnonID:        "0123456789abcdef",
		Report:        report,
	}
}

// mismatchAgainst is the report an agent files when the network told it a
// sample passes and its own machine says otherwise.
func mismatchAgainst(sampleID string) domain.AnomalyReport {
	return domain.AnomalyReport{
		SchemaVersion: 1,
		AnomalyType:   domain.AnomalyCSXPassLocalFail,
		Package:       "pkg:npm/axios@1.12.0",
		Symbol:        "axios.post",
		Environment:   nodeEnv("esm"),
		SampleID:      sampleID,
		CSXObserved:   domain.AnomalyObservation{Result: "PASS", Stage: "contract", Detail: "sample contract recorded PASS"},
		LocalObserved: domain.AnomalyObservation{Result: "FAIL", Stage: "test", Detail: "axios.post rejects the same body"},
		Reproducible:  "yes",
		Confidence:    "high",
		ErrorCode:     "ERR_MODULE_NOT_FOUND",
		LLMHypothesis: "probably an ESM/CJS interop difference",
	}
}

func countCrossJobs(t *testing.T, store *serverstore.Fake, sampleID string) int {
	t.Helper()
	jobs, err := store.JobsForSample(context.Background(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, j := range jobs {
		if j.Reason == "cross" {
			n++
		}
	}
	return n
}

// This wrapper recreates the old handler race deterministically: both
// JobsForSample reads complete before either CreateJob can run. The fixed
// handler never calls these split operations; its promoted EnsureCrossJob
// method goes straight to the embedded store's atomic implementation.
type legacyCrossJobRaceStore struct {
	serverstore.Store
	serverstore.AnomalyStore
	reads atomic.Int32
	gate  chan struct{}
}

func (s *legacyCrossJobRaceStore) JobsForSample(ctx context.Context, sampleID string) ([]serverstore.JobRow, error) {
	if s.reads.Add(1) == 2 {
		close(s.gate)
	}
	<-s.gate
	return s.Store.JobsForSample(ctx, sampleID)
}

// The completion criterion, end to end: a mismatch is reported, the fleet's
// own queue receives work, a real worker claims it, its signed receipt comes
// back, and the report ends CONFIRMED — with nothing public touched before
// that receipt existed.
func TestAReportedMismatchIsVerifiedByTheFleetAndConfirmed(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "a1")
	ctx := context.Background()

	var out anomalyResponse
	resp := postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(mismatchAgainst(sampleID)), &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.Status != "accepted" || out.ReportID == 0 {
		t.Fatalf("report response = %+v", out)
	}
	if out.VerificationJobID == 0 {
		t.Fatalf("no verification job was queued: %+v", out)
	}
	if !strings.Contains(out.Note, "VERIFICATION PENDING") {
		t.Fatalf("the response must say nothing is confirmed yet, got %q", out.Note)
	}

	// The job is in the canonical queue the deployed fleet already polls.
	job, found, err := store.Job(ctx, out.VerificationJobID)
	if err != nil || !found {
		t.Fatalf("queued job missing: found=%v err=%v", found, err)
	}
	if job.Reason != "cross" || job.SampleID != sampleID || job.Status != "open" {
		t.Fatalf("queued job = %+v; a worker in the field must be able to claim it", job)
	}

	// A worker claims it. That claim is the receipt an operator reads to
	// tell a slow fleet from a stuck one.
	priv, peerID := newPeer(t)
	var claim map[string]any
	resp = postJSON(t, srv.URL+"/v1/verification/jobs/"+strconv.FormatInt(out.VerificationJobID, 10)+"/claim",
		map[string]string{"peerId": peerID}, &claim)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("claim status = %d", resp.StatusCode)
	}
	claimed, _, _ := store.AnomalyReportByID(ctx, out.ReportID)
	if claimed.Status != domain.AnomalyStatusVerifying {
		t.Fatalf("after a worker claimed the job the report reads %q", claimed.Status)
	}

	// Before the receipt exists, nothing is confirmed.
	if claimed.Verdict != "" {
		t.Fatalf("a verdict appeared before any verification ran: %q", claimed.Verdict)
	}

	// The worker's run contradicts what the network served.
	receipt := signedReceipt(t, priv, sampleID, nodeEnv("esm"), "FAIL")
	var verify verifyResponse
	resp = postJSON(t, srv.URL+"/v1/verifications", receipt, &verify)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("receipt status = %d", resp.StatusCode)
	}

	decided, _, _ := store.AnomalyReportByID(ctx, out.ReportID)
	if decided.Verdict != domain.AnomalyVerdictCSXDefect {
		t.Fatalf("verdict = %q, want %q", decided.Verdict, domain.AnomalyVerdictCSXDefect)
	}
	if decided.Status != domain.AnomalyStatusVerified {
		t.Fatalf("status = %q, want verified", decided.Status)
	}
	if !domain.AnomalyVerdictConfirmed(decided.Verdict) {
		t.Fatal("a confirmed mismatch must be promotable")
	}
	if decided.VerdictAt.IsZero() {
		t.Fatal("report → verdict latency cannot be measured without a verdict time")
	}
}

// Dedupe is the whole defence against one wrong answer queuing a container
// per agent that meets it.
func TestADuplicateReportCreatesNoNewVerificationWork(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "b2")

	var first anomalyResponse
	postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(mismatchAgainst(sampleID)), &first)
	if first.Status != "accepted" {
		t.Fatalf("first report = %+v", first)
	}
	jobsAfterFirst := countCrossJobs(t, store, sampleID)

	// The same mismatch, from a different agent, worded differently.
	second := mismatchAgainst(sampleID)
	second.LLMHypothesis = "I now think this is a bundler problem"
	second.Confidence = "low"
	envelope := anomalyEnvelopeFor(second)
	envelope.AnonID = "fedcba9876543210"

	var out anomalyResponse
	resp := postJSON(t, srv.URL+"/v1/anomalies", envelope, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if out.Status != "duplicate" {
		t.Fatalf("status = %q, want duplicate", out.Status)
	}
	if out.MatchedReportID != first.ReportID {
		t.Fatalf("matchedReportId = %d, want %d", out.MatchedReportID, first.ReportID)
	}
	if out.Submissions != 2 {
		t.Fatalf("submissions = %d, want 2", out.Submissions)
	}
	if out.RetryAfterSeconds <= 0 {
		t.Fatal("a duplicate inside the cooldown must tell the caller when asking again is worth anything")
	}
	if got := countCrossJobs(t, store, sampleID); got != jobsAfterFirst {
		t.Fatalf("cross jobs went %d → %d: a duplicate queued new work", jobsAfterFirst, got)
	}
}

func TestDistinctConcurrentReportsReuseOneCrossJob(t *testing.T) {
	var racing *legacyCrossJobRaceStore
	srv, store, _ := newTestServer(t, func(d *Deps) {
		racing = &legacyCrossJobRaceStore{
			Store: d.Store, AnomalyStore: d.Store.(serverstore.AnomalyStore), gate: make(chan struct{}),
		}
		d.Store = racing
	})
	sampleID := saveSampleForVerification(t, store, "cc")
	reports := []domain.AnomalyReport{mismatchAgainst(sampleID), mismatchAgainst(sampleID)}
	reports[1].Symbol = "axios.get" // a distinct fingerprint, same sample

	type result struct {
		status int
		body   anomalyResponse
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, len(reports))
	var wg sync.WaitGroup
	for _, report := range reports {
		report := report
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			raw, err := json.Marshal(anomalyEnvelopeFor(report))
			if err != nil {
				results <- result{err: err}
				return
			}
			resp, err := http.Post(srv.URL+"/v1/anomalies", "application/json", strings.NewReader(string(raw)))
			if err != nil {
				results <- result{err: err}
				return
			}
			defer resp.Body.Close()
			var body anomalyResponse
			err = json.NewDecoder(resp.Body).Decode(&body)
			results <- result{status: resp.StatusCode, body: body, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for got := range results {
		if got.err != nil || got.status != http.StatusOK || got.body.VerificationJobID == 0 {
			t.Fatalf("concurrent report = status %d body %+v err=%v", got.status, got.body, got.err)
		}
	}
	if got := countCrossJobs(t, store, sampleID); got != 1 {
		t.Fatalf("two fingerprints raced into %d cross jobs, want 1", got)
	}
}

// The single most important refusal on the wire.
func TestAPureHypothesisIsRefusedOnTheWire(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "c3")

	report := mismatchAgainst(sampleID)
	report.LocalObserved = domain.AnomalyObservation{Detail: "I think this sample is wrong"}
	report.LLMHypothesis = "the version resolution looks suspicious to me"

	var body map[string]string
	resp := postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body["error"], "hypothes") {
		t.Fatalf("the refusal must say why: %q", body["error"])
	}
	if countCrossJobs(t, store, sampleID) != 0 {
		t.Fatal("a rejected report queued verification work")
	}
	rows, _ := store.ListAnomalyReports(context.Background(), 10)
	if len(rows) != 0 {
		t.Fatalf("a rejected report was stored anyway: %+v", rows)
	}
}

// A model told not to include a path may include one anyway. The facts are
// kept; the path is not.
func TestIdentifyingMaterialIsRedactedBeforeItIsStored(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "d4")

	report := mismatchAgainst(sampleID)
	report.LocalObserved.Detail = `failed in C:\Users\ana\acme-billing\src\pay.ts with token Zq7bY2mK9pW3nT6vR8sL1xC5dH0gJe4A`
	report.LLMHypothesis = "see https://internal.acme.corp/runbook for the same failure"
	private := "/home/alice/private-project"
	report.Symbol = private + "/secret-symbol"
	report.Environment.Ecosystem = private + "/ecosystem"
	report.Environment.OS = private + "/os"
	report.Environment.OSVersionBucket = private + "/os-version"
	report.Environment.Arch = private + "/arch"
	report.Environment.Runtime = private + "/runtime"
	report.Environment.RuntimeVersion = private + "/runtime-version"
	report.Environment.Language = private + "/language"
	report.Environment.LanguageVersion = private + "/language-version"
	report.Environment.Compiler = private + "/compiler"
	report.Environment.CompilerVersion = private + "/compiler-version"
	report.Environment.PackageManager = private + "/package-manager"
	report.Environment.PackageManagerVersion = private + "/package-manager-version"
	report.Environment.ModuleSystem = private + "/module-system"
	report.Environment.ExecutionContext = private + "/execution-context"
	report.Environment.BrowserFamily = private + "/browser"
	report.Environment.BrowserMajor = private + "/browser-version"
	report.Environment.Engine = private + "/engine"
	report.Environment.EngineVersion = private + "/engine-version"
	report.Environment.Virtualization = private + "/virtualization"
	report.Environment.ContainerRuntime = private + "/container-runtime"
	report.Environment.Libc = private + "/libc"
	report.Environment.LibcVersion = private + "/libc-version"
	report.Environment.Distro = private + "/distro"
	report.Environment.Frameworks = []string{private + "/framework"}

	var out anomalyResponse
	resp := postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: redaction keeps the facts rather than dropping the report", resp.StatusCode)
	}
	if !out.Redacted {
		t.Fatal("the response must admit that something was removed")
	}
	rows, err := store.ListAnomalyReports(context.Background(), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("stored rows = %+v err=%v", rows, err)
	}
	stored := rows[0].ReportJSON
	for _, secret := range []string{"Users\\ana", "acme-billing", "pay.ts", "Zq7bY2mK9pW3nT6vR8sL1xC5dH0gJe4A", "internal.acme.corp", "alice", "private-project", "secret-symbol"} {
		if strings.Contains(stored, secret) {
			t.Fatalf("%q reached storage: %s", secret, stored)
		}
	}
	// And the fact that made the report admissible is still there.
	var back domain.AnomalyReport
	if err := json.Unmarshal([]byte(stored), &back); err != nil {
		t.Fatal(err)
	}
	if back.LocalObserved.Result != "FAIL" || back.Package != "pkg:npm/axios@1.12.0" {
		t.Fatalf("redaction destroyed the local facts: %+v", back)
	}
}

func TestStructuredAnomalyFieldsCannotCarryIdentifyingText(t *testing.T) {
	tests := []struct {
		name string
		set  func(*anomalyEnvelope)
	}{
		{"sample id", func(e *anomalyEnvelope) { e.Report.SampleID = "/home/alice/private-project" }},
		{"evidence id", func(e *anomalyEnvelope) { e.Report.EvidenceID = "/home/alice/private-project" }},
		{"search fingerprint", func(e *anomalyEnvelope) { e.Report.SearchFingerprint = "/home/alice/private-project" }},
		{"related id", func(e *anomalyEnvelope) { e.Report.RelatedIDs = []string{"/home/alice/private-project"} }},
		{"unchecked package in related ids", func(e *anomalyEnvelope) {
			e.Report.RelatedIDs = []string{"pkg:npm/@private/acme-billing@1.0.0"}
		}},
		{"csx stage", func(e *anomalyEnvelope) { e.Report.CSXObserved.Stage = "/home/alice/private-project" }},
		{"local stage", func(e *anomalyEnvelope) { e.Report.LocalObserved.Stage = "https://internal.example/stage" }},
		{"error code", func(e *anomalyEnvelope) { e.Report.ErrorCode = "/home/alice/private-project" }},
		{"error fingerprint", func(e *anomalyEnvelope) { e.Report.ErrorFingerprint = "/home/alice/private-project" }},
		{"epoch", func(e *anomalyEnvelope) { e.Epoch = "/home/alice/private-project" }},
		{"anon id", func(e *anomalyEnvelope) { e.AnonID = "https://internal.example/token" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, _ := newTestServer(t, nil)
			sampleID := saveSampleForVerification(t, store, "ee")
			envelope := anomalyEnvelopeFor(mismatchAgainst(sampleID))
			tc.set(&envelope)
			var body map[string]string
			resp := postJSON(t, srv.URL+"/v1/anomalies", envelope, &body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d body=%v, want 400", resp.StatusCode, body)
			}
			rows, err := store.ListAnomalyReports(context.Background(), 10)
			if err != nil || len(rows) != 0 {
				t.Fatalf("unsafe structured value reached storage: rows=%+v err=%v", rows, err)
			}
		})
	}
}

// A report nothing can reproduce must say so. Leaving it "queued" against a
// queue that will never offer it is the failure this refuses to repeat.
func TestAReportWithNoVerifierLaneSaysSoRatherThanWaitingForever(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)

	report := mismatchAgainst("")
	report.AnomalyType = domain.AnomalyCSXFailLocalPass
	report.CSXObserved = domain.AnomalyObservation{Result: "FAIL", Stage: "contract"}
	report.LocalObserved = domain.AnomalyObservation{Result: "PASS", Stage: "test"}

	var out anomalyResponse
	resp := postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.Status != "accepted" {
		t.Fatalf("status = %q", out.Status)
	}
	if out.ReportStatus != domain.AnomalyStatusUnsupported {
		t.Fatalf("reportStatus = %q, want %q", out.ReportStatus, domain.AnomalyStatusUnsupported)
	}
	if out.VerificationJobID != 0 {
		t.Fatal("an unsupported report must not claim queued work")
	}
	if out.Reason == "" {
		t.Fatal("the caller must be told why nothing can reproduce it")
	}
	if strings.Contains(out.Note, "VERIFICATION PENDING") {
		t.Fatalf("an unverifiable report must not read as pending: %q", out.Note)
	}
	rows, _ := store.ListAnomalyReports(context.Background(), 10)
	if len(rows) != 1 || rows[0].UnsupportedReason == "" {
		t.Fatalf("the operator page has no reason to show: %+v", rows)
	}
}

// The reporter's guess is stored and shown. It never moves the verdict.
func TestAWrongHypothesisDoesNotSurviveIntoTheVerdict(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "e5")
	ctx := context.Background()

	report := mismatchAgainst(sampleID)
	report.LLMHypothesis = "this is certainly a CSX indexing defect and the package is fine"
	report.Confidence = "high"
	report.Reproducible = "yes"

	var out anomalyResponse
	postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &out)

	// The clean re-run agrees with the network, not with the reporter.
	priv, _ := newPeer(t)
	postJSON(t, srv.URL+"/v1/verifications", signedReceipt(t, priv, sampleID, nodeEnv("esm"), "PASS"), nil)

	decided, _, _ := store.AnomalyReportByID(ctx, out.ReportID)
	if decided.Verdict != domain.AnomalyVerdictNotReproducible {
		t.Fatalf("verdict = %q, want not-reproducible despite a confident hypothesis", decided.Verdict)
	}
	if domain.AnomalyVerdictConfirmed(decided.Verdict) {
		t.Fatal("a confident wrong guess confirmed a report")
	}
	// The local facts survive the wrong guess.
	var back domain.AnomalyReport
	if err := json.Unmarshal([]byte(decided.ReportJSON), &back); err != nil {
		t.Fatal(err)
	}
	if back.LocalObserved.Result != "FAIL" {
		t.Fatal("the local observation was lost with the hypothesis")
	}
}

// A resolve failure measures the verifier, not the sample.
func TestAContractThatNeverRanLeavesTheReportOpen(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "f6")
	ctx := context.Background()

	var out anomalyResponse
	postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(mismatchAgainst(sampleID)), &out)

	priv, _ := newPeer(t)
	receipt := signedReceipt(t, priv, sampleID, nodeEnv("esm"), "SKIPPED")
	receipt.Stages["resolve"] = "FAIL"
	receipt.Stages["compile"] = "SKIPPED"
	receipt.PeerSignature = base64Sign(t, priv, receipt)
	postJSON(t, srv.URL+"/v1/verifications", receipt, nil)

	row, _, _ := store.AnomalyReportByID(ctx, out.ReportID)
	if row.Verdict != "" {
		t.Fatalf("a run that never reached the contract produced verdict %q", row.Verdict)
	}
}

// Confirmed or not, a report is never itself public. The only thing a reader
// ever sees is the signed receipt a verifier wrote.
func TestNoPublicRouteServesAnomalyReports(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "a7")

	report := mismatchAgainst(sampleID)
	report.LocalObserved.Detail = "unmistakable-report-marker-9713"
	report.LLMHypothesis = "unmistakable-hypothesis-marker-4420"
	var out anomalyResponse
	postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &out)
	if out.ReportID == 0 {
		t.Fatalf("report not accepted: %+v", out)
	}

	for _, path := range []string{
		"/v1/stats",
		"/v1/wanted",
		"/v1/samples/" + sampleID,
		"/v1/registry/packages/" + url.PathEscape("pkg:npm/axios@1.12.0"),
		"/v1/adapters",
	} {
		body := getBody(t, srv.URL+path)
		for _, marker := range []string{"unmistakable-report-marker-9713", "unmistakable-hypothesis-marker-4420", "anomaly"} {
			if strings.Contains(strings.ToLower(body), marker) {
				t.Fatalf("%s leaked %q into a public response", path, marker)
			}
		}
	}
	_ = store
}

// "No verdict" must not mean "wait forever". When the sample has no
// verification left coming, the honest close is that the network tried and
// could not measure it — a report open against an empty queue is the pending
// that never ends, and this channel is not allowed to produce one.
func TestAReportWithNothingLeftToMeasureIsClosedRatherThanLeftPending(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "b8")
	ctx := context.Background()

	var out anomalyResponse
	postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(mismatchAgainst(sampleID)), &out)
	if out.VerificationJobID == 0 {
		t.Fatalf("nothing was queued: %+v", out)
	}
	// Spend every cross attempt the sample is allowed, so the receipt path
	// has nothing left to requeue.
	for i := 0; i < maxCrossAttempts; i++ {
		if _, err := store.CreateJob(ctx, serverstore.JobRow{
			SampleID: sampleID, Reason: "cross", Status: "done",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CompleteJob(ctx, out.VerificationJobID); err != nil {
		t.Fatal(err)
	}

	// A run that died at resolve: it measured the verifier, not the sample.
	priv, _ := newPeer(t)
	receipt := signedReceipt(t, priv, sampleID, nodeEnv("esm"), "SKIPPED")
	receipt.Stages["resolve"] = "FAIL"
	receipt.Stages["compile"] = "SKIPPED"
	receipt.PeerSignature = base64Sign(t, priv, receipt)
	postJSON(t, srv.URL+"/v1/verifications", receipt, nil)

	row, _, _ := store.AnomalyReportByID(ctx, out.ReportID)
	if row.Verdict != domain.AnomalyVerdictInsufficientEvidence {
		t.Fatalf("verdict = %q, want insufficient-evidence", row.Verdict)
	}
	if domain.AnomalyVerdictConfirmed(row.Verdict) {
		t.Fatal("a report nothing could measure must not confirm anything")
	}
}
