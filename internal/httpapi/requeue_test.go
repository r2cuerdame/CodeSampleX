package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"net/http/httptest"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A verifier that cannot resolve dependencies consumed the sample's only
// cross-verification job and closed it: the job went to 'done' whatever the
// receipt said, and nothing ever queued another. Production stranded 159
// authoring drafts that way, each holding one SKIPPED receipt from a machine
// that never got as far as running the contract.
//
// SKIPPED is not FAIL. FAIL is a measurement of the sample; SKIPPED is the
// verifier reporting it measured nothing, and closing the work on it throws
// away the sample rather than the verifier.
func TestSkippedCrossVerificationQueuesAnotherAttempt(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID, jobID, priv, _ := seedQuarantinedDraft(t, store, srv)

	resp := postReceipt(t, srv, priv, sampleID, jobID, "SKIPPED")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("receipt rejected: %d", resp.StatusCode)
	}

	jobs, err := store.JobsForSample(t.Context(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, j := range jobs {
		if j.Reason == "cross" && (j.Status == "open" || j.Status == "claimed") {
			open++
		}
	}
	if open != 1 {
		t.Errorf("open cross jobs after a skipped attempt = %d, want 1: %+v", open, jobs)
	}
	sample, ok, err := store.GetSample(t.Context(), sampleID)
	if err != nil || !ok {
		t.Fatal("sample vanished")
	}
	if sample.Status != "DRAFT" || !sample.Quarantined {
		t.Errorf("sample = %s quarantined=%v, want it still awaiting verification",
			sample.Status, sample.Quarantined)
	}
}

// A contract that ran and failed is a result about the sample. Queueing
// another machine to run it again would be asking the network to keep
// trying until it hears what it wants.
func TestFailedCrossVerificationIsNotRetried(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID, jobID, priv, _ := seedQuarantinedDraft(t, store, srv)

	if resp := postReceipt(t, srv, priv, sampleID, jobID, "FAIL"); resp.StatusCode != http.StatusOK {
		t.Fatalf("receipt rejected: %d", resp.StatusCode)
	}
	jobs, err := store.JobsForSample(t.Context(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.Status == "open" || j.Status == "claimed" {
			t.Errorf("a measured failure queued another attempt: %+v", j)
		}
	}
}

// The retry is bounded. A sample nothing can resolve would otherwise cycle
// through every verifier forever, and the queue it crowds is the one real
// work waits in.
func TestSkippedRetriesAreBounded(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID, jobID, priv, _ := seedQuarantinedDraft(t, store, srv)

	// A different verifier each round, which is both what the retry is FOR
	// and what makes each receipt a distinct document.
	attempts := 0
	for jobID != 0 && attempts < maxCrossAttempts+3 {
		attempts++
		if resp := postReceipt(t, srv, priv, sampleID, jobID, "SKIPPED"); resp.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d rejected: %d", attempts, resp.StatusCode)
		}
		jobs, err := store.JobsForSample(t.Context(), sampleID)
		if err != nil {
			t.Fatal(err)
		}
		jobID = 0
		for _, j := range jobs {
			if j.Reason == "cross" && j.Status == "open" {
				jobID = j.ID
			}
		}
		if jobID == 0 {
			break
		}
		var peerID string
		priv, peerID = newPeer(t)
		if ok, err := store.ClaimJob(t.Context(), jobID, peerID); err != nil || !ok {
			t.Fatalf("claim retry job: ok=%v err=%v", ok, err)
		}
	}
	if attempts != maxCrossAttempts {
		t.Errorf("cross attempts = %d, want the cap of %d", attempts, maxCrossAttempts)
	}
}

// seedQuarantinedDraft creates the state an authoring worker leaves behind:
// a private draft with exactly one open cross job, claimed by a verifier.
func seedQuarantinedDraft(t *testing.T, store *serverstore.Fake, srv *httptest.Server) (string, int64, ed25519.PrivateKey, string) {
	t.Helper()
	sampleID := "sha256:" + strings.Repeat("5c", 32)
	manifest := testManifest()
	if err := store.SaveSample(t.Context(), serverstore.SampleRow{
		SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status: "DRAFT", License: "MIT-0", CreatedAt: testNow,
		Quarantined: true, QuarantineReason: "private authoring draft awaiting cross verification",
	}); err != nil {
		t.Fatal(err)
	}
	priv, peerID := newPeer(t)
	jobID, err := store.CreateJob(t.Context(), serverstore.JobRow{
		SampleID: sampleID, Reason: "cross", Status: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClaimJob(t.Context(), jobID, peerID); err != nil || !ok {
		t.Fatalf("claim cross job: ok=%v err=%v", ok, err)
	}
	return sampleID, jobID, priv, peerID
}

func postReceipt(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, sampleID string, jobID int64, contract string) *http.Response {
	t.Helper()
	receipt := signedReceipt(t, priv, sampleID, crossReceiptEnv(), contract)
	receipt.PeerSignature = base64.StdEncoding.EncodeToString(signWith(t, priv, receipt))
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/verifications", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if jobID != 0 {
		req.Header.Set(domain.VerificationJobIDHeader, strconv.FormatInt(jobID, 10))
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// crossReceiptEnv is a container environment, which is what a cross verifier
// reports and what the receipt's capability is derived from.
func crossReceiptEnv() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
		Runtime: "node", RuntimeVersion: "22", Language: "typescript",
		LanguageVersion: "5.9", ExecutionContext: "node", PackageManager: "npm",
		Virtualization: "container", ContainerRuntime: "docker",
		Distro: "alpine", OSVersionBucket: "alpine", Libc: "musl",
	}
}

// The retry only fires when a receipt closes a job, so the drafts already
// stranded before it existed have no future event to wake them. The boot
// reconcile is what reaches them.
func TestReconcileWakesDraftsStrandedBeforeTheRetryExisted(t *testing.T) {
	_, store, _ := newTestServer(t, nil)
	sampleID := "sha256:" + strings.Repeat("6d", 32)
	manifest := testManifest()
	if err := store.SaveSample(t.Context(), serverstore.SampleRow{
		SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status: "DRAFT", License: "MIT-0", CreatedAt: testNow,
		Quarantined: true, QuarantineReason: "private authoring draft awaiting cross verification",
	}); err != nil {
		t.Fatal(err)
	}
	jobID, err := store.CreateJob(t.Context(), serverstore.JobRow{
		SampleID: sampleID, Reason: "cross", Status: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = jobID

	woken, err := ReconcileStrandedDrafts(t.Context(), store, 200)
	if err != nil {
		t.Fatal(err)
	}
	if woken != 1 {
		t.Fatalf("reconcile woke %d drafts, want 1", woken)
	}
	jobs, err := store.JobsForSample(t.Context(), sampleID)
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, j := range jobs {
		if j.Reason == "cross" && j.Status == "open" {
			open++
		}
	}
	if open != 1 {
		t.Errorf("open cross jobs after reconcile = %d, want 1", open)
	}
	// And it is idempotent: a second boot must not pile on more work.
	if again, err := ReconcileStrandedDrafts(t.Context(), store, 200); err != nil || again != 0 {
		t.Errorf("second reconcile woke %d (err %v), want 0", again, err)
	}
}
